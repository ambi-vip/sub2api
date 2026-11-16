"""Offline resource regressions; run inside the deployment image with pids=512."""
import importlib.util
import json
import socket
import subprocess
import sys
import threading
import time
import unittest
from contextlib import contextmanager
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('resource_proxy', Path(__file__).parents[1] / 'assets' / 'bps-sanitize-proxy.py')
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)
POST = b'POST /v1/responses HTTP/1.1\r\nContent-Length: 2\r\n\r\n{}'


def response(client):
    data = b''
    while True:
        part = client.recv(65536)
        if not part:
            return data
        data += part


@contextmanager
def connection(request=POST):
    a, client = socket.socketpair()
    client.settimeout(3)
    p.CONNECTIONS.acquire()
    worker = threading.Thread(target=p.handle, args=(a, ('offline', 0), '127.0.0.1', 1))
    worker.start()
    try:
        client.sendall(request)
        yield client, worker
    finally:
        client.close()
        a.close()
        worker.join(3)


class ResourceTests(unittest.TestCase):
    def setUp(self):
        self.baseline = p.CONNECTIONS.count()
        self.logging = patch.object(p, 'log')
        self.logging.start()

    def tearDown(self):
        self.logging.stop()
        self.assertEqual(p.CONNECTIONS.count(), self.baseline)
        self.assertEqual(p.CONNECTIONS.snapshot()['phases'], {})

    def test_wait_timeout_is_json_without_upstream_or_permit_leak(self):
        sem = threading.BoundedSemaphore(1)
        sem.acquire()
        with patch.object(p, 'NORMALIZERS', sem), patch.object(p, 'NORMALIZER_WAIT_SECONDS', .15), patch.object(p, 'prepare_forward') as forward:
            started = time.monotonic()
            with connection() as (client, worker):
                data = response(client)
                worker.join(1)
                self.assertFalse(worker.is_alive())
            self.assertLess(time.monotonic() - started, 1)
            self.assertIn(b'HTTP/1.1 503', data)
            self.assertEqual(json.loads(data.split(b'\r\n\r\n', 1)[1])['error']['code'], 'proxy_normalizer_busy')
            forward.assert_not_called()
            self.assertFalse(sem.acquire(blocking=False))
        sem.release()

    def test_queued_disconnect_exits_before_long_wait_deadline(self):
        sem = threading.BoundedSemaphore(1)
        sem.acquire()
        with patch.object(p, 'NORMALIZERS', sem), patch.object(p, 'NORMALIZER_WAIT_SECONDS', 10), patch.object(p, 'prepare_forward') as forward:
            with connection() as (client, worker):
                deadline = time.monotonic() + 1
                while 'normalizer_wait' not in p.CONNECTIONS.snapshot()['phases'] and time.monotonic() < deadline:
                    time.sleep(.01)
                self.assertIn('normalizer_wait', p.CONNECTIONS.snapshot()['phases'])
                client.close()
                worker.join(.7)
                self.assertFalse(worker.is_alive())
            forward.assert_not_called()
            self.assertFalse(sem.acquire(blocking=False))
        sem.release()

    def test_trickled_body_has_absolute_deadline_and_releases_permit(self):
        sem = threading.BoundedSemaphore(1)
        stop = threading.Event()
        def trickle(client):
            while not stop.wait(.025):
                try:
                    client.sendall(b'x')
                except OSError:
                    return
        with patch.object(p, 'NORMALIZERS', sem), patch.object(p, 'PREPARE_SECONDS', .16):
            with connection(b'POST /v1/responses HTTP/1.1\r\nContent-Length: 100\r\n\r\nx') as (client, worker):
                sender = threading.Thread(target=trickle, args=(client,))
                sender.start()
                started = time.monotonic()
                try:
                    data = response(client)
                    worker.join(1)
                    self.assertLess(time.monotonic() - started, .9)
                    self.assertIn(b'proxy_preparation_timeout', data)
                    self.assertIn(b'HTTP/1.1 504', data)
                    self.assertFalse(worker.is_alive())
                    self.assertTrue(sem.acquire(blocking=False))
                    sem.release()
                finally:
                    stop.set()
                    sender.join(1)

    def test_transform_failure_releases_permit(self):
        sem = threading.BoundedSemaphore(1)
        with patch.object(p, 'NORMALIZERS', sem), patch.object(p, 'sanitize_body', side_effect=ValueError('offline failure')):
            with connection() as (client, worker):
                response(client)
                worker.join(1)
                self.assertFalse(worker.is_alive())
            self.assertTrue(sem.acquire(blocking=False))
            sem.release()

    def test_models_request_bypasses_saturated_normalizers(self):
        sem = threading.BoundedSemaphore(1)
        sem.acquire()
        upstream, fake = socket.socketpair()
        fake.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok')
        try:
            with patch.object(p, 'NORMALIZERS', sem), patch.object(p, 'prepare_forward', return_value=upstream):
                with connection(b'GET /v1/models HTTP/1.1\r\n\r\n') as (client, worker):
                    self.assertTrue(response(client).endswith(b'ok'))
                    worker.join(1)
                self.assertFalse(sem.acquire(blocking=False))
        finally:
            upstream.close()
            fake.close()
            sem.release()

    def test_websocket_bypasses_normalizers_and_releases_on_disconnect(self):
        sem = threading.BoundedSemaphore(1)
        sem.acquire()
        upstream, fake = socket.socketpair()
        fake.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nhello')
        try:
            with patch.object(p, 'NORMALIZERS', sem), patch.object(p.socket, 'create_connection', return_value=upstream):
                with connection(b'GET /v1/responses HTTP/1.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n') as (client, worker):
                    head, rest = p.read_head(client)
                    self.assertIn(b'101 Switching Protocols', head)
                    if not rest:
                        rest = client.recv(5)
                    self.assertEqual(rest, b'hello')
                    client.close()
                    worker.join(1)
                    self.assertFalse(worker.is_alive())
                self.assertFalse(sem.acquire(blocking=False))
        finally:
            upstream.close()
            fake.close()
            sem.release()

    def test_pid_guard_rejects_before_creating_worker(self):
        a, client = socket.socketpair()
        client.settimeout(3)
        try:
            with patch.object(p, 'pid_budget', return_value=(496, 512)), patch.object(p.threading, 'Thread') as factory:
                self.assertFalse(p.start_worker(a, ('offline', 0), '127.0.0.1', 1))
                factory.assert_not_called()
            data = response(client)
            body = json.loads(data.split(b'\r\n\r\n', 1)[1])
            self.assertEqual(body['error']['code'], 'proxy_resource_busy')
            self.assertEqual(body['error']['details']['reserved'], 16)
        finally:
            a.close()
            client.close()

    def test_worker_creation_failure_does_not_leak_counter(self):
        a, client = socket.socketpair()
        client.settimeout(3)
        try:
            with patch.object(p, 'pid_budget', return_value=None), patch.object(p.threading, 'Thread') as factory:
                factory.return_value.start.side_effect = RuntimeError('offline PID race')
                self.assertFalse(p.start_worker(a, ('offline', 0), '127.0.0.1', 1))
            self.assertIn(b'proxy_worker_unavailable', response(client))
        finally:
            a.close()
            client.close()

    def test_80_simultaneous_websockets_in_actual_container_budget(self):
        self.websocket_load(80)

    def test_actual_cgroup_pressure_preserves_fork_headroom(self):
        budget = p.pid_budget()
        if budget is None or budget[1] != 512:
            self.skipTest('requires isolated test container with pids_limit=512')
        self.websocket_load(512, expect_guard=True)

    def websocket_load(self, target, expect_guard=False):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0))
        listener.listen(128)
        listener.settimeout(.1)
        stop = threading.Event()
        upstreams, clients, errors = [], [], []
        def serve():
            while not stop.is_set():
                try:
                    upstream, _ = listener.accept()
                except socket.timeout:
                    continue
                except OSError:
                    return
                upstreams.append(upstream)
                upstream.settimeout(3)
                try:
                    p.read_head(upstream)
                    upstream.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n')
                except OSError as error:
                    errors.append(type(error).__name__)
        server = threading.Thread(target=serve)
        server.start()
        try:
            accepted = 0
            guarded = False
            for _ in range(target):
                a, client = socket.socketpair()
                clients.append(client)
                client.settimeout(3)
                if not p.start_worker(a, ('offline', 0), '127.0.0.1', listener.getsockname()[1]):
                    a.close()
                    if not expect_guard:
                        self.fail('worker refused below expected container budget')
                    self.assertIn(b'proxy_resource_busy', response(client))
                    guarded = True
                    break
                client.sendall(b'GET /v1/responses HTTP/1.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n')
                head, _ = p.read_head(client)
                if head is None:
                    self.fail('handshake closed: ' + json.dumps({
                        'accepted': accepted, 'client_fd': client.fileno(),
                        'budget': p.pid_budget(), 'server_errors': errors[-3:],
                        'proxy_logs': [str(c.args[0]) for c in p.log.call_args_list[-4:]],
                    }))
                self.assertIn(b'101 Switching Protocols', head)
                accepted += 1
            self.assertEqual(p.CONNECTIONS.count() - self.baseline, accepted)
            self.assertEqual(guarded, expect_guard)
            budget = p.pid_budget()
            if budget is not None:
                self.assertEqual(budget[1], 512, 'run this regression with the intended pids_limit=512')
                self.assertGreaterEqual(budget[0], 82)
                evidence = {'simultaneous_ws': accepted, 'pids_current': budget[0], 'pids_max': budget[1], 'guarded': guarded}
                memory = Path('/sys/fs/cgroup/memory.current')
                if memory.exists():
                    evidence['memory_bytes'] = int(memory.read_text())
                    memory_events = dict(line.split() for line in Path('/sys/fs/cgroup/memory.events').read_text().splitlines())
                    self.assertEqual(memory_events.get('oom_kill'), '0')
                    evidence['oom_kill'] = 0
                if expect_guard:
                    self.assertGreaterEqual(budget[1] - budget[0], p.PID_RESERVE)
                    fork = subprocess.run([sys.executable, '-c', 'print("fork-ok")'], capture_output=True, text=True, timeout=5)
                    self.assertEqual(fork.returncode, 0, fork.stderr)
                    self.assertEqual(fork.stdout.strip(), 'fork-ok')
                    evidence['fork_while_full'] = 'ok'
                    events = Path('/sys/fs/cgroup/pids.events')
                    if events.exists():
                        self.assertEqual(dict(line.split() for line in events.read_text().splitlines()).get('max'), '0')
                print('RESOURCE_EVIDENCE ' + json.dumps(evidence))
            for client in clients:
                client.close()
            deadline = time.monotonic() + 8
            while p.CONNECTIONS.count() != self.baseline and time.monotonic() < deadline:
                time.sleep(.01)
            self.assertEqual(p.CONNECTIONS.count(), self.baseline)
            self.assertEqual(errors, [])
            print('CLEANUP_EVIDENCE ' + json.dumps({'target': target, 'workers_after_close': p.CONNECTIONS.count(), 'phases': p.CONNECTIONS.snapshot()['phases']}))
        finally:
            stop.set()
            listener.close()
            for sock in clients + upstreams:
                sock.close()
            server.join(3)
            deadline = time.monotonic() + 8
            while p.CONNECTIONS.count() != self.baseline and time.monotonic() < deadline:
                time.sleep(.01)


if __name__ == '__main__':
    unittest.main()

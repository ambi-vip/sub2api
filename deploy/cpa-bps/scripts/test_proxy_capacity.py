"""Local sockets only: cancellation, stream framing and unrestricted admission."""
import importlib.util
import socket
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('capacity_proxy', Path(__file__).parents[1] / 'assets' / 'bps-sanitize-proxy.py')
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


class CapacityTests(unittest.TestCase):
    def test_64_streams_release_after_downstream_disconnect(self):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0)); listener.listen(128); listener.settimeout(.1)
        stop = threading.Event()
        sockets, clients, workers, errors = [], [], [], []
        baseline = p.CONNECTIONS.count()

        def fake():
            while not stop.is_set():
                try:
                    upstream, _ = listener.accept()
                except socket.timeout:
                    continue
                except OSError:
                    break
                sockets.append(upstream); upstream.settimeout(3)
                try:
                    p.read_head(upstream)
                    upstream.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n')
                except OSError as error:
                    errors.append(type(error).__name__)

        server = threading.Thread(target=fake)
        with patch.object(p, 'log'):
            server.start()
            try:
                for _ in range(64):
                    a, client = socket.socketpair()
                    sockets.append(a); clients.append(client); client.settimeout(3)
                    self.assertTrue(p.CONNECTIONS.acquire(blocking=False))
                    worker = threading.Thread(target=p.handle, args=(a, ('offline', 0), '127.0.0.1', listener.getsockname()[1]))
                    workers.append(worker); worker.start()
                    client.sendall(b'GET /hold HTTP/1.1\r\n\r\n')
                    head, rest = p.read_head(client)
                    self.assertTrue(head.startswith(b'HTTP/1.1 200'))
                    self.assertEqual(rest, b'')
                self.assertEqual(p.CONNECTIONS.count() - baseline, 64)
                self.assertTrue(all(w.is_alive() for w in workers))
                for client in clients:
                    client.close()
                deadline = time.monotonic() + 4
                for worker in workers:
                    worker.join(max(0, deadline - time.monotonic()))
                self.assertFalse(any(w.is_alive() for w in workers))
                self.assertEqual(p.CONNECTIONS.count(), baseline)
                self.assertEqual(errors, [])
            finally:
                stop.set(); listener.close()
                for sock in sockets + clients:
                    sock.close()
                server.join(3)
                for worker in workers:
                    worker.join(.2)

    def test_cancel_before_response_headers_closes_upstream(self):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0)); listener.listen(); listener.settimeout(3)
        accepted, released = threading.Event(), threading.Event()

        def fake():
            with listener.accept()[0] as upstream:
                upstream.settimeout(3)
                p.read_head(upstream); accepted.set()
                if upstream.recv(1) == b'':
                    released.set()

        a, client = socket.socketpair()
        p.CONNECTIONS.acquire()
        worker = threading.Thread(target=p.handle, args=(a, ('offline', 0), '127.0.0.1', listener.getsockname()[1]))
        server = threading.Thread(target=fake)
        with patch.object(p, 'log'):
            try:
                server.start(); worker.start()
                client.sendall(b'GET /hold HTTP/1.1\r\n\r\n')
                self.assertTrue(accepted.wait(2))
                client.close()
                self.assertTrue(released.wait(2))
                worker.join(2)
                self.assertFalse(worker.is_alive())
            finally:
                client.close(); a.close(); listener.close()
                server.join(4); worker.join(4)

    def test_large_chunk_is_incremental_and_trailers_preserved(self):
        source, upstream = socket.socketpair()
        out, client = socket.socketpair()
        payload = b'x' * (1024 * 1024)
        prefix = b'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n100000\r\n'
        ending = b'\r\n0\r\nX-Test: retained\r\n\r\n'
        finish = threading.Event()
        errors = []

        def producer():
            source.sendall(prefix + payload[:1024])
            finish.wait(3)
            source.sendall(payload[1024:] + ending)

        def relay():
            try:
                p.relay_response(upstream, out)
            except Exception as error:
                errors.append(type(error).__name__)

        server = threading.Thread(target=producer)
        worker = threading.Thread(target=relay)
        try:
            server.start(); worker.start(); client.settimeout(3)
            received = b''
            while len(received) < len(prefix) + 1024:
                received += client.recv(65536)
            self.assertEqual(received, prefix + payload[:1024])
            finish.set()
            expected = prefix + payload + ending
            while len(received) < len(expected):
                received += client.recv(65536)
            self.assertEqual(received, expected)
            worker.join(3)
            self.assertFalse(worker.is_alive())
            self.assertEqual(errors, [])
        finally:
            finish.set()
            for sock in [source, upstream, out, client]:
                sock.close()
            server.join(3); worker.join(3)


if __name__ == '__main__':
    unittest.main()

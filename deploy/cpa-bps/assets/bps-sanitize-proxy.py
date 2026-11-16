#!/usr/bin/env python3
"""BPS 请求清洗代理：把 Codex 发来的非法历史项在发送前剔掉。

## 为什么需要

Codex 的 app-server 在经过多智能体委派后，会往会话历史里写入
`function_call_output`，带 `name` / `namespace` 但**没有 `call_id`**，
而且这些工具在会话里根本没有对应的 `function_call`。官方 API 宽容，
BPS 通道是硬校验，整条请求直接 400：

    bps_request_unsupported; reason=missing_tool_call; path=input[87]
    hint=resend_complete_tool_call_and_result

修 rollout 只能治已发生的，app-server 在会话进行中还会继续写新的。
所以在转发链路上做一次清洗才是根治法。

## 行为

  * 只处理 `POST /v1/responses` 的 JSON 请求体：把 `input` 数组里
    「output 类型但没有 call_id、或 call_id 在本请求里找不到对应 call」的条目删掉；
  * 其它请求（/v1/models、/health、WS 升级等）原样透传；
  * 响应流式转发，不缓冲，SSE 不受影响；
  * 每处理一个请求打印一行：删了几条、状态码、路径。

## 用法

  python3 bps-sanitize-proxy.py --listen 0.0.0.0:8318 --upstream cpa-bps-core:8317
"""
import argparse
import json
import os
import math
import socket
import select
import sys
import threading
import time
from contextlib import contextmanager

CALL_TYPES = {"function_call", "custom_tool_call", "local_shell_call"}
OUT_TYPES = {"function_call_output", "custom_tool_call_output", "local_shell_call_output"}

STATS = {"requests": 0, "sanitized": 0, "dropped": 0,
         "normalizer_rejections": 0, "resource_rejections": 0}
LOCK = threading.Lock()


def positive_seconds(name, default):
    value = float(os.environ.get(name, default))
    if not math.isfinite(value) or value <= 0:
        raise ValueError('%s must be a positive finite duration' % name)
    return value


NORMALIZER_WAIT_SECONDS = positive_seconds('BPS_NORMALIZER_WAIT_SECONDS', '15')
PREPARE_SECONDS = positive_seconds('BPS_PREPARE_SECONDS', '60')
PID_RESERVE = int(os.environ.get('BPS_PID_RESERVE', '16'))
if PID_RESERVE < 1:
    raise ValueError('BPS_PID_RESERVE must be positive')


class ConnectionCounter:
    """Observe workers without imposing a request-count ceiling."""
    def __init__(self):
        self.active = 0
        self.lock = threading.Lock()
        self.workers = {}

    def acquire(self, blocking=False):
        with self.lock:
            self.active += 1
        return True

    def release(self):
        with self.lock:
            self.active -= 1
            self.workers.pop(threading.get_ident(), None)

    def count(self):
        with self.lock:
            return self.active

    def phase(self, name):
        with self.lock:
            self.workers[threading.get_ident()] = (name, time.monotonic())

    def snapshot(self):
        with self.lock:
            phases = {}
            now = time.monotonic()
            for name, started in self.workers.values():
                state = phases.setdefault(name, {'count': 0, 'oldest_seconds': 0})
                state['count'] += 1
                state['oldest_seconds'] = max(state['oldest_seconds'], round(now - started, 1))
            return {'active': self.active, 'phases': phases}


CONNECTIONS = ConnectionCounter()
NORMALIZERS = threading.BoundedSemaphore(2)


def log(msg):
    print(msg, flush=True)


class ClientDisconnected(ConnectionError):
    pass


class NormalizerBusy(RuntimeError):
    pass


class PreparationTimeout(TimeoutError):
    pass


def wait_readable(sockets, timeout):
    """poll supports high descriptor numbers; Windows retains socket select."""
    if not hasattr(select, 'poll'):
        return select.select(sockets, [], [], timeout)[0]
    poller = select.poll()
    by_fd = {}
    for sock in sockets:
        descriptor = sock.fileno()
        if descriptor < 0:
            raise OSError('socket closed while waiting')
        by_fd[descriptor] = sock
        poller.register(descriptor, select.POLLIN)
    milliseconds = None if timeout is None else math.ceil(max(0, timeout) * 1000)
    events = poller.poll(milliseconds)
    if any(flags & select.POLLNVAL for _, flags in events):
        raise OSError('socket invalid while waiting')
    return [by_fd[descriptor] for descriptor, _ in events]


def ensure_client_open(conn):
    """Check visible EOF without consuming or expanding a queued request body.

    If unread data hides EOF, the absolute queue deadline still bounds retention.
    """
    ready = wait_readable([conn], 0)
    if ready:
        try:
            data = conn.recv(1, socket.MSG_PEEK | getattr(socket, 'MSG_DONTWAIT', 0))
        except BlockingIOError:
            return
        except OSError as exc:
            raise ClientDisconnected('downstream closed while queued') from exc
        if not data:
            raise ClientDisconnected('downstream closed while queued')


@contextmanager
def normalizer_slot(conn):
    """Cancelable, finite admission; never retain an abandoned waiter forever."""
    deadline = time.monotonic() + NORMALIZER_WAIT_SECONDS
    acquired = False
    CONNECTIONS.phase('normalizer_wait')
    try:
        while not acquired:
            ensure_client_open(conn)
            left = deadline - time.monotonic()
            if left <= 0:
                raise NormalizerBusy('request preparation queue timed out')
            acquired = NORMALIZERS.acquire(timeout=min(.1, left))
        ensure_client_open(conn)
        CONNECTIONS.phase('preparing')
        yield
    finally:
        if acquired:
            NORMALIZERS.release()


def remaining(deadline):
    left = deadline - time.monotonic()
    if left <= 0:
        raise PreparationTimeout('request preparation deadline exceeded')
    return left


def receive_preparing(conn, count, deadline):
    conn.settimeout(min(30, remaining(deadline)))
    try:
        return conn.recv(count)
    except socket.timeout as exc:
        raise PreparationTimeout('request upload deadline or idle timeout exceeded') from exc


def send_error(conn, status, code, message, details=None, retry_after=None):
    error = {'code': code, 'message': message}
    if details is not None:
        error['details'] = details
    body = json.dumps({'error': error}, separators=(',', ':')).encode('utf-8')
    reason = {503: 'Service Unavailable', 504: 'Gateway Timeout'}[status]
    headers = 'HTTP/1.1 %s %s\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: %s\r\n' % (status, reason, len(body))
    if retry_after is not None:
        headers += 'Retry-After: %s\r\n' % retry_after
    try:
        conn.settimeout(2)
        conn.sendall(headers.encode('ascii') + b'\r\n' + body)
    except OSError:
        pass


def pid_budget():
    """Read the effective container budget, not a second connection-count cap."""
    for directory in ('/sys/fs/cgroup', '/sys/fs/cgroup/pids'):
        try:
            with open(directory + '/pids.max', encoding='ascii') as source:
                limit = source.read(32).strip()
            with open(directory + '/pids.current', encoding='ascii') as source:
                current = int(source.read(32))
            if limit == 'max':
                return None
            return current, int(limit)
        except (OSError, ValueError):
            continue
    return None


def log_state(reason, **details):
    details.update(CONNECTIONS.snapshot())
    log('[resource] %s %s' % (reason, json.dumps(details, separators=(',', ':'))))


def recv_upstream(upstream, client, count):
    """Release a worker when its downstream closes, even before upstream headers."""
    timeout = upstream.gettimeout()
    ready = wait_readable([upstream, client], timeout)
    if not ready:
        raise TimeoutError('upstream idle timeout')
    if client in ready:
        if not client.recv(1, socket.MSG_PEEK):
            raise ClientDisconnected('downstream disconnected')
        # This proxy handles one request per connection. Extra downstream bytes
        # must not cause a select busy-loop while waiting for the response.
        if upstream not in ready:
            ready = wait_readable([upstream], timeout)
            if not ready:
                raise TimeoutError('upstream idle timeout')
    return upstream.recv(count)


def read_head(conn, client=None, deadline=None):
    """读到 \\r\\n\\r\\n 为止，返回 (head_bytes, rest_bytes)。"""
    buf = b""
    while b"\r\n\r\n" not in buf:
        if client is not None:
            chunk = recv_upstream(conn, client, 65536)
        elif deadline is not None:
            chunk = receive_preparing(conn, 65536, deadline)
        else:
            chunk = conn.recv(65536)
        if not chunk:
            return None, b""
        buf += chunk
        if len(buf) > 1 << 20:
            return None, b""
    head, rest = buf.split(b"\r\n\r\n", 1)
    return head, rest


def parse_head(head):
    lines = head.decode("iso-8859-1").split("\r\n")
    method, target, version = (lines[0].split(" ") + ["", "", ""])[:3]
    headers = []
    for line in lines[1:]:
        if not line:
            continue
        i = line.find(":")
        if i < 0:
            continue
        headers.append((line[:i].strip(), line[i + 1:].strip()))
    return method, target, version, headers


def get_header(headers, name):
    low = name.lower()
    for k, v in headers:
        if k.lower() == low:
            return v
    return None


def normalize_tool_schemas(data):
    """Remove only provably redundant parent constraints on the CUA union.

    Every branch retains its types, required keys and additionalProperties=false.
    This preserves the set of accepted arguments; no validation is disabled.
    BPS 0.5.31 mishandles the original duplicated parent/oneOf layout.
    """
    changed = 0

    def visit(tools, namespace=""):
        nonlocal changed
        if not isinstance(tools, list):
            return
        for tool in tools:
            if not isinstance(tool, dict):
                continue
            if tool.get("type") == "namespace":
                visit(tool.get("tools"), tool.get("name", ""))
                continue
            if tool.get("type") != "function" or namespace != "mcp__cua_repl" or tool.get("name") != "js":
                continue
            schema = tool.get("parameters")
            if not isinstance(schema, dict) or set(schema) != {"type", "properties", "additionalProperties", "oneOf"}:
                continue
            if schema["type"] != "object" or schema["additionalProperties"] is not False or not isinstance(schema["properties"], dict):
                continue
            branches = schema["oneOf"]
            if not isinstance(branches, list) or not branches:
                continue
            redundant = True
            for branch in branches:
                if not isinstance(branch, dict) or set(branch) != {"type", "properties", "required", "additionalProperties"}:
                    redundant = False
                    break
                props = branch["properties"]
                if branch["type"] != "object" or branch["additionalProperties"] is not False or not isinstance(props, dict):
                    redundant = False
                    break
                if any(k not in schema["properties"] or value != schema["properties"][k] for k, value in props.items()):
                    redundant = False
                    break
            if redundant:
                tool["parameters"] = {"oneOf": branches}
                changed += 1

    visit(data.get("tools"))
    for item in data.get("input", []) if isinstance(data.get("input"), list) else []:
        if isinstance(item, dict) and item.get("type") == "additional_tools":
            visit(item.get("tools"))
    return changed


def sanitize_body(body):
    """返回 (新 body, 删除条数, 原始条数)。不是 responses 请求体就原样返回。"""
    try:
        data = json.loads(body)
    except Exception:
        return body, 0, 0
    if not isinstance(data, dict):
        return body, 0, 0
    normalized = normalize_tool_schemas(data)
    if normalized:
        log("[schema] normalized %d redundant CUA union schema(s)" % normalized)
    if not isinstance(data.get("input"), list):
        return (json.dumps(data, ensure_ascii=False).encode("utf-8") if normalized else body), 0, 0

    items = data["input"]
    call_ids = set()
    for it in items:
        if isinstance(it, dict) and it.get("type") in CALL_TYPES:
            cid = it.get("call_id") or it.get("id")
            if cid:
                call_ids.add(cid)

    kept = []
    dropped = 0
    for it in items:
        if isinstance(it, dict) and it.get("type") in OUT_TYPES:
            cid = it.get("call_id")
            if not cid or cid not in call_ids:
                dropped += 1
                continue
        kept.append(it)

    if not dropped and not normalized:
        return body, 0, len(items)

    data["input"] = kept
    return json.dumps(data, ensure_ascii=False).encode("utf-8"), dropped, len(items)


def relay_response(upstream, conn, initial=None):
    """把上游响应原样转发给客户端，处理 chunked / content-length / HTTP/1.0 EOF。"""
    head, rest = read_head(upstream, conn) if initial is None else initial
    if head is None:
        return b""
    conn.sendall(head + b"\r\n\r\n")
    method, target, version, headers = parse_head(head)
    te = (get_header(headers, "Transfer-Encoding") or "").lower()
    cl = get_header(headers, "Content-Length")

    if "chunked" in te:
        buf = rest
        while True:
            while b"\r\n" not in buf:
                more = recv_upstream(upstream, conn, 65536)
                if not more:
                    conn.sendall(buf)
                    return head
                buf += more
            line, buf = buf.split(b"\r\n", 1)
            try:
                size = int(line.split(b";")[0], 16)
            except ValueError:
                conn.sendall(buf)
                return head
            if size < 0:
                raise ValueError('invalid upstream chunk size')
            conn.sendall(line + b"\r\n")
            if size == 0:
                # Forward trailers through their terminating empty line.
                while True:
                    while b"\r\n" not in buf:
                        more = recv_upstream(upstream, conn, 65536)
                        if not more:
                            raise EOFError('incomplete upstream trailer')
                        buf += more
                        if len(buf) > 65536:
                            raise ValueError('upstream trailer too large')
                    trailer, buf = buf.split(b"\r\n", 1)
                    conn.sendall(trailer + b"\r\n")
                    if not trailer:
                        break
                return head
            # A large HTTP chunk is not a buffering boundary: forward its
            # contents incrementally, keeping at most one recv buffer here.
            remaining = size
            while remaining:
                if not buf:
                    buf = recv_upstream(upstream, conn, min(65536, remaining))
                    if not buf:
                        raise EOFError('incomplete upstream chunk')
                take = min(len(buf), remaining)
                conn.sendall(buf[:take])
                buf = buf[take:]
                remaining -= take
            while len(buf) < 2:
                more = recv_upstream(upstream, conn, 2 - len(buf))
                if not more:
                    raise EOFError('missing upstream chunk delimiter')
                buf += more
            if buf[:2] != b"\r\n":
                raise ValueError('invalid upstream chunk delimiter')
            conn.sendall(b"\r\n")
            buf = buf[2:]
    elif cl is not None:
        remaining = int(cl)
        if rest:
            take = min(len(rest), remaining)
            conn.sendall(rest[:take])
            remaining -= take
        while remaining > 0:
            more = recv_upstream(upstream, conn, min(65536, remaining))
            if not more:
                break
            conn.sendall(more)
            remaining -= len(more)
        if rest and len(rest) > remaining:
            pass
    else:
        if rest:
            conn.sendall(rest)
        while True:
            more = recv_upstream(upstream, conn, 65536)
            if not more:
                break
            conn.sendall(more)
    return head


def tunnel(a, b):
    """One worker per tunnel; close both peers on EOF or bounded inactivity."""
    peers = {a: b, b: a}
    a.settimeout(120)
    b.settimeout(120)
    while True:
        ready = wait_readable([a, b], 120)
        if not ready:
            return
        for src in ready:
            data = src.recv(65536)
            if not data:
                return
            peers[src].sendall(data)


def prepare_forward(conn, method, target, headers, rest, upstream_host, upstream_port, deadline=None):
    """Called under NORMALIZERS: bounded upload, JSON expansion and forwarding."""
    if deadline is None:
        deadline = time.monotonic() + PREPARE_SECONDS
    body = b""
    clen = get_header(headers, "Content-Length")
    if clen is not None:
        need = int(clen)
        if need < 0:
            raise ValueError('negative content length')
        received = bytearray(rest[:need])
        rest = b""
        while len(received) < need:
            more = receive_preparing(conn, min(65536, need - len(received)), deadline)
            if not more:
                raise ClientDisconnected('incomplete downstream request')
            received.extend(more)
        body = bytes(received)
        del received
    elif (get_header(headers, "Transfer-Encoding") or "").lower().startswith("chunked"):
        buf = rest
        received = bytearray()
        while True:
            while b"\r\n" not in buf:
                more = receive_preparing(conn, 65536, deadline)
                if not more:
                    raise ClientDisconnected('incomplete downstream chunk')
                buf += more
            line, buf = buf.split(b"\r\n", 1)
            size = int(line.split(b";", 1)[0], 16)
            if size < 0:
                raise ValueError('invalid downstream chunk size')
            if size == 0:
                break
            while len(buf) < size + 2:
                more = receive_preparing(conn, min(65536, size + 2 - len(buf)), deadline)
                if not more:
                    raise ClientDisconnected('incomplete downstream chunk')
                buf += more
            received.extend(buf[:size])
            if buf[size:size + 2] != b"\r\n":
                raise ValueError('invalid downstream chunk delimiter')
            buf = buf[size + 2:]
        body = bytes(received)
        del received
    path = target.split("?")[0]
    if method == "POST" and path.endswith("/responses") and body:
        body, dropped, total = sanitize_body(body)
        if dropped:
            with LOCK:
                STATS["sanitized"] += 1
                STATS["dropped"] += dropped
            log("[sanitize] %s removed %d / %d orphan outputs" % (path, dropped, total))
    out = ["%s %s HTTP/1.1" % (method, target)]
    for k, v in headers:
        if k.lower() not in ("host", "content-length", "transfer-encoding", "connection", "proxy-connection"):
            out.append("%s: %s" % (k, v))
    out.append("Host: %s:%d" % (upstream_host, upstream_port))
    if body or method in ("POST", "PUT", "PATCH"):
        out.append("Content-Length: %d" % len(body))
    out.append("Connection: close")
    raw = ("\r\n".join(out) + "\r\n\r\n").encode("iso-8859-1")
    try:
        upstream = socket.create_connection((upstream_host, upstream_port), timeout=min(30, remaining(deadline)))
    except socket.timeout as exc:
        raise PreparationTimeout('upstream connection preparation timed out') from exc
    try:
        upstream.settimeout(min(30, remaining(deadline)))
        upstream.sendall(raw)
        if body:
            upstream.settimeout(min(30, remaining(deadline)))
            upstream.sendall(body)
        remaining(deadline)
        upstream.settimeout(300)
        return upstream
    except socket.timeout as exc:
        upstream.close()
        raise PreparationTimeout('upstream request sending timed out') from exc
    except BaseException:
        upstream.close()
        raise


def handle(conn, addr, upstream_host, upstream_port):
    upstream = None
    try:
        conn.settimeout(30)
        CONNECTIONS.phase('headers')
        head, rest = read_head(conn, deadline=time.monotonic() + 30)
        if head is None:
            return
        method, target, version, headers = parse_head(head)

        # WebSocket / 其它 Upgrade：原样建隧道，不改内容
        if get_header(headers, "Upgrade"):
            CONNECTIONS.phase('upgrade')
            out = ["%s %s HTTP/1.1" % (method, target)]
            for k, v in headers:
                if k.lower() == "host":
                    continue
                out.append("%s: %s" % (k, v))
            out.append("Host: %s:%d" % (upstream_host, upstream_port))
            raw = ("\r\n".join(out) + "\r\n\r\n").encode("iso-8859-1") + rest
            upstream = socket.create_connection((upstream_host, upstream_port), timeout=30)
            upstream.settimeout(300)
            upstream.sendall(raw)
            # Only tunnel a successful upgrade. A rejected handshake is a finite
            # HTTP response, not a permanently open bidirectional connection.
            response_head, pending = read_head(upstream, conn)
            if response_head is None:
                return
            status = response_head.split(b" ", 2)[1]
            if status == b"101":
                conn.sendall(response_head + b"\r\n\r\n" + pending)
                log("[tunnel] upgraded %s" % target.split("?")[0])
                CONNECTIONS.phase('websocket')
                tunnel(conn, upstream)
            else:
                relay_response(upstream, conn, (response_head, pending))
                log("[tunnel] rejected status=%s" % status.decode("ascii", "replace"))
            return

        # The permit covers upload and transformation, not model streaming.
        # Waiting clients retain only the bounded header-read buffer.
        has_body = (method in ('POST', 'PUT', 'PATCH')
                    or get_header(headers, 'Content-Length') not in (None, '0')
                    or get_header(headers, 'Transfer-Encoding') is not None)
        if has_body:
            with normalizer_slot(conn):
                upstream = prepare_forward(conn, method, target, headers, rest, upstream_host, upstream_port)
        else:
            # Bodyless models/health traffic does not need JSON processing slots.
            CONNECTIONS.phase('preparing')
            upstream = prepare_forward(conn, method, target, headers, rest, upstream_host, upstream_port)
        rest = b""
        conn.settimeout(300)
        CONNECTIONS.phase('response')
        with LOCK:
            STATS["requests"] += 1
        relay_response(upstream, conn)
        upstream.close()
    except ClientDisconnected:
        log("[cancel] downstream closed; upstream released")
    except NormalizerBusy:
        with LOCK:
            STATS['normalizer_rejections'] += 1
        send_error(conn, 503, 'proxy_normalizer_busy',
                   'Request preparation is busy; no upstream request was sent.',
                   {'wait_seconds': NORMALIZER_WAIT_SECONDS}, retry_after=2)
        log_state('normalizer wait timed out')
    except PreparationTimeout:
        send_error(conn, 504, 'proxy_preparation_timeout',
                   'Request preparation exceeded its time budget; this proxy did not retry the request.')
        log_state('request preparation timed out')
    except Exception as e:
        log("[error] %s -> %s" % (addr, e))
    finally:
        if upstream is not None:
            upstream.close()
        try:
            conn.close()
        except Exception:
            pass
        CONNECTIONS.release()


def start_worker(conn, addr, upstream_host, upstream_port):
    budget = pid_budget()
    if budget is not None and budget[0] >= max(1, budget[1] - PID_RESERVE):
        with LOCK:
            STATS['resource_rejections'] += 1
        details = {'pids_current': budget[0], 'pids_max': budget[1], 'reserved': PID_RESERVE}
        send_error(conn, 503, 'proxy_resource_busy',
                   'Proxy PID budget is nearly exhausted; reserving capacity for recovery.', details, retry_after=2)
        conn.close()
        log_state('PID headroom protected', **details)
        return False
    CONNECTIONS.acquire()
    try:
        threading.Thread(target=handle, args=(conn, addr, upstream_host, upstream_port), daemon=True).start()
        return True
    except RuntimeError:
        CONNECTIONS.release()
        with LOCK:
            STATS['resource_rejections'] += 1
        send_error(conn, 503, 'proxy_worker_unavailable',
                   'Proxy could not allocate a worker; retry shortly.', retry_after=2)
        conn.close()
        log_state('worker creation failed')
        return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen", default="0.0.0.0:8318")
    ap.add_argument("--upstream", default="cpa-bps-core:8317")
    args = ap.parse_args()

    host, port = args.listen.rsplit(":", 1)
    uhost, uport = args.upstream.rsplit(":", 1)
    uport = int(uport)

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((host, int(port)))
    srv.listen(128)
    log("bps-sanitize-proxy %s -> %s; connection_limit=unlimited; normalization_workers=2; normalizer_wait_seconds=%s; prepare_seconds=%s; pid_reserve=%s; resource_guard=v2" % (args.listen, args.upstream, NORMALIZER_WAIT_SECONDS, PREPARE_SECONDS, PID_RESERVE))

    while True:
        conn, addr = srv.accept()
        start_worker(conn, addr, uhost, uport)


if __name__ == "__main__":
    main()

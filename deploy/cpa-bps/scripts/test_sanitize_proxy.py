"""Offline transport regressions; no credentials or upstream account calls."""
import importlib.util
import copy
import itertools
import json
import socket
import threading
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('proxy', Path(__file__).parents[1] / 'assets' / 'bps-sanitize-proxy.py')
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)

class TransportTests(unittest.TestCase):
    def schema_fixture(self):
        props = {'code': {'type':'string'}, 'title': {'type':'string'},
                 'timeout_ms': {'type':'integer'}, 'completion_summary': {'type':'string'}}
        branches = [
            {'type':'object','properties':{k:props[k] for k in ['code','title','timeout_ms']},'required':['code'],'additionalProperties':False},
            {'type':'object','properties':{'completion_summary':props['completion_summary']},'required':['completion_summary'],'additionalProperties':False}]
        schema = {'type':'object','properties':props,'additionalProperties':False,'oneOf':branches}
        return {'input':[{'type':'additional_tools','role':'developer','tools':[{'type':'namespace','name':'mcp__cua_repl','tools':[{'type':'function','name':'js','parameters':schema}]}]}]}

    def test_cua_union_preserves_argument_contract(self):
        data=self.schema_fixture()
        original=copy.deepcopy(data['input'][0]['tools'][0]['tools'][0]['parameters'])
        body,dropped,total=p.sanitize_body(json.dumps(data).encode())
        normalized=json.loads(body)['input'][0]['tools'][0]['tools'][0]['parameters']
        self.assertEqual(normalized, {'oneOf':original['oneOf']})
        self.assertEqual((dropped,total),(0,1))
        def accepts(schema,value):
            kind=schema.get('type')
            if kind=='object' and not isinstance(value,dict):return False
            if kind=='string' and not isinstance(value,str):return False
            if kind=='integer' and (not isinstance(value,int) or isinstance(value,bool)):return False
            if isinstance(value,dict):
                if any(k not in value for k in schema.get('required',[])):return False
                props=schema.get('properties',{})
                if schema.get('additionalProperties') is False and any(k not in props for k in value):return False
                if any(not accepts(props[k],v) for k,v in value.items() if k in props):return False
            if 'oneOf' in schema and sum(accepts(s,value) for s in schema['oneOf'])!=1:return False
            return True
        keys=list(original['properties']); missing=object();cases=[None,[],4,'text',False]
        for values in itertools.product([missing,'ok',1],repeat=len(keys)):
            value={k:v for k,v in zip(keys,values) if v is not missing}
            cases.extend([value,dict(value,unexpected=True)])
        for value in cases:self.assertEqual(accepts(original,value),accepts(normalized,value),repr(value))

    def test_nonredundant_constraints_are_not_removed(self):
        for change in ('required','conflict','different_tool'):
            data=self.schema_fixture();tool=data['input'][0]['tools'][0]['tools'][0]
            if change=='required':tool['parameters']['required']=['code']
            elif change=='conflict':tool['parameters']['properties']['code']={'type':'number'}
            else:tool['name']='unrelated'
            raw=json.dumps(data).encode()
            self.assertEqual(p.sanitize_body(raw)[0],raw)

    def test_post_body_forwarded_once_and_stream_completed(self):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0)); listener.listen()
        observed = []
        payload = json.dumps({'input': [{'role':'user','content':'x' * 180000}]}).encode()
        def fake():
            with listener.accept()[0] as upstream:
                head, body = p.read_head(upstream)
                headers = p.parse_head(head)[3]
                length = int(p.get_header(headers, 'Content-Length'))
                while len(body) < length: body += upstream.recv(65536)
                observed.append((p.get_header(headers, 'Transfer-Encoding'), body))
                upstream.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok')
        server = threading.Thread(target=fake)
        a, client = socket.socketpair(); p.CONNECTIONS.acquire()
        worker = threading.Thread(target=p.handle, args=(a, ('test',0), '127.0.0.1', listener.getsockname()[1]))
        try:
            server.start(); worker.start(); client.settimeout(3)
            client.sendall(('POST /v1/responses HTTP/1.1\r\nContent-Length: %d\r\n\r\n' % len(payload)).encode() + payload)
            response = b''
            while True:
                part = client.recv(4096)
                if not part: break
                response += part
            self.assertTrue(response.endswith(b'ok'))
            self.assertEqual(observed, [(None, payload)])
        finally:
            client.close(); a.close(); listener.close(); server.join(4); worker.join(4)

    def test_tunnel_peer_eof_releases_worker(self):
        for _ in range(80):
            a, ac = socket.socketpair()
            b, bc = socket.socketpair()
            thread = threading.Thread(target=p.tunnel, args=(a, b))
            try:
                thread.start()
                ac.sendall(b'ping')
                bc.settimeout(2)
                self.assertEqual(bc.recv(4), b'ping')
                bc.close()
                thread.join(2)
                self.assertFalse(thread.is_alive())
            finally:
                for s in [a, ac, b, bc]:
                    s.close()
                thread.join(2)

    def test_rejected_websocket_is_finite_response(self):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0))
        listener.listen()
        released = threading.Event()
        def fake():
            with listener.accept()[0] as upstream:
                p.read_head(upstream)
                upstream.sendall(b'HTTP/1.1 400 Bad Request\r\nContent-Length: 3\r\nConnection: keep-alive\r\n\r\nbad')
                released.wait(3)
        server = threading.Thread(target=fake)
        a, client = socket.socketpair()
        p.CONNECTIONS.acquire()
        worker = threading.Thread(target=p.handle, args=(a, ('test', 0), '127.0.0.1', listener.getsockname()[1]))
        try:
            server.start(); worker.start()
            client.sendall(b'GET /v1/responses HTTP/1.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n')
            client.settimeout(2)
            output = b''
            while True:
                chunk = client.recv(4096)
                if not chunk: break
                output += chunk
            self.assertTrue(output.endswith(b'bad'))
            worker.join(2)
            self.assertFalse(worker.is_alive())
        finally:
            released.set(); client.close(); a.close(); listener.close()
            server.join(4); worker.join(4)

    def test_chunked_response_complete(self):
        a, upstream = socket.socketpair()
        client, out = socket.socketpair()
        raw = b'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n'
        thread = threading.Thread(target=p.relay_response, args=(upstream, out))
        try:
            a.sendall(raw); thread.start(); client.settimeout(2)
            buf = b''
            while len(buf) < len(raw): buf += client.recv(4096)
            self.assertEqual(buf, raw)
            thread.join(2); self.assertFalse(thread.is_alive())
        finally:
            for s in [a, upstream, client, out]: s.close()
            thread.join(2)

    def test_valid_tool_pair_preserved_orphan_removed(self):
        data = {'input': [{'type':'function_call','call_id':'c','name':'x','arguments':'{}'},
                          {'type':'function_call_output','call_id':'c','output':'ok'},
                          {'type':'function_call_output','output':'orphan'}]}
        body, dropped, total = p.sanitize_body(json.dumps(data).encode())
        self.assertEqual((dropped, total), (1, 3))
        self.assertEqual(json.loads(body)['input'], data['input'][:2])

if __name__ == '__main__':
    unittest.main()

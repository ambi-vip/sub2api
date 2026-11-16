"""Offline boundary tests. No account, Docker mutation, or network request."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import socket
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location('bootstrap', Path(__file__).with_name('bps.py'))
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)


class BootstrapTests(unittest.TestCase):
    def test_config_credentials_only_in_private_config(self):
        keys = dict(client_api='client-test-secret', cpa_management='admin-test-secret')
        c = b.config(keys, 'http://user:pass@proxy.invalid:80')
        self.assertEqual(c['access']['api-keys'], [keys['client_api']])
        compose = json.dumps(b.compose_config(19317))
        self.assertNotIn('test-secret', compose)
        self.assertNotIn('user:pass', compose)

    def test_all_host_ports_are_loopback_and_unique(self):
        services = b.compose_config(19317)['services']
        ports = [p for svc in services.values() for p in svc['ports']]
        self.assertEqual(set(ports), {'127.0.0.1:19317:8317', '127.0.0.1:19318:18317', '127.0.0.1:19319:8318'})
        self.assertTrue(all('container_name' not in svc for svc in services.values()))
        self.assertEqual(services['gateway']['pids_limit'], 512)

    def test_foreign_directory_not_adopted(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sentinel = root / 'keep.txt'
            sentinel.write_text('existing user file')
            with patch.object(b, 'check_environment'), self.assertRaises(FileNotFoundError):
                b.install(argparse.Namespace(), root)
            self.assertEqual(sentinel.read_text(), 'existing user file')

    def test_marker_mismatch_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            b.save_json(root / 'installation.json', {'format': b.FORMAT, 'root': str(root), 'project': 'someone-else'})
            with self.assertRaisesRegex(RuntimeError, 'identity'):
                b.read_state(root)

    def test_repeat_install_does_not_write_or_start(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            b.save_json(root / 'installation.json', {'format': b.FORMAT, 'root': str(root),
                        'project': b.project_name(root), 'phase': 'ready'})
            before = (root / 'installation.json').read_bytes()
            with patch.object(b, 'check_environment'), patch.object(b, 'health', return_value=[]), patch.object(b, 'run') as run:
                b.install(argparse.Namespace(), root)
                run.assert_not_called()
            self.assertEqual(before, (root / 'installation.json').read_bytes())

    def test_incomplete_install_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            b.save_json(root / 'installation.json', {'format': b.FORMAT, 'root': str(root),
                        'project': b.project_name(root), 'phase': 'failed'})
            with patch.object(b, 'check_environment'), self.assertRaisesRegex(RuntimeError, 'incomplete'):
                b.install(argparse.Namespace(), root)

    def test_zip_path_escape_rejected_before_extraction(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            z = root / 'evil.zip'
            with zipfile.ZipFile(z, 'w') as f:
                f.writestr(b.BUNDLE + '/../../escape', 'no')
            with self.assertRaisesRegex(RuntimeError, 'Unsafe'):
                b.safe_extract(z, root / 'out')
            self.assertFalse((root / 'out').exists())

    def test_manifest_member_tamper_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            members = {'plugin/dist/cpa-bps.so': b'plugin', 'plugin/dist/native-smoke': b'smoke',
                       'panel/apps/web/dist/index.html': b'panel'}
            manifest = {'format': 1, 'platform': 'linux/amd64', 'version': b.VERSION,
                        'files': {k: hashlib.sha256(v).hexdigest() for k, v in members.items()}}
            z = root / 'tampered.zip'
            with zipfile.ZipFile(z, 'w') as f:
                f.writestr(b.BUNDLE + '/BINARY.json', json.dumps(manifest))
                for name, value in members.items():
                    f.writestr(b.BUNDLE + '/' + name, value + b'changed')
            with self.assertRaisesRegex(RuntimeError, 'integrity'):
                b.safe_extract(z, root / 'out')

    def test_occupied_port_rejected(self):
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0))
            s.listen(1)
            port = s.getsockname()[1]
            if port <= 65533:
                with self.assertRaisesRegex(RuntimeError, 'occupied'):
                    b.check_ports(port)

    def test_secrets_are_redacted(self):
        with patch.object(b, 'PRIVATE_VALUES', ['xyz-secret']):
            result = b.redact('xyz-secret http://u:p@example.test:8080/path')
        self.assertNotIn('xyz-secret', result)
        self.assertNotIn('u:p@', result)

    def test_moved_directory_identity_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            b.save_json(root / 'installation.json', {'format': b.FORMAT, 'root': '/old/place', 'project': b.project_name(root)})
            with self.assertRaisesRegex(RuntimeError, 'identity'):
                b.read_state(root)


if __name__ == '__main__':
    unittest.main()

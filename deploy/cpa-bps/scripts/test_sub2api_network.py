"""Sub2API network integration, without Docker or external requests."""
import importlib.util
from pathlib import Path
import unittest
import tempfile
import json
import hashlib
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('bps', Path(__file__).with_name('bps.py'))
bps = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bps)

class NetworkTests(unittest.TestCase):
    def test_unmodified_imports_match_source_manifest(self):
        root = Path(__file__).parents[1]
        source = json.loads((root / 'SOURCE.json').read_text())
        for name, entry in source['files'].items():
            if not entry['modified']:
                self.assertEqual(hashlib.sha256((root / name).read_bytes()).hexdigest(), entry['source_sha256'], name)

    def test_only_gateway_joins_external_network(self):
        config = bps.compose_config(18317, 'sub2api-cpa-bps')
        self.assertEqual(config['networks']['sub2api'], {'external': True, 'name': 'sub2api-cpa-bps'})
        gateway = config['services']['gateway']
        self.assertEqual(gateway['networks'], {'default': {}, 'sub2api': {'aliases': ['cpa-bps-gateway']}})
        for name in ['core', 'manager']:
            self.assertNotIn('networks', config['services'][name])
        for service in config['services'].values():
            self.assertTrue(all(port.startswith('127.0.0.1:') for port in service['ports']))

    def test_original_defaults_and_pins_are_preserved(self):
        self.assertNotIn('networks', bps.compose_config(18317))
        config = bps.config({'client_api': 'test', 'cpa_management': 'management'}, '')
        self.assertEqual(config['routing']['retry']['request-retry'], 0)
        self.assertTrue(config['plugins']['configs']['cpa-bps']['omit_encrypted_history'])
        self.assertFalse(config['plugins']['configs']['cpa-bps']['fast_mode'])
        self.assertEqual(bps.VERSION, '0.5.31')
        self.assertEqual(bps.SHA256, 'c35fe15632fbf6a09437ce490afcdf1877ea7e9f2a66a9dfee087ec55145e45b')
        for service in bps.compose_config(18317)['services'].values():
            self.assertIn('@sha256:', service['image'])
            self.assertEqual(service['platform'], 'linux/amd64')

    def test_stop_keeps_runtime_data(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root / 'runtime').mkdir()
            sentinel = root / 'runtime' / 'account-fixture.json'
            sentinel.write_text('{"fixture":true}')
            state = {'format': bps.FORMAT, 'root': str(root), 'project': bps.project_name(root), 'phase': 'ready'}
            bps.save_json(root / 'installation.json', state)
            with patch('sys.argv', ['bps.py', 'stop', '--root', str(root)]), patch.object(bps, 'compose', return_value='stopped') as compose:
                self.assertEqual(bps.main(), 0)
                self.assertEqual(compose.call_args.args[2], ['stop', '--timeout', '20'])
            self.assertEqual(sentinel.read_text(), '{"fixture":true}')
            self.assertTrue((root / 'installation.json').exists())

    def test_network_is_a_name_not_a_compose_fragment(self):
        for name in ['bad network', '../network', 'a\nexternal: false', '-bad']:
            with self.assertRaises(RuntimeError):
                bps.compose_config(18317, name)

if __name__ == '__main__':
    unittest.main()

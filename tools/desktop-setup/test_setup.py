"""Offline checks: temporary config only; never invoke installers or gateways."""

import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import tomllib
import unittest
import zipfile

SOURCE = Path(__file__).resolve().parent
ROOT = SOURCE.parent.parent
MAC_SCRIPT = SOURCE / 'ambi-setup-macos.command'
PUBLIC = ROOT / 'frontend/public/help'


class DesktopSetupTests(unittest.TestCase):
    def setUp(self):
        self.sandbox = tempfile.TemporaryDirectory(prefix='ambi-setup-test-')
        self.addCleanup(self.sandbox.cleanup)
        self.directory = Path(self.sandbox.name)
        self.config_dir = self.directory / "配置 directory's"
        self.environment = dict(os.environ, CODEX_HOME=str(self.config_dir))

    def bash(self, command, *arguments):
        return subprocess.run(['/bin/bash', '-c', 'source "$1"; ' + command,
                               'test-setup', str(MAC_SCRIPT), *arguments],
                              env=self.environment, capture_output=True, text=True)

    def test_private_config_escapes_literal_values_and_backs_up_previous_file(self):
        self.config_dir.mkdir()
        config = self.config_dir / 'config.toml'
        previous = b'model = "old-model"\n[features]\nold_setting = true\n'
        config.write_bytes(previous)
        sentinel = self.directory / 'should-not-exist'
        key = 'test-key-"\\$(touch ' + str(sentinel) + ')'
        model = 'gpt-test-"\\literal'
        result = self.bash('umask 077; ambi_write_config "$2" "$3"', key, model)
        self.assertEqual(result.returncode, 0, result.stderr)
        parsed = tomllib.loads(config.read_text())
        provider = parsed['model_providers']['ambi_vip']
        self.assertEqual(parsed['model'], model)
        self.assertEqual(provider['experimental_bearer_token'], key)
        self.assertEqual(provider['base_url'], 'https://sub.ambi.vip/v1')
        self.assertEqual(provider['model_catalog_url'], 'https://sub.ambi.vip/v1/models?client_version=0.158.0')
        self.assertFalse(provider['requires_openai_auth'])
        self.assertEqual(provider['wire_api'], 'responses')
        self.assertFalse(sentinel.exists())
        self.assertNotIn(key, result.stdout + result.stderr)
        self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)
        backups = list(self.config_dir.glob('config.toml.ambi-backup.*'))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_bytes(), previous)
        self.assertEqual(stat.S_IMODE(backups[0].stat().st_mode), 0o600)

    def test_symlink_destination_is_rejected_without_touching_its_target(self):
        self.config_dir.mkdir()
        target = self.directory / 'existing.toml'
        target.write_text('keep this')
        (self.config_dir / 'config.toml').symlink_to(target)
        result = self.bash('ambi_write_config "$2" "$3"', 'test-key', 'gpt-test')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(target.read_text(), 'keep this')
        self.assertFalse(list(self.config_dir.glob('config.toml.ambi-backup.*')))

    def test_control_character_cannot_break_out_of_toml_string(self):
        self.config_dir.mkdir()
        config = self.config_dir / 'config.toml'
        config.write_text('model = "preserved"\n')
        result = self.bash('ambi_write_config "$2" "$3"', 'test-key\nmodel = "injected"', 'gpt-test')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(config.read_text(), 'model = "preserved"\n')

    def test_failed_brew_install_is_propagated_from_command_substitution(self):
        result = self.bash('ambi_find_app() { return 1; }; brew() { return 23; }; app=$(ambi_install_app)')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.config_dir.exists())

    def test_zip_preserves_executable_permission_and_exact_script(self):
        with zipfile.ZipFile(PUBLIC / 'downloads/ambi-setup-macos.zip') as archive:
            info = archive.getinfo('Ambi-Setup.command')
            self.assertEqual(info.create_system, 3)
            self.assertEqual(stat.S_IMODE(info.external_attr >> 16), 0o755)
            self.assertEqual(archive.read(info), MAC_SCRIPT.read_bytes())

    def test_offline_payloads_match_publishable_downloads_and_csp_safe_script(self):
        offline = (ROOT / 'docs/chatgpt-desktop-setup.html').read_text()
        data = re.search(r'<script id="setup-installers" type="application/json">(.*?)</script>', offline, re.S)
        payloads = json.loads(data[1])
        for payload in payloads.values():
            self.assertEqual(base64.b64decode(payload['base64']), (PUBLIC / 'downloads' / payload['filename']).read_bytes())
        published = (PUBLIC / 'index.html').read_text()
        self.assertIn('<script src="./setup.js" defer></script>', published)
        self.assertNotRegex(published, r'<script(?:\s*|\s+type="(?:text|application)/javascript")>')
        windows = (PUBLIC / 'downloads/ambi-setup-windows.cmd').read_bytes()
        self.assertNotIn(b'\n', windows.replace(b'\r\n', b''))

    def test_packaging_is_deterministic(self):
        targets = [ROOT / 'docs/chatgpt-desktop-setup.html', PUBLIC / 'index.html',
                   PUBLIC / 'setup.js', *sorted((PUBLIC / 'downloads').iterdir())]
        previous = {path: path.read_bytes() for path in targets}
        spec = importlib.util.spec_from_file_location('ambi_build', SOURCE / 'build.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        module.package()
        self.assertEqual(previous, {path: path.read_bytes() for path in targets})


if __name__ == '__main__':
    unittest.main()

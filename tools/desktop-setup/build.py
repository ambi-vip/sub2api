#!/usr/bin/env python3
"""Package standalone desktop setup and the CSP-compatible public help page."""

import base64
import io
import json
from pathlib import Path
import re
import zipfile

SOURCE = Path(__file__).resolve().parent
ROOT = SOURCE.parent.parent
PAGE = ROOT / 'docs/chatgpt-desktop-setup.html'
PUBLIC = ROOT / 'frontend/public/help'
PAYLOAD_START = '<!-- AMBI_INSTALLERS_START -->'
PAYLOAD_END = '<!-- AMBI_INSTALLERS_END -->'
SCRIPT_START = '<!-- AMBI_PAGE_SCRIPT_START -->'
SCRIPT_END = '<!-- AMBI_PAGE_SCRIPT_END -->'


def replace_block(html, start, end, content):
    pattern = re.escape(start) + r'.*?' + re.escape(end)
    replacement = start + '\n' + content + '\n' + end
    html, count = re.subn(pattern, lambda _: replacement, html, flags=re.DOTALL)
    if count != 1:
        raise ValueError(f'Expected one {start} block, found {count}')
    return html


def package():
    mac_script = (SOURCE / 'ambi-setup-macos.command').read_bytes()
    windows_text = (SOURCE / 'ambi-setup-windows.cmd').read_text(encoding='utf-8')
    windows_script = windows_text.replace('\r\n', '\n').replace('\n', '\r\n').encode('utf-8')
    archive = io.BytesIO()
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as zipped:
        entry = zipfile.ZipInfo('Ambi-Setup.command', date_time=(2026, 11, 13, 0, 0, 0))
        entry.create_system = 3
        entry.external_attr = 0o100755 << 16
        entry.compress_type = zipfile.ZIP_DEFLATED
        zipped.writestr(entry, mac_script)
        entry = zipfile.ZipInfo('使用说明.txt', date_time=(2026, 11, 13, 0, 0, 0))
        entry.create_system = 3
        entry.external_attr = 0o100644 << 16
        entry.compress_type = zipfile.ZIP_DEFLATED
        zipped.writestr(entry, '解压后双击 Ambi-Setup.command，输入 sub.ambi.vip 的 API Key 并选择模型。\n'
                        '系统可能要求确认运行脚本或授权安装。配置会备份并重建 Codex config.toml。\n'
                        '完成后使用 ChatGPT 桌面应用中的 Codex 本地模式；若应用已打开，请完全退出再重开。\n'
                        '恢复原配置：退出应用，将 config.toml.ambi-backup.* 备份复制回 config.toml 后重启。\n')
    downloads = {
        'windows': ('ambi-setup-windows.cmd', 'application/octet-stream', windows_script),
        'macos': ('ambi-setup-macos.zip', 'application/zip', archive.getvalue()),
    }
    payloads = {platform: {'filename': filename, 'type': mime,
                           'base64': base64.b64encode(content).decode('ascii')}
                for platform, (filename, mime, content) in downloads.items()}
    data_tag = '<script id="setup-installers" type="application/json">' + json.dumps(payloads, separators=(',', ':')) + '</script>'
    javascript = (SOURCE / 'page.js').read_text(encoding='utf-8')
    html = PAGE.read_text(encoding='utf-8')
    html = replace_block(html, PAYLOAD_START, PAYLOAD_END, data_tag)
    html = replace_block(html, SCRIPT_START, SCRIPT_END, '<script>\n' + javascript + '\n</script>')
    PAGE.write_text(html, encoding='utf-8')

    # Static files do not receive the backend's CSP nonce. Load executable JS
    # from the same origin instead of weakening the site's security policy.
    public_html = replace_block(html, SCRIPT_START, SCRIPT_END, '<script src="./setup.js" defer></script>')
    PUBLIC.mkdir(parents=True, exist_ok=True)
    (PUBLIC / 'index.html').write_text(public_html, encoding='utf-8')
    (PUBLIC / 'setup.js').write_text(javascript, encoding='utf-8')
    (PUBLIC / 'downloads').mkdir(exist_ok=True)
    for filename, _, content in downloads.values():
        (PUBLIC / 'downloads' / filename).write_bytes(content)
    print('Packaged standalone docs page and frontend/public/help/ (no network requests).')


if __name__ == '__main__':
    package()

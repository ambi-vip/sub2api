#!/usr/bin/env python3
"""Portable, isolated CPA-BPS bootstrap. Python 3.10+, Linux Docker amd64."""
import argparse
import hashlib
import json
import os
import platform
import re
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path, PurePosixPath

SKILL = Path(__file__).resolve().parents[1]
VERSION = '0.5.31'
COMMIT = 'aaee30218e174e5a6c495b935d20d4014d432f49'
BUNDLE = 'cpa-bps-unified-0.5.31-linux-amd64-binary'
URL = 'https://raw.githubusercontent.com/2697106714/cpa-bps/' + COMMIT + '/' + BUNDLE + '.zip'
SHA256 = 'c35fe15632fbf6a09437ce490afcdf1877ea7e9f2a66a9dfee087ec55145e45b'
CORE = 'eceasy/cli-proxy-api@sha256:2e098c0b3c79571f0c3150c8f27725ab2645b6a463cf6c4f862e4e86343466a1'
MANAGER = 'seakee/cpa-manager-plus@sha256:0e803f21d51aceea7b752686417bf57f40678d8068fe102ba23cb2a3afb414e1'
PYTHON = 'python@sha256:a5a7017efc5969e00ae2b91c8eb0631049a569652aa4ffc4501769049ae45c72'
FORMAT = 'bps-skill-install-v1'
PRIVATE_VALUES = []
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def redact(value):
    value = str(value)
    for secret in PRIVATE_VALUES:
        if secret:
            value = value.replace(secret, '[redacted]')
    return re.sub(r'https?://[^ /\s]+:[^ /\s]+@', 'http://[redacted]@', value)


def run(args, timeout=60, data=None):
    result = subprocess.run([str(x) for x in args], input=data, capture_output=True,
                            text=True, encoding='utf-8', errors='replace', timeout=timeout)
    if result.returncode:
        raise RuntimeError('Command failed (' + str(result.returncode) + '): ' +
                           redact(result.stderr[-1200:] or result.stdout[-1200:]))
    return result.stdout


def save_json(path, value):
    tmp = path.with_name(path.name + '.tmp')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w', encoding='utf-8') as f:
        json.dump(value, f, ensure_ascii=False, indent=2)
        f.write('\n')
    os.replace(tmp, path)


def private_root(root):
    root.mkdir(parents=True, exist_ok=False, mode=0o700)
    os.chmod(root, 0o700)
    # NTFS ignores ordinary POSIX chmod without metadata. Set a Windows ACL too.
    if str(root).startswith('/mnt/') and shutil.which('icacls.exe'):
        win = run(['wslpath', '-w', root]).strip()
        who = run(['whoami.exe']).strip()
        run(['icacls.exe', win, '/inheritance:r', '/grant:r', who + ':(OI)(CI)F',
             '*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F'])


def project_name(root):
    return 'bps-skill-' + hashlib.sha256(str(root).encode()).hexdigest()[:10]


def read_state(root):
    if root.is_symlink():
        raise RuntimeError('Installation directory must not be a symlink.')
    path = root / 'installation.json'
    if path.is_symlink():
        raise RuntimeError('Installation marker must not be a symlink.')
    d = json.loads(path.read_text(encoding='utf-8'))
    if d.get('format') != FORMAT or d.get('root') != str(root.resolve()) or d.get('project') != project_name(root.resolve()):
        raise RuntimeError('Installation identity mismatch; refusing to manage this directory.')
    return d


def compose(root, state, args, timeout=180):
    return run(['docker', 'compose', '-p', state['project'], '--project-directory', root,
                '-f', root / 'compose.json'] + args, timeout=timeout)


def check_environment():
    if sys.version_info < (3, 10):
        raise RuntimeError('Python 3.10 or newer is required.')
    if sys.platform != 'linux':
        raise RuntimeError('Use Linux x86-64, or Windows with WSL2 Linux and Docker integration.')
    if not shutil.which('docker'):
        raise RuntimeError('Docker CLI is missing in this Linux environment; install/enable Docker first.')
    info = json.loads(run(['docker', 'info', '--format', '{{json .}}']))
    if info.get('OSType') != 'linux' or info.get('Architecture') not in ('x86_64', 'amd64'):
        raise RuntimeError('This bundle requires a Linux amd64 Docker engine; ARM/Windows engines are not supported.')
    # Local bind mounts must refer to this machine, never a remote Docker daemon.
    context = json.loads(run(['docker', 'context', 'inspect']))[0]
    host = os.environ.get('DOCKER_HOST') or context.get('Endpoints', {}).get('docker', {}).get('Host', '')
    if host and not host.startswith('unix://'):
        raise RuntimeError('Remote Docker contexts are unsupported; select a local engine yourself.')
    version = run(['docker', 'compose', 'version', '--short']).strip()
    if not re.match(r'^v?[25]\.', version):
        raise RuntimeError('Docker Compose v2-compatible plugin is required.')
    return {'engine': 'linux/amd64', 'compose': version, 'context': context.get('Name')}


def check_ports(base):
    if not 1024 <= base <= 65533:
        raise RuntimeError('port-base must be between 1024 and 65533.')
    for port in range(base, base + 3):
        with socket.socket() as s:
            try:
                s.bind(('127.0.0.1', port))
            except OSError:
                raise RuntimeError('Port ' + str(port) + ' is occupied; choose another --port-base.') from None


def safe_extract(archive, destination):
    with zipfile.ZipFile(archive) as z:
        entries = z.infolist()
        if len(entries) > 200 or sum(i.file_size for i in entries) > 100 * 1024 * 1024:
            raise RuntimeError('Unexpected archive size.')
        names = set()
        for i in entries:
            p = PurePosixPath(i.filename)
            if (p.is_absolute() or '..' in p.parts or '\\' in i.filename or ':' in i.filename
                    or not p.parts or p.parts[0] != BUNDLE or i.filename in names
                    or stat.S_ISLNK(i.external_attr >> 16)):
                raise RuntimeError('Unsafe archive entry.')
            names.add(i.filename)
        z.extractall(destination)
    bundle = destination / BUNDLE
    manifest = json.loads((bundle / 'BINARY.json').read_text(encoding='utf-8'))
    if manifest.get('format') != 1 or manifest.get('platform') != 'linux/amd64' or manifest.get('version') != VERSION:
        raise RuntimeError('Unsupported bundle manifest.')
    required = {'plugin/dist/cpa-bps.so', 'plugin/dist/native-smoke', 'panel/apps/web/dist/index.html'}
    if not required.issubset(manifest.get('files', {})):
        raise RuntimeError('Incomplete manifest.')
    for name, digest in manifest['files'].items():
        p = PurePosixPath(name)
        if p.is_absolute() or '..' in p.parts or '\\' in name or ':' in name:
            raise RuntimeError('Unsafe manifest path.')
        f = bundle / name
        if f.is_symlink() or not f.is_file() or hashlib.sha256(f.read_bytes()).hexdigest() != digest:
            raise RuntimeError('Bundle member integrity check failed: ' + name)
    return bundle


def download_bundle(root):
    archive = root / 'download' / (BUNDLE + '.zip')
    archive.parent.mkdir(mode=0o700)
    print('Downloading pinned BPS ' + VERSION + ' (about 6.6 MB)...', flush=True)
    h = hashlib.sha256()
    count = 0
    with urllib.request.urlopen(URL, timeout=45) as response, archive.open('xb') as f:
        while True:
            chunk = response.read(262144)
            if not chunk:
                break
            count += len(chunk)
            if count > 20 * 1024 * 1024:
                raise RuntimeError('Download exceeded expected size.')
            h.update(chunk)
            f.write(chunk)
    if h.hexdigest() != SHA256:
        raise RuntimeError('Downloaded ZIP hash does not match the pinned version; stopped.')
    return safe_extract(archive, root / 'bundle')


def config(keys, proxy):
    return {'config-version': 8, 'server': {'host': '', 'port': 8317},
            'access': {'api-keys': [keys['client_api']]},
            'management': {'allow-remote': True, 'secret-key': keys['cpa_management'], 'disable-control-panel': True},
            'oauth': {'auth-dir': '/root/.cli-proxy-api'},
            'plugins': {'enabled': True, 'dir': '/CLIProxyAPI/plugins', 'configs': {'cpa-bps': {
                'enabled': True, 'auto_discover': True, 'fast_mode': False,
                'omit_encrypted_history': True, 'model_suffix': 'none', 'models': []}}},
            'requests': {'proxy-url': proxy, 'streaming': {'keepalive-seconds': 15, 'send-keepalive-before-ft': True}},
            'routing': {'strategy': 'round-robin', 'retry': {'request-retry': 0}},
            'observability': {'logs': {'debug': False, 'request-log': False, 'logging-to-file': True,
                                     'logs-max-total-size-mb': 50, 'error-logs-max-files': 5},
                              'usage': {'usage-statistics-enabled': True}}}


def compose_config(base, network=None):
    logging = {'driver': 'json-file', 'options': {'max-size': '5m', 'max-file': '2'}}
    common = {'restart': 'unless-stopped', 'platform': 'linux/amd64', 'logging': logging,
              'extra_hosts': ['host.docker.internal:host-gateway']}
    result = {'services': {
        'core': {**common, 'image': CORE, 'ports': ['127.0.0.1:' + str(base) + ':8317'],
                 'environment': {'GODEBUG': 'http2client=0'}, 'mem_limit': '2g', 'memswap_limit': '3g',
                 'pids_limit': 128, 'cpus': 1.0,
                 'volumes': ['./runtime/config.yaml:/CLIProxyAPI/config.yaml',
                             './runtime/auth:/root/.cli-proxy-api', './runtime/logs:/CLIProxyAPI/logs',
                             './runtime/plugins:/CLIProxyAPI/plugins']},
        'gateway': {**common, 'image': PYTHON, 'ports': ['127.0.0.1:' + str(base + 2) + ':8318'],
                    'entrypoint': ['python3', '/app/proxy.py'],
                    'command': ['--listen', '0.0.0.0:8318', '--upstream', 'core:8317'],
                    'volumes': ['./scripts/proxy.py:/app/proxy.py:ro'], 'depends_on': ['core'],
                    'mem_limit': '1g', 'memswap_limit': '2g', 'cpus': 0.5, 'pids_limit': 512,
                    'ulimits': {'nofile': {'soft': 4096, 'hard': 4096}},
                    'environment': {'MALLOC_ARENA_MAX': '2', 'BPS_NORMALIZER_WAIT_SECONDS': '15',
                                    'BPS_PREPARE_SECONDS': '60', 'BPS_PID_RESERVE': '16'}},
        'manager': {**common, 'image': MANAGER, 'ports': ['127.0.0.1:' + str(base + 1) + ':18317'],
                    'mem_limit': '256m', 'cpus': 0.5, 'pids_limit': 128,
                    'environment': {'HTTP_ADDR': '0.0.0.0:18317', 'USAGE_DB_PATH': '/data/usage.sqlite',
                        'CPA_MANAGER_DATA_KEY_PATH': '/data/data.key',
                        'CPA_MANAGER_ADMIN_KEY_FILE': '/run/local-secrets/manager-admin.key',
                        'PANEL_PATH': '/opt/cpa-bps/management.html', 'USAGE_COLLECTOR_MODE': 'auto',
                        'USAGE_CORS_ORIGINS': 'http://127.0.0.1:' + str(base + 1)},
                    'volumes': ['./runtime/manager:/data',
                        './secrets/manager-admin.key:/run/local-secrets/manager-admin.key:ro',
                        './runtime/panel.html:/opt/cpa-bps/management.html:ro']}}}

    if network:
        if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}', network):
            raise RuntimeError('Invalid Docker network name.')
        result['networks'] = {'sub2api': {'external': True, 'name': network}}
        result['services']['gateway']['networks'] = {
            'default': {}, 'sub2api': {'aliases': ['cpa-bps-gateway']}}
    return result


def http_json(url, key, payload=None, timeout=15):
    headers = {'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'}
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, headers=headers)
    try:
        with OPENER.open(req, timeout=timeout) as r:
            raw = r.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024:
                raise RuntimeError('Unexpected response size.')
            return r.status, json.loads(raw), dict(r.headers)
    except urllib.error.HTTPError as e:
        try:
            body = json.loads(e.read(8192))
            error = body.get('error', {})
            code = error.get('code', 'unknown') if isinstance(error, dict) else 'unknown'
        except (ValueError, AttributeError):
            code = 'non_json_error'
        raise RuntimeError('HTTP ' + str(e.code) + ', error=' + str(code)) from None


def credentials(root):
    d = json.loads((root / 'secrets/keys.json').read_text(encoding='utf-8'))
    PRIVATE_VALUES.extend(d.values())
    return d


def health(root, state):
    keys = credentials(root)
    base = state['port_base']
    out = []
    for port in [base, base + 2]:
        status, d, _ = http_json('http://127.0.0.1:' + str(port) + '/v1/models', keys['client_api'])
        if not isinstance(d.get('data'), list):
            raise RuntimeError('Invalid models response.')
        out.append({'port': port, 'http': status, 'models': len(d['data'])})
    status, d, _ = http_json('http://127.0.0.1:' + str(base) + '/v0/management/auth-files', keys['cpa_management'])
    out.append({'auth_files_http': status, 'accounts': len(d.get('files', []))})
    _, d, _ = http_json('http://127.0.0.1:' + str(base) + '/v0/management/plugins', keys['cpa_management'])
    plugin = next((p for p in d.get('plugins', []) if p.get('id') == 'cpa-bps'), {})
    if not plugin.get('registered') or not plugin.get('effective_enabled'):
        raise RuntimeError('BPS plugin is not registered and enabled.')
    out.append({'bps_plugin': 'registered_and_enabled'})
    with OPENER.open('http://127.0.0.1:' + str(base + 1) + '/health', timeout=10) as r:
        if r.status != 200:
            raise RuntimeError('Manager health failed.')
        out.append({'manager_health': r.status})
    with OPENER.open('http://127.0.0.1:' + str(base + 1) + '/management.html', timeout=10) as r:
        page = r.read(8 * 1024 * 1024)
    expected = (root / 'runtime/panel.html').read_bytes()
    if hashlib.sha256(page).digest() != hashlib.sha256(expected).digest():
        raise RuntimeError('Manager did not serve the bundled BPS panel.')
    out.append({'bps_panel': 'matched'})
    return out


def install(args, root):
    check_environment()
    if root.exists():
        state = read_state(root)  # A nonempty foreign directory is never adopted.
        if state.get('phase') != 'ready':
            raise RuntimeError('Previous installation is incomplete. Preserve the directory; inspect installation.json and use stop before recovery.')
        print(json.dumps(health(root, state), ensure_ascii=False))
        print('Already installed; no configuration or account was overwritten.')
        return
    check_ports(args.port_base)
    existing = run(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=' + project_name(root)])
    if existing.strip():
        raise RuntimeError('Compose project already exists without this installation marker; refusing adoption.')
    ancestor = root.parent
    while not ancestor.exists():
        ancestor = ancestor.parent
    if shutil.disk_usage(ancestor).free < 1024 ** 3:
        raise RuntimeError('At least 1 GiB free space is required for the download and image installation.')
    proxy = ''
    if args.proxy_file:
        proxy = json.loads(Path(args.proxy_file).read_text(encoding='utf-8')).get('url', '')
        parsed = urllib.parse.urlsplit(proxy)
        if parsed.scheme not in ('http', 'https', 'socks5', 'socks5h') or not parsed.hostname:
            raise RuntimeError('Proxy JSON must contain a valid url field.')
        PRIVATE_VALUES.append(proxy)
    private_root(root)
    state = {'format': FORMAT, 'root': str(root), 'project': project_name(root), 'version': VERSION,
             'port_base': args.port_base, 'network': getattr(args, 'network', None), 'phase': 'preparing', 'created_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
    save_json(root / 'installation.json', state)
    launched = False
    try:
        for directory in ['secrets', 'runtime/auth', 'runtime/logs', 'runtime/plugins', 'runtime/manager', 'scripts']:
            (root / directory).mkdir(parents=True, mode=0o700)
        keys = {k: secrets.token_urlsafe(36) for k in ['client_api', 'manager_admin', 'cpa_management']}
        PRIVATE_VALUES.extend(keys.values())
        save_json(root / 'secrets/keys.json', keys)
        (root / 'secrets/manager-admin.key').write_text(keys['manager_admin'], encoding='utf-8')
        os.chmod(root / 'secrets/manager-admin.key', 0o600)
        save_json(root / 'runtime/config.yaml', config(keys, proxy))  # JSON is a YAML subset.
        save_json(root / 'compose.json', compose_config(args.port_base, getattr(args, 'network', None)))
        shutil.copyfile(SKILL / 'assets/bps-sanitize-proxy.py', root / 'scripts/proxy.py')
        bundle = download_bundle(root)
        shutil.copyfile(bundle / 'plugin/dist/cpa-bps.so', root / 'runtime/plugins/cpa-bps.so')
        shutil.copyfile(bundle / 'panel/apps/web/dist/index.html', root / 'runtime/panel.html')
        if Path('/etc/localtime').is_file():
            shutil.copyfile('/etc/localtime', root / 'runtime/plugins/cpa-bps-localtime')
        compose(root, state, ['config', '--quiet'])
        print('Pulling pinned Docker images; existing images will be reused...', flush=True)
        compose(root, state, ['pull'], timeout=900)
        dist = bundle / 'plugin/dist'
        os.chmod(dist, 0o755)
        os.chmod(dist / 'native-smoke', 0o755)
        os.chmod(dist / 'cpa-bps.so', 0o644)
        nonce = secrets.token_hex(8)
        smoke = state['project'] + '-preflight-' + nonce
        ownership = 'io.bps-skill.preflight=' + nonce
        try:
            run(['docker', 'run', '--rm', '--name', smoke, '--label', ownership, '--network', 'none', '--read-only',
                 '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--user', '65534:65534',
                 '--memory', '256m', '--pids-limit', '64', '--mount',
                 'type=bind,src=' + str(dist) + ',dst=/check,readonly', '--entrypoint', '/check/native-smoke',
                 CORE, '/check/cpa-bps.so'], timeout=60)
        finally:
            # Require the private per-run label as well as the exact name.
            filters = ['--filter', 'label=' + ownership, '--filter', 'name=^/' + smoke + '$']
            leftover = run(['docker', 'ps', '-aq'] + filters).strip()
            if leftover:
                run(['docker', 'stop', '--time', '3', leftover])
                leftover = run(['docker', 'ps', '-aq'] + filters).strip()
                if leftover:
                    run(['docker', 'rm', leftover])
        state['phase'] = 'starting'
        save_json(root / 'installation.json', state)
        launched = True
        compose(root, state, ['up', '-d', '--no-build', '--pull', 'never'])
        last = None
        for _ in range(20):
            try:
                checks = health(root, state)
                break
            except (RuntimeError, OSError, ValueError) as e:
                last = e
                time.sleep(2)
        else:
            raise RuntimeError('Service readiness failed: ' + redact(last))
        state['phase'] = 'ready'
        save_json(root / 'installation.json', state)
        print(json.dumps({'checks': checks, 'panel': 'http://127.0.0.1:' + str(args.port_base + 1) + '/management.html',
                          'base_url': 'http://127.0.0.1:' + str(args.port_base + 2) + '/v1',
                          'keys_file': str(root / 'secrets/keys.json'), 'account_authorization': 'required'}, ensure_ascii=False, indent=2))
        print('Services installed. Complete your own BPS authorization in the panel; no account was supplied.')
    except BaseException as e:
        state['phase'] = 'failed'
        state['failure'] = redact(type(e).__name__ + ': ' + str(e))[:1000]
        if launched:
            try:
                compose(root, state, ['stop', '--timeout', '20'])
                state['failed_services_stopped'] = True
            except Exception:
                state['failed_services_stopped'] = False
        save_json(root / 'installation.json', state)
        raise


def forward_windows(args):
    if not shutil.which('wsl.exe'):
        raise RuntimeError('WSL2 is required. Install a Linux distribution and enable Docker integration first.')
    prefix = ['wsl.exe'] + (['-d', args.distro] if args.distro else []) + ['-u', 'root', '--exec']
    def mapped(p):
        return run(prefix + ['wslpath', '-a', Path(p).resolve().as_posix()]).strip()
    command = prefix + ['python3', mapped(Path(__file__)), args.action, '--root', mapped(args.root)]
    if args.action == 'install' and not Path(args.root).exists():
        check_ports(args.port_base)  # Check host-side listeners as well as Linux listeners.
    if args.action == 'install':
        command += ['--port-base', str(args.port_base)]
    if args.network:
        command += ['--network', args.network]
    if args.proxy_file:
        command += ['--proxy-file', mapped(args.proxy_file)]
    if args.model:
        command += ['--model', args.model]
    return subprocess.call(command)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['doctor', 'install', 'start', 'stop', 'status', 'models', 'verify'])
    parser.add_argument('--root', default=str(Path.home() / 'cpa-bps-skill'))
    parser.add_argument('--network', help='Existing Docker network shared with Sub2API (install only)')
    parser.add_argument('--distro', help='Windows only: WSL distribution (default: system default)')
    parser.add_argument('--port-base', type=int, default=18317)
    parser.add_argument('--proxy-file', help='Private JSON file containing url, never a command-line credential')
    parser.add_argument('--model', help='Exact model returned by models; required for verify')
    args = parser.parse_args()
    if os.name == 'nt':
        return forward_windows(args)
    candidate = Path(args.root).expanduser()
    if candidate.is_symlink():
        raise RuntimeError('Symlink installation directories are unsupported.')
    root = candidate.resolve()
    if args.action == 'doctor':
        print(json.dumps(check_environment(), ensure_ascii=False))
        return 0
    if args.action == 'install':
        install(args, root)
        return 0
    state = read_state(root)
    if args.action in ('start', 'stop'):
        if args.action == 'start' and state.get('phase') != 'ready':
            raise RuntimeError('Incomplete installation; do not start until its failure has been resolved.')
        print(compose(root, state, ['up', '-d', '--no-build', '--pull', 'never'] if args.action == 'start'
                      else ['stop', '--timeout', '20']))
        if args.action == 'start':
            print(json.dumps(health(root, state), ensure_ascii=False))
        return 0
    if args.action == 'status':
        print(compose(root, state, ['ps']))
        print(json.dumps(health(root, state), ensure_ascii=False))
        return 0
    keys = credentials(root)
    url = 'http://127.0.0.1:' + str(state['port_base'] + 2) + '/v1'
    _, d, _ = http_json(url + '/models', keys['client_api'])
    models = [x['id'] for x in d.get('data', [])]
    if args.action == 'models':
        print(json.dumps(models, ensure_ascii=False, indent=2))
        return 0
    if not args.model or args.model not in models:
        raise RuntimeError('verify requires --model with an exact ID returned by models.')
    code, d, headers = http_json(url + '/responses', keys['client_api'], {
        'model': args.model, 'input': 'Reply with the single word OK.', 'stream': False, 'store': False}, timeout=90)
    output = ''.join(p.get('text', '') for i in d.get('output', []) for p in i.get('content', []) if p.get('type') == 'output_text')
    error = d.get('error') or {}
    trace = next((v for k, v in headers.items() if k.lower() == 'x-cpa-trace-id'), '')
    ok = (d.get('status') == 'completed' and bool(output.strip()) and not error
          and d.get('model') == args.model)
    print(json.dumps({'http': code, 'status': d.get('status'), 'model': d.get('model'),
        'requested_model': args.model, 'model_matches': d.get('model') == args.model,
        'output': output[:200], 'error_code': error.get('code') if isinstance(error, dict) else 'unknown',
        'auth_index': trace.split('-')[1] if len(trace.split('-')) > 1 else None,
        'ok': ok, 'tools_verified': False}, ensure_ascii=False))
    return 0 if ok else 2


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (Exception, KeyboardInterrupt) as exc:
        print('STOP: ' + redact(str(exc) or type(exc).__name__), file=sys.stderr)
        raise SystemExit(1)

#!/usr/bin/env python3
"""Destructive lifecycle checks for two explicitly selected disposable nsl VMs.

Kills the selected VM once to exercise recovery. Writes unique test files and
services, tests conflicting localhost ports, then stops both environments.
"""
import argparse
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import subprocess
import threading
import time
import urllib.request
import uuid

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl', default='build/nsl')
p.add_argument('--environment', required=True)
p.add_argument('--peer', required=True)
p.add_argument('--output', default='build/native/evidence/lifecycle.json')
a = p.parse_args()
assert a.environment != a.peer
assert all(re.fullmatch(r'[a-z][a-z0-9-]{0,23}', x) for x in (a.environment, a.peer))
binary = str(Path(a.nsl).resolve())
state = Path(os.environ.get('NSL_HOME', str(Path.home()/'.local/share/nsl')))
output = Path(a.output)
output.parent.mkdir(parents=True, exist_ok=True)
result = {}
token = 'nsl-probe-' + uuid.uuid4().hex


def cli(*args, check=True):
    r = subprocess.run([binary, *args], capture_output=True, text=True, timeout=160)
    if check and r.returncode:
        raise RuntimeError(f'{args}: {r.returncode}: {r.stderr}')
    return r


def guest(name, *argv, root=False):
    return cli('exec', name, *(['--root'] if root else []), '--', *argv).stdout.strip()


def record(name, **details):
    result[name] = dict({'pass': True}, **details)
    print(name, details, flush=True)
    output.write_text(json.dumps(result, indent=2)+'\n')


def wait_until(predicate, seconds=15):
    deadline = time.monotonic()+seconds
    while time.monotonic() < deadline:
        try:
            if predicate():
                return
        except (OSError, RuntimeError):
            pass
        time.sleep(.2)
    raise RuntimeError('condition timed out')


def response(port):
    with urllib.request.urlopen(f'http://127.0.0.1:{port}/', timeout=1) as r:
        return r.read().decode()


server_code = '''from http.server import BaseHTTPRequestHandler,HTTPServer
import sys
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers();self.wfile.write(sys.argv[2].encode())
 def log_message(self,*args):pass
HTTPServer(("127.0.0.1",int(sys.argv[1])),Handler).serve_forever()
'''
server = None
try:
    metadata = [json.loads((state/'environments'/n/'environment.json').read_text()) for n in (a.environment, a.peer)]
    assert all(m['owner'] == os.getuid() and m['schema'] == 2 for m in metadata)
    assert metadata[0]['id'] != metadata[1]['id']
    keys = [(state/'environments'/n/'keys/identity.pub').read_text() for n in (a.environment, a.peer)]
    assert keys[0] != keys[1]
    boots = [guest(n, 'cat', '/proc/sys/kernel/random/boot_id') for n in (a.environment, a.peer)]
    assert boots[0] != boots[1]
    sizes = [int(guest(n, 'python3', '-c', 'import os;s=os.statvfs("/");print(s.f_blocks*s.f_frsize)')) for n in (a.environment, a.peer)]
    assert all(size > 14*1024**3 for size in sizes)
    record('independent_vms_and_grown_roots', boot_ids=boots, root_bytes=sizes)
    guest(a.environment, 'python3', '-c', 'from pathlib import Path;import sys;Path.home().joinpath(sys.argv[1]).write_text(sys.argv[1])', token)
    guest(a.peer, 'python3', '-c', 'from pathlib import Path;import sys;assert not Path.home().joinpath(sys.argv[1]).exists()', token)
    guest(a.environment, 'sync')  # Crash checks cover committed data, not buffered writes.
    record('separate_homes')

    class HostHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'host')
        def log_message(self, *args):
            pass
    server = ThreadingHTTPServer(('127.0.0.1', 0), HostHandler)
    port = server.server_port
    threading.Thread(target=server.serve_forever, daemon=True).start()
    guest(a.environment, 'systemd-run', '--user', '--unit='+token, '--collect', 'python3', '-c', server_code, str(port), 'main')
    wait_until(lambda: 'conflict' in cli('ports', a.environment).stdout)
    assert response(port) == 'host'
    record('host_port_not_displaced', port=port)
    server.shutdown()
    server.server_close()
    server = None
    wait_until(lambda: response(port) == 'main')
    record('conflict_retry')
    guest(a.peer, 'systemd-run', '--user', '--unit='+token, '--collect', 'python3', '-c', server_code, str(port), 'peer')
    wait_until(lambda: 'conflict' in cli('ports', a.peer).stdout)
    assert response(port) == 'main'
    guest(a.environment, 'systemctl', '--user', 'stop', token)
    wait_until(lambda: response(port) == 'peer')
    record('cross_vm_conflict_and_handoff')

    unit = f'nsl-{os.getuid()}-{metadata[0]["id"]}.service'
    subprocess.run(['systemctl', '--user', 'kill', '--kill-whom=all', '--signal=SIGKILL', unit], check=True)
    wait_until(lambda: 'Running' not in cli('list').stdout.split(a.environment+'\t', 1)[-1].split('\n')[0])
    cli('recover', a.environment)
    assert guest(a.environment, 'cat', '/home/nsl/'+token) == token
    after = guest(a.environment, 'cat', '/proc/sys/kernel/random/boot_id')
    assert after != boots[0]
    assert guest(a.peer, 'cat', '/proc/sys/kernel/random/boot_id') == boots[1]
    assert response(port) == 'peer'
    record('forced_exit_recovery_preserves_data_and_peer', recovered_boot=after)
    ports_unit = unit.removesuffix('.service')+'-ports.service'
    subprocess.run(['systemctl', '--user', 'stop', ports_unit], check=True)
    guest(a.environment, 'true')
    assert subprocess.run(['systemctl', '--user', 'is-active', '--quiet', ports_unit]).returncode == 0
    record('missing_forwarder_restarted')
finally:
    if server:
        server.shutdown()
        server.server_close()
    for name in (a.environment, a.peer):
        # Cleanup must not boot an unreachable/broken VM and repeat its timeout.
        request = base64.b64encode(json.dumps(dict(version=1, argv=['rm', '-f', '/home/nsl/'+token])).encode()).decode()
        try:
            subprocess.run(['ssh', '-F', str(state/'environments'/name/'ssh.config'),
                            '-T', 'guest', '/usr/local/libexec/nsl-exec', request],
                           capture_output=True, timeout=5)
        except subprocess.TimeoutExpired:
            pass
        try:
            result['stop_'+name] = dict(exit=cli('stop', name, check=False).returncode)
        except subprocess.TimeoutExpired:
            result['stop_'+name] = dict(error='stop timed out')
    output.write_text(json.dumps(result, indent=2)+'\n')

#!/usr/bin/env python3
"""Exercise real nsl VMs; write reproducible observations, not a product forecast.

Requires an existing, disposable environment with --project and --desktop.
Writes only uniquely named probe files and guest user services. Stops the tested VM.
"""
import argparse
import base64
import ctypes
import fcntl
import json
import math
import os
from pathlib import Path
import pty
import select
import signal
import socket
import statistics
import struct
import subprocess
import termios
import time
import urllib.request
import uuid

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl', default='build/nsl')
p.add_argument('--environment', required=True)
p.add_argument('--project', required=True)
p.add_argument('--peer')
p.add_argument('--cold-trials', type=int, default=20)
p.add_argument('--warm-trials', type=int, default=50)
p.add_argument('--output', default='build/poc/evidence/measurement.json')
a = p.parse_args()
nsl = str(Path(a.nsl).resolve())
project = Path(a.project).resolve()
result = {'started': time.strftime('%Y-%m-%dT%H:%M:%S%z'), 'environment': a.environment,
          'host_kernel': os.uname().release, 'checks': {}, 'warm': [], 'cold': []}
output = Path(a.output)
output.parent.mkdir(parents=True, exist_ok=True)
token = 'nsl-probe-' + uuid.uuid4().hex

def save():
    output.write_text(json.dumps(result, indent=2) + '\n')

def record(name, passed, **detail):
    result['checks'][name] = {'pass': bool(passed), **detail}
    print(name, 'PASS' if passed else 'FAIL', detail, flush=True)
    save()

def run(args, data=None, timeout=120):
    start = time.monotonic()
    process = subprocess.Popen([nsl, *args], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, start_new_session=True)
    try:
        out, err = process.communicate(data, timeout=timeout)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        out, err = process.communicate()
        return {'code': -1, 'seconds': time.monotonic()-start, 'out': out.decode(errors='replace'), 'err': 'TIMEOUT ' + err.decode(errors='replace')}
    return {'code': process.returncode, 'seconds': time.monotonic()-start,
            'stdout_base64': base64.b64encode(out).decode(),
            'out': out.decode(errors='replace'), 'err': err.decode(errors='replace')}

def guest(*args, root=False, env=None, data=None, timeout=120):
    return run(['exec', env or a.environment, *(['--root'] if root else []), '--', *args], data, timeout)

def percentile(values, q):
    return sorted(values)[max(0, math.ceil(len(values)*q)-1)]

def summary(trials):
    n = len(trials)
    if not n:
        return {}
    passed = sum(x['pass'] for x in trials)
    fraction = passed / n
    z = 1.96
    center = (fraction+z*z/(2*n))/(1+z*z/n)
    half = z*math.sqrt(fraction*(1-fraction)/n+z*z/(4*n*n))/(1+z*z/n)
    times = [x['seconds'] for x in trials if x['pass']]
    return {'passed': passed, 'trials': n, 'wilson_95_interval': [center-half, center+half],
            'median_seconds': statistics.median(times) if times else None,
            'p95_seconds': percentile(times, .95) if times else None}

try:
    r = guest('python3', '-c', 'import json,os;print(json.dumps(dict(uid=os.getuid(),gid=os.getgid(),kernel=os.uname().release,home=os.environ["HOME"])))')
    record('guest_identity', r['code'] == 0 and json.loads(r['out'])['uid'] == os.getuid(), result=r)
    r = guest('python3', '-c', 'import sys;sys.stdout.buffer.write(sys.stdin.buffer.read());sys.stderr.write("probe-error");sys.exit(37)', data=b'\x00binary\xff\n')
    record('binary_stdio_and_exit', r['code'] == 37 and r['stdout_base64'] == base64.b64encode(b'\x00binary\xff\n').decode() and r['err'] == 'probe-error', result=r)
    special = ['a b', "'quoted'", '$(false);echo bad', 'line\nbreak', '']
    r = guest('python3', '-c', 'import json,sys;print(json.dumps(sys.argv[1:]))', *special)
    record('argument_fidelity', r['code'] == 0 and json.loads(r['out']) == special)
    r = guest('id', '-u', root=True)
    record('explicit_guest_root', r['code'] == 0 and r['out'].strip() == '0')

    hostfile = project / token
    hostfile.write_text('from-host')
    r = guest('python3', '-c', 'import pathlib,sys;p=pathlib.Path(sys.argv[1]);assert p.read_text()=="from-host";p.write_text("from-guest");p.rename(str(p)+" renamed")', '/work/'+token)
    renamed = project / (token+' renamed')
    record('shared_files_and_rename', r['code'] == 0 and renamed.read_text() == 'from-guest' and renamed.stat().st_uid == os.getuid(), result=r)
    renamed.unlink(missing_ok=True)

    # inotify is called directly so the probe does not install guest packages.
    watch_code = '''import ctypes,os,select,sys
libc=ctypes.CDLL(None,use_errno=True)
f=libc.inotify_init1(0)
assert f>=0
assert libc.inotify_add_watch(f,b"/work",0xfff)>=0
print("READY",flush=True)
ready=select.select([f],[],[],5)[0]
print(os.read(f,65536).hex() if ready else "NO_EVENT",flush=True)
'''
    watcher = subprocess.Popen([nsl,'exec',a.environment,'--','python3','-c',watch_code], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    ready = watcher.stdout.readline().decode().strip()
    hostfile.write_text('watch-host-create')
    watched, watcherr = watcher.communicate(timeout=15)
    record('host_to_guest_inotify', ready == 'READY' and b'NO_EVENT' not in watched and bool(watched.strip()), output=watched.decode(), stderr=watcherr.decode())
    hostfile.unlink(missing_ok=True)

    # Allocate an unused local port; fail the check if a competing bind wins the race.
    with socket.socket() as probe:
        probe.bind(('127.0.0.1',0))
        port = probe.getsockname()[1]
    hostfile.write_text(token)
    unit = token+'-http'
    r = guest('systemd-run', '--user', '--unit='+unit, 'python3', '-m', 'http.server', str(port), '--bind', '127.0.0.1', '--directory', '/work')
    started = time.monotonic()
    forwarded = False
    while time.monotonic()-started < 15:
        try:
            with urllib.request.urlopen('http://127.0.0.1:'+str(port)+'/'+token, timeout=1) as response:
                forwarded = response.read().decode() == token
            if forwarded:
                break
        except OSError:
            time.sleep(.2)
    record('automatic_localhost_tcp', r['code'] == 0 and forwarded, discovery_seconds=time.monotonic()-started, port=port)
    guest('systemctl', '--user', 'stop', unit)
    hostfile.unlink(missing_ok=True)

    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH',33,101,0,0))
    process = subprocess.Popen([nsl,'exec',a.environment,'--tty','--','sh','-c','stty size; printf READY; read line; stty size'],stdin=slave,stdout=slave,stderr=slave)
    os.close(slave)
    terminal = b''
    deadline = time.monotonic()+15
    while b'READY' not in terminal and time.monotonic()<deadline:
        if select.select([master],[],[],1)[0]:
            terminal += os.read(master,65536)
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH',41,121,0,0))
    # nsl and SSH share the test process group; target SSH's SIGWINCH explicitly.
    children = Path('/proc/'+str(process.pid)+'/task/'+str(process.pid)+'/children').read_text().split()
    for child in children:
        os.kill(int(child), signal.SIGWINCH)
    time.sleep(.3)  # Allow the asynchronous SSH resize request to reach the guest.
    os.write(master,b'\n')
    while time.monotonic()<deadline:
        try:
            if select.select([master],[],[],.2)[0]:
                terminal += os.read(master,65536)
            elif process.poll() is not None:
                break
        except OSError:
            break
    if process.poll() is None:
        process.kill()
    process.wait()
    os.close(master)
    record('pty_size_and_resize', b'33 101' in terminal and b'41 121' in terminal, output=terminal.decode(errors='replace'))

    if os.environ.get('NSL_WAYPIPE') or __import__('shutil').which('waypipe'):
        r = run(['gui',a.environment,'--','env','WAYLAND_DEBUG=1','timeout','3','galculator'],timeout=20)
        Path(output.parent/'gui-protocol.log').write_text(r['err'])
        record('wayland_surface', r['code']==124 and 'wl_surface' in r['err'] and '.commit(' in r['err'] and '.configure(' in r['err'], exit=r['code'], seconds=r['seconds'], evidence='gui-protocol.log')

    r = guest('python3','-c','from pathlib import Path;import sys;Path.home().joinpath(sys.argv[1]).write_text(sys.argv[1])',token)
    record('persistent_file_setup', r['code']==0)
    if a.peer:
        r = guest('python3','-c','from pathlib import Path;import sys;assert not Path.home().joinpath(sys.argv[1]).exists()',token,env=a.peer)
        record('second_vm_separate_home',r['code']==0,result=r)
        r = guest('cat','/proc/sys/kernel/random/boot_id',env=a.peer)
        peer_id = r['out'].strip()
        own = guest('cat','/proc/sys/kernel/random/boot_id')
        record('independent_kernels',r['code']==0 and own['code']==0 and peer_id != own['out'].strip())

    for i in range(a.warm_trials):
        r = guest('true')
        result['warm'].append({'pass':r['code']==0,'seconds':r['seconds'],'error':r['err'] if r['code'] else ''})
    result['warm_summary'] = summary(result['warm'])
    print('warm',result['warm_summary'],flush=True)
    save()

    # After each stop, exec must boot a new kernel and recover persistent home data.
    old_boot = guest('cat','/proc/sys/kernel/random/boot_id')['out'].strip()
    for i in range(a.cold_trials):
        stopped = run(['stop',a.environment])
        r = guest('python3','-c','from pathlib import Path;import sys;assert Path.home().joinpath(sys.argv[1]).read_text()==sys.argv[1];print(Path("/proc/sys/kernel/random/boot_id").read_text().strip())',token,timeout=180)
        boot = r['out'].strip()
        passed = stopped['code']==0 and r['code']==0 and bool(boot) and boot != old_boot
        old_boot = boot
        result['cold'].append({'pass':passed,'seconds':r['seconds'],'stop_seconds':stopped['seconds'],'boot_id':boot,'error':r['err'][-3000:] if not passed else ''})
        result['cold_summary'] = summary(result['cold'])
        print('cold',i+1,'PASS' if passed else 'FAIL',round(r['seconds'],3),flush=True)
        save()
        if not passed:
            break  # Diagnose the first failure instead of repeating a known defect.
    guest('rm','--',str(Path('/home/nsl')/token))
finally:
    result['finished'] = time.strftime('%Y-%m-%dT%H:%M:%S%z')
    result['cleanup'] = run(['stop',a.environment])
    save()
    print('Evidence:',output,flush=True)

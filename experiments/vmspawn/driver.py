#!/usr/bin/env python3
"""Single-environment vmspawn experiment adapter, separate from the nsl CLI.

init IMAGE PROJECT KEY creates owned state without replacing an existing VM.
Other commands match the measurement harness: start/stop/exec/gui probe ...
"""
import argparse
import base64
import fcntl
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import time
import uuid

STATE = Path(os.environ.get('NSL_VMSPAWN_STATE', str(Path.home()/'.local/share/nsl-vmspawn-eval'))).resolve()
UNIT = 'nsl-vmspawn-experiment.service'
FORWARD_UNIT = 'nsl-vmspawn-ports.service'
CID = 19876

def run(args, **kwargs):
    return subprocess.run(args, **kwargs)

def owned():
    st = STATE.lstat()
    if not STATE.is_dir() or st.st_uid != os.getuid() or st.st_mode & 0o022:
        raise RuntimeError('refusing unowned/writable experiment state')
    meta = STATE/'state.json'
    st = meta.lstat()
    if not meta.is_file() or meta.is_symlink() or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise RuntimeError('invalid experiment metadata')
    data = json.loads(meta.read_text())
    if data['owner'] != os.getuid() or data['name'] != 'probe':
        raise RuntimeError('wrong experiment owner')
    return data

def ssh(*args, capture=False):
    return run(['ssh','-F',str(STATE/'ssh.config'),*args], capture_output=capture)

def active():
    return run(['systemctl','--user','is-active','--quiet',UNIT]).returncode == 0

def payload(args, directory=''):
    return base64.b64encode(json.dumps({'version':1,'argv':args,'directory':directory}).encode()).decode()

def start(data):
    if active():
        return
    # Refuse CID collisions before launching instead of connecting to another VM.
    s = socket.socket(socket.AF_VSOCK,socket.SOCK_STREAM)
    s.settimeout(.3)
    try:
        s.connect((CID,22))
    except OSError:
        pass
    else:
        raise RuntimeError('experiment vsock CID already answers SSH')
    finally:
        s.close()
    cmd=['systemd-vmspawn','--user','--no-ask-password','--keep-unit','--register=no',
         '--image='+str(STATE/'disk.qcow2'),'--image-format=qcow2','--machine=nsl-vmspawn-probe',
         '--cpus=2','--ram=2G','--kvm=yes','--vsock=yes','--vsock-cid='+str(CID),
         '--tpm=no','--secure-boot=no','--network-user-mode','--notify-ready=no',
         '--pass-ssh-key=no','--console=read-only','--bind='+data['project']+':/work']
    if os.environ.get('NSL_VMSPAWN_NO_SHARE') == '1':
        cmd = cmd[:-1]
    if os.environ.get('NSL_VMSPAWN_DEBUG') == '1':
        cmd += ['systemd.journald.forward_to_console=yes']
    # vmspawn mounts shares in the initrd; mkosi removes its /work build directory.
    # A writable development root lets the mount unit recreate the mount point.
    cmd += ['rw']
    # A fresh group context picks up the user's already-authorized kvm membership.
    launch=run(['systemd-run','--user','--unit='+UNIT,'--collect','--property=Type=exec',
                '--property=TimeoutStopSec=30','--property=KillMode=mixed',
                'sg','kvm','-c','exec '+shlex.join([sys.executable,str(Path(__file__).with_name('fd-launch.py').resolve()),*cmd])],stdout=sys.stderr)
    launch.check_returncode()
    deadline=time.monotonic()+90
    while time.monotonic()<deadline:
        if not active():
            raise RuntimeError('vmspawn exited; inspect journalctl --user -u '+UNIT)
        r=ssh('probe','/usr/local/libexec/nsl-exec',payload(['true']),capture=True)
        if r.returncode==0:
            run(['systemd-run','--user','--unit='+FORWARD_UNIT,'--collect',
                 sys.executable,str(Path(__file__).resolve()),'_forward'],stdout=sys.stderr,check=True)
            return
        time.sleep(.2)
    raise RuntimeError('guest SSH readiness timed out; state retained')

def stop():
    run(['systemctl','--user','stop',FORWARD_UNIT],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    if active():
        ssh('probe','sudo','-n','systemctl','poweroff',capture=True)
        deadline=time.monotonic()+30
        while active() and time.monotonic()<deadline:
            time.sleep(.1)
        if active():
            run(['systemctl','--user','stop',UNIT],check=True)
    ssh('-O','exit','probe',capture=True)

def forward_loop():
    owned()
    forwarded=set()
    while active():
        r=ssh('probe','/usr/local/libexec/nsl-exec',payload(['ss','-H','-ltn']),capture=True)
        if r.returncode:
            time.sleep(1)
            continue
        ports=set()
        for line in r.stdout.decode().splitlines():
            try:
                port=int(line.split()[3].rsplit(':',1)[1])
            except (IndexError,ValueError):
                continue
            if 1024<=port<=65535 and port not in (5353,5355):
                ports.add(port)
        for port in sorted(ports-forwarded):
            r=ssh('-O','forward','-L',f'127.0.0.1:{port}:127.0.0.1:{port}','probe',capture=True)
            if r.returncode==0:
                forwarded.add(port)
            else:
                print('port',port,r.stderr.decode().strip(),flush=True)
        for port in forwarded-ports:
            ssh('-O','cancel','-L',f'127.0.0.1:{port}:127.0.0.1:{port}','probe',capture=True)
        forwarded.intersection_update(ports)
        time.sleep(1)

def initialize(image, project, key):
    image,project,key=map(lambda p:Path(p).resolve(),(image,project,key))
    if not image.is_file() or not project.is_dir() or not key.is_file():
        raise ValueError('image/project/key must exist')
    if any(c in str(project) for c in ':\n\r'):
        raise ValueError('unsupported project path')
    STATE.mkdir(mode=0o700)  # Deliberately refuses a preexisting directory.
    data={'owner':os.getuid(),'name':'probe','id':uuid.uuid4().hex,'project':str(project),'image':str(image)}
    (STATE/'state.json').write_text(json.dumps(data,indent=2)+'\n')
    (STATE/'state.json').chmod(0o600)
    run(['qemu-img','create','-f','qcow2','-F','raw','-b',str(image),str(STATE/'disk.qcow2')],check=True)
    (STATE/'ssh.config').write_text(f'''Host probe
    Hostname vsock/{CID}
    User nsl
    IdentityFile {json.dumps(str(key))}
    IdentitiesOnly yes
    BatchMode yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile {json.dumps(str(STATE/'known_hosts'))}
    HostKeyAlias nsl-vmspawn-{data['id']}
    ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %h %p
    ProxyUseFdpass yes
    ConnectTimeout 2
    ControlMaster auto
    ControlPath {json.dumps(str(STATE/'ssh.sock'))}
    ControlPersist 60
    ServerAliveInterval 10
    ServerAliveCountMax 3
''')
    (STATE/'ssh.config').chmod(0o600)

def main():
    args=sys.argv[1:]
    if args and args[0]=='init' and len(args)==4:
        initialize(*args[1:]);return 0
    data=owned()
    if args==['_forward']:
        forward_loop();return 0
    if len(args)<2 or args[1]!='probe':
        raise ValueError('expected start/stop/exec/gui probe')
    with (STATE/'lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        if args[0]=='stop':
            stop();return 0
        start(data)
    if args[0]=='start':return 0
    if args[0] not in ('exec','gui'):raise ValueError('unknown operation')
    parser=argparse.ArgumentParser()
    parser.add_argument('--root',action='store_true')
    parser.add_argument('--tty',action='store_true')
    parser.add_argument('--workdir',default='')
    parser.add_argument('command',nargs=argparse.REMAINDER)
    options=parser.parse_args(args[2:])
    command=options.command
    if command and command[0]=='--':command=command[1:]
    if not command:raise ValueError('expected command')
    remote=['/usr/local/libexec/nsl-exec',payload(command,options.workdir)]
    if options.root:remote=['sudo','-n','--',*remote]
    sshargs=['ssh','-F',str(STATE/'ssh.config'),'-tt' if options.tty else '-T','probe',*remote]
    if args[0]=='gui':
        sshargs=[os.environ.get('NSL_WAYPIPE','waypipe'),'--no-gpu','--title-prefix=nsl vmspawn: ',*sshargs]
    return run(sshargs).returncode

if __name__=='__main__':
    try:
        sys.exit(main())
    except (OSError,ValueError,RuntimeError,subprocess.CalledProcessError) as error:
        print('vmspawn experiment:',error,file=sys.stderr)
        sys.exit(1)

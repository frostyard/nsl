#!/usr/bin/env python3
"""Compare QEMU PSS across three identical 512 MiB guest allocations.

Uses only the existing experiment VMs, sequentially, and stops them afterwards.
"""
import json
import os
from pathlib import Path
import subprocess
import time

root=Path(__file__).resolve().parents[2]
env=dict(os.environ,NSL_HOME=str(Path.home()/'.local/share/nsl-eval'),NSL_LIMACTL=str(root/'build/poc/tools/bin/limactl'))
backends=[('lima',[os.environ.get('NSL_LIMA_BASELINE',str(root/'build/nsl-lima'))],'final'),('vmspawn',[str(root/'experiments/vmspawn/driver.py')],'probe')]
result={}

def command(prefix,args):
    return subprocess.run(prefix+args,env=env,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE).stdout.decode()

def qemu_pid(kind):
    if kind=='lima':
        return int((Path(env['NSL_HOME'])/'lima/nsl-final/qemu.pid').read_text())
    for p in Path('/proc').iterdir():
        if not p.name.isdigit():continue
        try:
            args=(p/'cmdline').read_bytes().split(b'\0')
            if args and b'qemu-system' in args[0] and any(b'nsl-vmspawn-probe' in x for x in args):return int(p.name)
        except OSError:pass
    raise RuntimeError('QEMU process missing')

def sample(pid):
    values={}
    for line in Path('/proc',str(pid),'smaps_rollup').read_text().splitlines():
        if line.startswith(('Rss:','Pss:')):
            key,value=line.split(':')
            values[key+'_KiB']=int(value.split()[0])
    return values

for kind,prefix,name in backends:
    try:
        command(prefix,['start',name])
        pid=qemu_pid(kind)
        time.sleep(12)
        result[kind]={'pid':pid,'settle_seconds':12,'cycles':[]}
        for cycle in range(3):
            before=sample(pid)
            command(prefix,['exec',name,'--','python3','-c','a=bytearray(512*1024*1024);print(len(a))'])
            after=sample(pid)
            time.sleep(10)
            later=sample(pid)
            values={'before':before,'after_allocate_and_exit':after,'after_10_seconds':later}
            result[kind]['cycles'].append(values)
            print(kind,cycle+1,values,flush=True)
        guest=command(prefix,['exec',name,'--','cat','/proc/meminfo'])
        result[kind]['guest_meminfo']=guest
    finally:
        subprocess.run(prefix+['stop',name],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        (root/'build/vmspawn/evidence/memory-comparison.json').write_text(json.dumps(result,indent=2)+'\n')

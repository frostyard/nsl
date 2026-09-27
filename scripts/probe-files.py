#!/usr/bin/env python3
"""Measure inotify event fidelity and a polling fallback on an explicit VM share."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import uuid

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--environment',required=True)
p.add_argument('--project',required=True)
p.add_argument('--nsl',default='build/nsl')
p.add_argument('--output',default='build/poc/evidence/files.json')
a=p.parse_args()
base=[str(Path(a.nsl).resolve()),'exec',a.environment,'--','python3','-c']
name='nsl-files-'+uuid.uuid4().hex
host=Path(a.project).resolve()/name
host.mkdir()
(host/'nested').mkdir()
(host/'existing').write_text('before')
code='''import ctypes,json,os,select,struct,sys,time
p=sys.argv[1]
libc=ctypes.CDLL(None,use_errno=True)
f=libc.inotify_init1(0)
for sub in [p,p+"/nested",p+"/existing"]:
 assert libc.inotify_add_watch(f,sub.encode(),0xfff)>=0
print("READY",flush=True)
events=[]
end=time.monotonic()+2
while time.monotonic()<end:
 if not select.select([f],[],[],max(0,end-time.monotonic()))[0]:break
 b=os.read(f,65536)
 while b:
  wd,mask,cookie,n=struct.unpack("iIII",b[:16])
  events.append(dict(watch=wd,mask=hex(mask),name=b[16:16+n].split(bytes([0]))[0].decode()))
  b=b[16+n:]
print(json.dumps(events))
'''
result={}
def observe(label,action):
 proc=subprocess.Popen(base+[code,'/work/'+name],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 if proc.stdout.readline().strip()!=b'READY':
  raise RuntimeError('watcher not ready')
 action()
 out,err=proc.communicate(timeout=10)
 result[label]={'events':json.loads(out),'stderr':err.decode(),'exit':proc.returncode}
try:
 observe('create',lambda:(host/'created').write_text('new'))
 observe('modify',lambda:(host/'existing').write_text('after'))
 observe('rename',lambda:(host/'created').rename(host/'renamed'))
 observe('delete',lambda:(host/'renamed').unlink())
 observe('nested_create',lambda:(host/'nested'/'new').write_text('nested'))
 # Poll content/metadata rather than relying on a notification crossing kernels.
 poll='''import pathlib,sys,time
p=pathlib.Path(sys.argv[1]);before=p.read_text();print("READY",flush=True)
start=time.monotonic()
while time.monotonic()-start<5:
 if p.read_text()!=before:print(time.monotonic()-start);sys.exit(0)
 time.sleep(.1)
sys.exit(1)
'''
 proc=subprocess.Popen(base+[poll,'/work/'+name+'/existing'],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 assert proc.stdout.readline().strip()==b'READY'
 (host/'existing').write_text('polled')
 out,err=proc.communicate(timeout=10)
 result['polling']={'pass':proc.returncode==0,'seconds':float(out) if out else None,'stderr':err.decode()}
finally:
 shutil.rmtree(host)
 target=Path(a.output)
 target.parent.mkdir(parents=True,exist_ok=True)
 target.write_text(json.dumps(result,indent=2)+'\n')
print(json.dumps(result,indent=2))

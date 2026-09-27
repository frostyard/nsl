#!/usr/bin/env python3
"""Compile/test nsl in a disposable guest and measure polling-based Go live reload.

Requires Go already installed in the selected guest. Shares only the explicitly
selected project. Test files, guest executables and services are cleaned up.
"""
import argparse
import json
from pathlib import Path
import shutil
import socket
import subprocess
import time
import urllib.request
import uuid

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl',default='build/nsl')
p.add_argument('--environment',required=True)
p.add_argument('--project',required=True)
p.add_argument('--output',default='build/native/evidence/development.json')
a=p.parse_args()
binary=str(Path(a.nsl).resolve())
root=Path(__file__).resolve().parents[1]
token='nsl-development-'+uuid.uuid4().hex
project=Path(a.project).resolve()/token
project.mkdir()
result={}

def guest(*args):
    r=subprocess.run([binary,'exec',a.environment,'--',*args],capture_output=True,text=True,timeout=180)
    if r.returncode:raise RuntimeError(r.stderr+r.stdout)
    return r.stdout.strip()

try:
    source=project/'source'
    source.mkdir()
    (source/'guest').mkdir()
    for f in list(root.glob('*.go'))+[root/'go.mod']:
        shutil.copy2(f,source/f.name)
    shutil.copy2(root/'guest/exec.py',source/'guest/exec.py')
    start=time.monotonic()
    result['go_version']=guest('go','version')
    result['tests']=guest('go','-C','/work/'+token+'/source','test','./...')
    guest('go','-C','/work/'+token+'/source','build','-o','/home/nsl/'+token,'.')
    result['build_and_test_seconds']=time.monotonic()-start
    with socket.socket() as s:
        s.bind(('127.0.0.1',0))
        port=s.getsockname()[1]
    def code(message):
        return 'package main\nimport("fmt";"net/http")\nfunc main(){http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,'+json.dumps(message)+')});panic(http.ListenAndServe("127.0.0.1:'+str(port)+'",nil))}\n'
    server=project/'server.go'
    server.write_text(code('before'))
    watcher='''import hashlib,pathlib,subprocess,sys,time
source=pathlib.Path(sys.argv[1]);binary=sys.argv[2];previous=None;child=None
try:
 while True:
  current=hashlib.sha256(source.read_bytes()).digest()
  if current!=previous:
   subprocess.run(["go","build","-o",binary,str(source)],check=True)
   if child:child.terminate();child.wait()
   child=subprocess.Popen([binary]);previous=current
  time.sleep(.1)
finally:
 if child:child.terminate();child.wait()
'''
    guest('systemd-run','--user','--unit='+token,'--collect','python3','-u','-c',watcher,'/work/'+token+'/server.go','/home/nsl/'+token+'-live')
    def wait(message):
        deadline=time.monotonic()+90
        while time.monotonic()<deadline:
            try:
                with urllib.request.urlopen('http://127.0.0.1:'+str(port),timeout=1) as r:
                    if r.read().decode()==message:return
            except OSError:pass
            time.sleep(.1)
        raise RuntimeError('live server did not return '+message)
    wait('before')
    start=time.monotonic()
    server.write_text(code('after'))
    wait('after')
    result['host_edit_build_and_reload_seconds']=time.monotonic()-start
    result['port']=port
    result['pass']=True
    print(json.dumps(result,indent=2),flush=True)
finally:
    try:guest('systemctl','--user','stop',token)
    finally:
        guest('rm','-f','/home/nsl/'+token,'/home/nsl/'+token+'-live')
        shutil.rmtree(project)
        output=Path(a.output)
        output.parent.mkdir(parents=True,exist_ok=True)
        output.write_text(json.dumps(result,indent=2)+'\n')

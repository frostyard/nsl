#!/usr/bin/env python3
"""Validate offline growth/removal using disposable guests restored from a backup."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--nsl',required=True)
p.add_argument('--archive',required=True)
p.add_argument('--home',required=True)
p.add_argument('--project',required=True)
p.add_argument('--peer-home',required=True)
p.add_argument('--peer',default='dev')
p.add_argument('--output',required=True)
a=p.parse_args()
binary=str(Path(a.nsl).resolve());home=Path(a.home).resolve();project=Path(a.project).resolve()
archive=Path(a.archive).resolve();peer_home=Path(a.peer_home).resolve();output=Path(a.output).resolve()
if home.exists() or project.exists():p.error('test home and project must not exist')
project.mkdir(parents=True);output.parent.mkdir(parents=True,exist_ok=True)
log=output.with_suffix('.log').open('w');result={};success=False

def cli(state,*args,check=True):
 r=subprocess.run([binary,*args],env=dict(os.environ,NSL_HOME=str(state)),capture_output=True,text=True,timeout=180)
 log.write(f'$ {state.name}: {args}\n{r.stdout}{r.stderr}\n');log.flush()
 if check and r.returncode:raise RuntimeError(f'{args}: {r.stderr}')
 return r

def guest(state,name,*args):return cli(state,'exec',name,'--',*args).stdout.strip()
def metadata(name):return json.loads((home/'environments'/name/'environment.json').read_text())
def capacity(name):return int(guest(home,name,'python3','-c','import os; s=os.statvfs("/"); print(s.f_blocks*s.f_frsize)'))
peer_state=next(line.split('\t')[1] for line in cli(peer_home,'list').stdout.splitlines() if line.split('\t')[0]==a.peer)
try:
 peer_boot=guest(peer_home,a.peer,'cat','/proc/sys/kernel/random/boot_id')
 cli(home,'restore','grow',str(archive),'--project',str(project))
 original=metadata('grow');private=home/'environments/grow/keys/identity'
 key_hash=hashlib.sha256(private.read_bytes()).hexdigest()
 guest(home,'grow','python3','-c','from pathlib import Path; import os; Path("/home/nsl/storage-probe").write_text("persistent guest data"); Path("/work/keep").write_text("host project"); os.sync()')
 before=capacity('grow');result['root_bytes_before']=before
 for args in [('resize','grow','--disk','24'),('remove','grow','--yes')]:
  r=cli(home,*args,check=False);assert r.returncode and 'stop' in r.stderr
 assert metadata('grow')['disk_gib']==16
 result['running_mutations_refused']=True
 cli(home,'stop','grow')
 start=time.monotonic();cli(home,'resize','grow','--disk','24');result['resize_seconds']=time.monotonic()-start
 assert metadata('grow')['disk_gib']==24 and not metadata('grow').get('resize_target_gib')
 assert metadata('grow')['id']==original['id'] and hashlib.sha256(private.read_bytes()).hexdigest()==key_hash
 cli(home,'resize','grow','--disk','24')
 assert cli(home,'resize','grow','--disk','8',check=False).returncode
 after=capacity('grow');result['root_bytes_after']=after
 assert after>=before+7*1024**3
 assert guest(home,'grow','cat','/home/nsl/storage-probe')=='persistent guest data'
 result['growth_preserved_identity_and_data']=True
 cli(home,'stop','grow')
 grown_archive=output.parent/'grown-storage.nsl'
 cli(home,'export','grow',str(grown_archive))
 cli(home,'restore','roundtrip',str(grown_archive))
 assert capacity('roundtrip')==after
 assert guest(home,'roundtrip','cat','/home/nsl/storage-probe')=='persistent guest data'
 assert not list((home/'images').iterdir())
 result['grown_backup_restores_without_cache']=True
 cli(home,'stop','roundtrip');cli(home,'remove','roundtrip','--yes')
 cache=home/'images/preserved-test-cache';cache.write_text('cache marker')
 (home/'environments/grow/project-link').symlink_to(project,target_is_directory=True)
 cli(home,'remove','grow');assert (home/'environments/grow/disk.qcow2').exists()
 cli(home,'remove','grow','--yes')
 assert not (home/'environments/grow').exists()
 assert not (home/'removing/grow').exists()
 assert not (Path('/run/user')/str(os.getuid())/'nsl'/original['id']).exists()
 assert (project/'keep').read_text()=='host project' and cache.exists() and grown_archive.exists() and archive.exists()
 result['removal_preserved_project_cache_backups']=True
 cli(home,'restore','grow',str(archive))
 assert metadata('grow')['id']!=original['id']
 guest(home,'grow','true');cli(home,'stop','grow');cli(home,'remove','grow','--yes')
 result['name_reused_with_new_identity']=True
 assert guest(peer_home,a.peer,'cat','/proc/sys/kernel/random/boot_id')==peer_boot
 result['peer_uninterrupted']=True
 result['passed']=True;success=True
finally:
 if not success:
  for name in ['grow','roundtrip']:cli(home,'stop',name,check=False)
 if peer_state!='Running':cli(peer_home,'stop',a.peer,check=False)
 output.write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result,indent=2));log.close()

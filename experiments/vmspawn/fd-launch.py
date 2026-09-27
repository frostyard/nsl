#!/usr/bin/env python3
"""Open VM devices under kvm, then restore the account's primary group.

Uses vmspawn's documented socket-activation interface, without host root or
changing device permissions. The user must already belong to kvm.
"""
import grp
import os
from pathlib import Path
import pwd
import shlex
import sys

account = pwd.getpwuid(os.getuid())
primary = grp.getgrgid(account.pw_gid).gr_name
kvm = os.open('/dev/kvm', os.O_RDWR)
vsock = os.open('/dev/vhost-vsock', os.O_RDWR)
# Opened first, these normally are 3/4. Duplicate above the target range first
# so inherited descriptors cannot make a dup2 accidentally close the other one.
import fcntl
kvm_copy = fcntl.fcntl(kvm, fcntl.F_DUPFD_CLOEXEC, 10)
vsock_copy = fcntl.fcntl(vsock, fcntl.F_DUPFD_CLOEXEC, 10)
os.close(kvm)
os.close(vsock)
os.dup2(kvm_copy, 3, inheritable=True)
os.dup2(vsock_copy, 4, inheritable=True)
os.close(kvm_copy)
os.close(vsock_copy)
os.environ.update(LISTEN_FDS='2', LISTEN_FDNAMES='kvm:vhost-vsock')
if sys.argv[1:] == ['--probe']:
    command = [sys.executable, '-c', 'import os;print("uid/gid",os.getuid(),os.getgid(),"devices",os.fstat(3).st_rdev,os.fstat(4).st_rdev)']
else:
    command = ['unshare','--user','--map-current-user','--keep-caps',*sys.argv[1:]]
# sg invokes a shell; only this fixed script and shlex-quoted argv are evaluated.
script = 'LISTEN_PID=$$; export LISTEN_PID; exec ' + shlex.join(command)
os.execvp('sg', ['sg', primary, '-c', script])

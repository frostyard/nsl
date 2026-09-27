#!/usr/bin/python3
"""Decode an nsl request, then replace this process without a command shell."""
import base64
import json
import os
import pwd
import sys


def main():
    if len(sys.argv) != 2 or len(sys.argv[1]) > 131072:
        raise ValueError('expected one bounded request')
    request = json.loads(base64.b64decode(sys.argv[1], validate=True))
    if request.get('version') != 1:
        raise ValueError('unsupported protocol version')
    args = request.get('argv')
    if not isinstance(args, list) or not args or any(not isinstance(x, str) or '\0' in x for x in args):
        raise ValueError('invalid argument array')
    user = pwd.getpwuid(os.getuid())
    os.environ.update(HOME=user.pw_dir, USER=user.pw_name, LOGNAME=user.pw_name)
    os.environ.setdefault('XDG_RUNTIME_DIR', '/run/user/' + str(os.getuid()))
    # A GUI session inherits Waypipe's display variables from its parent.
    directory = request.get('directory') or user.pw_dir
    if not isinstance(directory, str) or not directory.startswith('/'):
        raise ValueError('directory must be absolute')
    os.chdir(directory)
    os.execvp(args[0], args)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError) as error:
        print('nsl guest:', error, file=sys.stderr)
        sys.exit(127 if isinstance(error, FileNotFoundError) else 1)

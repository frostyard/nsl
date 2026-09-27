#!/usr/bin/env python3
"""Download checksum-pinned publisher tools into ignored build output."""
import hashlib
import io
from pathlib import Path
import tarfile
import urllib.request

TOOLS = {
    'cosign': ('https://github.com/sigstore/cosign/releases/download/v3.1.3/cosign-linux-amd64',
               '4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71', None),
    'oras': ('https://github.com/oras-project/oras/releases/download/v1.3.4/oras_1.3.4_linux_amd64.tar.gz',
             'f27adb935022d94df8dc77719c322dda592c78a0d57a6f7dcdd8d900b248c454', 'oras'),
}


def main():
    base = Path(__file__).resolve().parents[1]/'build/publisher/tools'
    base.mkdir(parents=True, exist_ok=True)
    for name, (url, digest, member) in TOOLS.items():
        with urllib.request.urlopen(url, timeout=120) as response:
            data = response.read(256*1024*1024+1)
        if hashlib.sha256(data).hexdigest() != digest:
            raise ValueError(f'{name}: publisher tool checksum mismatch')
        if member:
            with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
                item = archive.getmember(member)
                if not item.isfile() or item.size > 256*1024*1024:
                    raise ValueError('invalid tool archive')
                data = archive.extractfile(item).read()
        path = base/name
        path.write_bytes(data)
        path.chmod(0o755)
        print(f'{name}: verified pinned release')


if __name__ == '__main__':
    main()

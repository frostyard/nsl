#!/usr/bin/env python3
"""Verify anonymous catalogue delivery and boot every advertised x86-64 image."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import time


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--nsl', type=Path, default=Path('build/nsl'))
    p.add_argument('--home', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    binary, home, output = a.nsl.resolve(), a.home.resolve(), a.output.resolve()
    if home.exists() or output.exists():
        p.error('home and output must be unused')
    output.parent.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, NSL_HOME=str(home))
    results = dict(passed=False, images=[])
    active = None

    def cli(*args):
        start = time.monotonic()
        result = subprocess.run([str(binary), *args], env=env, capture_output=True, text=True, timeout=1800)
        if result.returncode:
            raise RuntimeError(f'{args[:3]}: {result.stderr}')
        return result.stdout, time.monotonic()-start

    def guest(*args):
        return cli('exec', active, '--', *args)[0].strip()

    try:
        listing, results['catalogue_seconds'] = cli('images')
        print(listing, flush=True)
        record = json.loads((home/'delivery/catalogue.json').read_text())
        catalogue = json.loads(base64.b64decode(record['document']))
        entries = [entry for entry in catalogue['images'] if entry['architecture'] == 'x86-64']
        if len(entries) != 7:
            raise ValueError('release acceptance requires all seven image profiles')
        results['catalogue_sequence'] = catalogue['sequence']
        for index, entry in enumerate(entries):
            selector = entry['selectors'][0]
            print(f'Verifying public delivery: {selector}', flush=True)
            active = f'public-{index}'
            item = dict(selector=selector, manifest=entry['manifest'], build_id=entry['build_id'])
            _, item['pull_seconds'] = cli('pull', selector)
            receipt_path = home/'delivery'/entry['manifest'][7:]/'receipt.json'
            receipt = json.loads(receipt_path.read_text())
            descriptor = json.loads(base64.b64decode(receipt['descriptor']))
            item['raw'] = descriptor['raw']
            item['compressed'] = descriptor['compressed']
            _, item['create_seconds'] = cli('create', active, '--distro', selector)
            _, item['first_start_seconds'] = cli('start', active)
            image = json.loads(guest('cat', '/usr/lib/nsl/image.json'))
            if image != descriptor['image']:
                raise ValueError('running guest differs from signed image identity')
            if guest('id', '-u') != str(os.getuid()) or guest('uname', '-m') != 'x86_64':
                raise ValueError('wrong guest user or architecture')
            item['os_release'] = guest('cat', '/etc/os-release')
            boot = guest('cat', '/proc/sys/kernel/random/boot_id')
            guest('python3', '-c', 'from pathlib import Path; (Path.home()/"delivery-check").write_text("persistent")')
            cli('stop', active)
            # Offline verification reuses the independently verified bytes.
            cli('pull', selector, '--offline')
            if guest('cat', '/home/nsl/delivery-check') != 'persistent':
                raise ValueError('guest data did not survive restart')
            if guest('cat', '/proc/sys/kernel/random/boot_id') == boot:
                raise ValueError('guest did not reboot')
            cli('stop', active)
            cli('remove', active, '--yes')
            active = None
            item['passed'] = True
            results['images'].append(item)
            output.write_text(json.dumps(results, indent=2)+'\n')
        results['passed'] = True
    finally:
        if active:
            subprocess.run([str(binary), 'stop', active], env=env, timeout=120, check=False)
        output.write_text(json.dumps(results, indent=2)+'\n')
    print(json.dumps(results, indent=2))


if __name__ == '__main__':
    main()

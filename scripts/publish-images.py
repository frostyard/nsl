#!/usr/bin/env python3
"""Build and accept the nsl VM image and machine images, then sign and publish them with a catalogue in Actions.

  publish-images.py build      make ci, build every image, run the acceptance probes and prepare public artifacts
  publish-images.py publish    sign and push the prepared images, then sign and promote a catalogue of them
  publish-images.py refresh    re-sign the current selections with a higher sequence and a renewed expiry
  publish-images.py withdraw   remove REVOKE_DIGESTS from the catalogue and record them as revoked

See docs/design/image-publication.md and docs/specs/image-delivery.md.
"""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import resource
import shutil
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('compose', ROOT/'scripts/compose-image.py')
compose = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compose)
REPOSITORY = 'ghcr.io/frostyard/nsl-images'
PUBLISHER = 'https://github.com/frostyard/nsl/.github/workflows/images.yml@refs/heads/main'
ISSUER = 'https://token.actions.githubusercontent.com'
# Catalogue selectors for each machine profile; compose-image.py's MACHINES lists the profiles.
SELECTORS = {('debian', 'trixie'): ['debian:trixie', 'debian:13'],
             ('fedora', '44'): ['fedora:44'],
             ('arch', 'rolling'): ['arch:rolling'],
             ('opensuse', 'tumbleweed'): ['opensuse:tumbleweed', 'opensuse-tumbleweed:rolling']}
# Payload files and the client's bounds (docs/specs/image-delivery.md#bounds).
KINDS = {'vm': dict(payload='disk.raw.zst', media='application/zstd', uncompressed='raw', compressed_limit=8 << 30, raw_limit=32 << 30),
         'machine': dict(payload='rootfs.tar.zst', media='application/zstd', uncompressed='rootfs', compressed_limit=4 << 30, raw_limit=16 << 30)}
VM_CHECKS = {'readiness', 'formatting', 'allowlist', 'ownership', 'sockets', 'root_replacement', 'growth', 'refusal', 'binding'}
# Every declared capability is accepted: gui by the gui check, nesting by podman.
MACHINE_CHECKS = {'entry', 'system', 'tally', 'packages', 'podman', 'files', 'ports', 'translation', 'broker', 'ssh', 'gui', 'persistence'}
ISOLATED_CHECKS = {'entry', 'system', 'tally', 'packages', 'podman', 'isolation', 'ssh', 'persistence'}
EVIDENCE_LIMIT = 16 << 20


def run(args, **kwargs):
    return subprocess.run([str(x) for x in args], check=True, **kwargs)


def write(path, value):
    with path.open('x') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')


def reference(path):
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
    return dict(digest='sha256:'+digest, size=path.stat().st_size)


def passed(checks, required, what):
    """Every required check ran and passed, and none was skipped."""
    missing = required - set(checks)
    failed = sorted(name for name in required & set(checks)
                    if checks[name].get('pass') is not True or checks[name].get('skipped'))
    if missing or failed:
        raise ValueError(f'{what} did not pass acceptance: missing {sorted(missing)}, failed {failed}')
    return {name: True for name in sorted(required)}


def vm_acceptance(probe, image, raw, machines):
    """The public report for the VM image: explicit fields only, never host paths or logs."""
    if probe.get('digest') != raw['digest']:
        raise ValueError('the VM probe tested other bytes than the published disk')
    checks = passed(probe.get('results', {}), VM_CHECKS, image['build_id'])
    if machines.get('vm', {}).get('digest') != raw['digest'][7:]:
        raise ValueError('the machine probe ran on another VM image')
    return dict(schema=1, passed=True, build_id=image['build_id'], raw=raw, checks=checks,
                readiness_seconds=probe['results']['readiness'].get('first_boot_seconds'),
                machine_images=sorted(m['build_id'] for m in machines['machines'].values()))


def machine_acceptance(machines, image, compressed, measurements):
    """The public report for a machine image, from its shared and isolated machines."""
    tested = {name: m for name, m in machines.get('machines', {}).items() if m.get('digest') == compressed['digest'][7:]}
    shared = [m for m in tested.values() if not m.get('isolated')]
    isolated = [m for m in tested.values() if m.get('isolated')]
    if len(shared) != 1 or any(m.get('build_id') != image['build_id'] for m in tested.values()):
        raise ValueError(f'{image["build_id"]} was not probed exactly once as a shared machine')
    report = dict(schema=1, passed=True, build_id=image['build_id'], compressed=compressed,
                  vm_image=machines['vm']['descriptor']['build_id'],
                  checks=passed(shared[0]['checks'], MACHINE_CHECKS, image['build_id']),
                  latency_ms=shared[0]['checks']['entry'].get('latency_ms'),
                  capabilities=sorted(image.get('capabilities', {})))
    if isolated:
        report['isolated_checks'] = passed(isolated[0]['checks'], ISOLATED_CHECKS, image['build_id'] + ' (isolated)')
    report['measurements'] = measurements
    return report


def measurements_passed(evidence):
    criteria = evidence.get('criteria', {})
    if not criteria or any(c.get('pass') is not True for c in criteria.values()):
        raise ValueError(f'memory or start-time regression: {criteria}')
    return dict(idle_4_pss_mib=criteria['idle_machines']['pss_mib'], additional_machine_p95_seconds=criteria['additional_machine_p95']['seconds'])


def require_actions():
    if (os.environ.get('GITHUB_REPOSITORY') != 'frostyard/nsl' or os.environ.get('GITHUB_REF') != 'refs/heads/main'
            or os.environ.get('GITHUB_EVENT_NAME') not in ('workflow_dispatch', 'schedule')
            or os.environ.get('GITHUB_WORKFLOW_REF') != 'frostyard/nsl/.github/workflows/images.yml@refs/heads/main'):
        raise ValueError('publication requires the designated main-branch workflow, dispatched or scheduled')
    for key in ('GITHUB_RUN_ID', 'GITHUB_RUN_NUMBER', 'GITHUB_RUN_ATTEMPT'):
        if not re.fullmatch(r'[1-9][0-9]*', os.environ.get(key, '')):
            raise ValueError(f'invalid {key}')
    if os.environ['GITHUB_RUN_ATTEMPT'] != '1':
        raise ValueError('dispatch a new run instead of rerunning a publication sequence')
    if run(['git', 'rev-parse', 'HEAD'], capture_output=True, text=True).stdout.strip() != os.environ['GITHUB_SHA']:
        raise ValueError('checkout does not match workflow source')
    if run(['git', 'status', '--porcelain'], capture_output=True, text=True).stdout:
        raise ValueError('publication requires clean tracked source')


def built_image(role, distribution=None, release=None):
    profile = compose.select(ROOT, role, distribution, release)
    name = compose.output_name(profile)
    payload = ROOT/'build/image/share'/(name + ('.raw' if role == 'vm' else '.tar.zst'))
    return name, payload, json.loads(payload.with_name(name + '.json').read_text())


def prepare(base, kind, name, payload, image, acceptance, provenance, tool):
    """Write an image's public directory: payload, evidence and unsigned descriptor."""
    public = base/name
    public.mkdir()
    limits = KINDS[kind]
    shutil.copyfile(payload.with_name(name + '.manifest'), public/'packages.json')
    if kind == 'vm':
        uncompressed = reference(payload)
        run([tool, 'compress', payload, public/limits['payload']])
    else:
        shutil.copyfile(payload, public/limits['payload'])
        measured = run([tool, 'measure', payload, limits['raw_limit']], capture_output=True, text=True).stdout
        uncompressed = json.loads(measured)
    compressed = reference(public/limits['payload'])
    if uncompressed['size'] > limits['raw_limit'] or compressed['size'] > limits['compressed_limit']:
        raise ValueError(f'{name} exceeds the client format bounds')
    write(public/'acceptance.json', acceptance(uncompressed, compressed))
    write(public/'provenance.json', dict(provenance, image=image, **{limits['uncompressed']: uncompressed}, compressed=compressed))
    descriptor = dict(schema=1, kind=kind, image=image, **{limits['uncompressed']: uncompressed}, compressed=compressed,
                      packages=reference(public/'packages.json'), provenance=reference(public/'provenance.json'),
                      acceptance=reference(public/'acceptance.json'))
    if any(descriptor[key]['size'] > EVIDENCE_LIMIT for key in ('packages', 'provenance', 'acceptance')):
        raise ValueError('evidence exceeds client bounds')
    write(public/'descriptor.json', descriptor)
    return public


def build(base):
    base.mkdir(parents=True)  # retries use a new run; never overwrite prior evidence
    if not os.environ.get('WAYLAND_DISPLAY'):
        raise ValueError('the gui capability needs a Wayland compositor on the runner; set WAYLAND_DISPLAY')
    run(['make', 'ci'])
    run(['make', 'build'])
    tool = base/'zstd-image'
    run(['go', 'build', '-o', tool, 'scripts/zstd-image.go'])
    started = time.monotonic()
    print('::group::Build the images', flush=True)
    run(['scripts/build-image.sh', '--role', 'vm'])
    for distribution, release in compose.MACHINES:
        run(['scripts/build-image.sh', '--role', 'machine', '--distribution', distribution, '--release', release])
    print('::endgroup::', flush=True)
    vm_name, vm_payload, vm_image = built_image('vm')
    machine_images = [(key, *built_image('machine', *key)) for key in compose.MACHINES]
    private = base/'private-evidence'
    private.mkdir()
    print('::group::Accept the images', flush=True)
    run(['python3', 'scripts/probe-vm.py', '--nsl', 'build/nsl', '--image', vm_payload, '--evidence', private/'vm.json'])
    images = [arg for _, _, payload, _ in machine_images for arg in ('--machine-image', payload)]
    run(['python3', 'scripts/probe-machines.py', '--nsl', 'build/nsl', '--vm-image', vm_payload, *images,
         '--isolated', machine_images[0][2], '--gui', '--evidence', private/'machines.json'])
    run(['python3', 'scripts/measure-machines.py', '--nsl', 'build/nsl', '--vm-image', vm_payload, *images,
         '--evidence', private/'measurements.json'])
    print('::endgroup::', flush=True)
    probe = json.loads((private/'vm.json').read_text())
    machines = json.loads((private/'machines.json').read_text())
    measurements = measurements_passed(json.loads((private/'measurements.json').read_text()))
    host = {tool_name: run(args, capture_output=True, text=True).stdout.splitlines()[0]
            for tool_name, args in [('systemd', ['systemctl', '--version']), ('qemu', ['qemu-system-x86_64', '--version']),
                                    ('virtiofsd', ['/usr/libexec/virtiofsd', '--version'])]}
    provenance = dict(schema=1, source='https://github.com/frostyard/nsl', revision=os.environ['GITHUB_SHA'], workflow=PUBLISHER,
                      run_id=os.environ['GITHUB_RUN_ID'], run_attempt=os.environ['GITHUB_RUN_ATTEMPT'], host=host,
                      build_and_acceptance_seconds=round(time.monotonic() - started))
    public = base/'public'
    public.mkdir()
    built = [dict(name=vm_name, kind='vm', architecture=vm_image['architecture'], build_id=vm_image['build_id'],
                  agent_protocol=vm_image['agent_protocol'])]
    prepare(public, 'vm', vm_name, vm_payload, vm_image, lambda raw, _: vm_acceptance(probe, vm_image, raw, machines), provenance, tool)
    for key, name, payload, image in machine_images:
        prepare(public, 'machine', name, payload, image,
                lambda _, compressed, image=image: machine_acceptance(machines, image, compressed, measurements), provenance, tool)
        built.append(dict(name=name, kind='machine', selectors=SELECTORS[key], architecture=image['architecture'],
                          build_id=image['build_id'], machine_protocol=image['machine_protocol']))
    write(base/'built.json', built)


def sign(tools, directory, stem):
    run([tools/'cosign', 'sign-blob', '--yes', '--oidc-provider', 'github-actions',
         '--bundle', directory/f'{stem}.sigstore.json', directory/f'{stem}.json'])
    verify_signature(tools, directory, stem)


def verify_signature(tools, directory, stem):
    run([tools/'cosign', 'verify-blob', '--bundle', directory/f'{stem}.sigstore.json',
         '--certificate-identity', PUBLISHER, '--certificate-oidc-issuer', ISSUER,
         '--trusted-root', ROOT/'trust/sigstore-root.json', directory/f'{stem}.json'])


def push(tools, auth, directory, kind, tag, files):
    manifest = directory/'manifest.json'
    run([tools/'oras', 'push', '--registry-config', auth, '--artifact-type', f'application/vnd.frostyard.nsl.{kind}.v1',
         '--annotation', 'org.opencontainers.image.source=https://github.com/frostyard/nsl',
         '--annotation', 'org.opencontainers.image.revision='+os.environ['GITHUB_SHA'],
         '--export-manifest', manifest, f'{REPOSITORY}:{tag}', *files], cwd=directory)
    return reference(manifest)['digest']


def bounded_fetch(tools, auth, destination, reference, blob=False):
    def limit_files():
        resource.setrlimit(resource.RLIMIT_FSIZE, (1 << 20, 1 << 20))
    command = [tools/'oras', 'blob' if blob else 'manifest', 'fetch', '--registry-config', auth,
               '--output', destination, reference]
    return subprocess.run([str(x) for x in command], capture_output=True, text=True,
                          preexec_fn=limit_files, timeout=60)


def previous_catalogue(tools, auth, base, allow_missing=False):
    directory = base/'previous'
    directory.mkdir()
    manifest = directory/'manifest.json'
    result = bounded_fetch(tools, auth, manifest, REPOSITORY+':catalogue-v1')
    if result.returncode:
        if allow_missing and result.stderr.strip().endswith(REPOSITORY+':catalogue-v1: not found'):
            return None
        raise RuntimeError('cannot fetch previous catalogue: '+result.stderr)
    m = json.loads(manifest.read_text())
    if m.get('schemaVersion') != 2 or m.get('artifactType') != 'application/vnd.frostyard.nsl.catalogue.v1':
        raise ValueError('invalid previous catalogue manifest')
    expected = {'catalogue.json', 'catalogue.sigstore.json'}
    layers = m.get('layers', [])
    if len(layers) != 2 or {x.get('annotations', {}).get('org.opencontainers.image.title') for x in layers} != expected:
        raise ValueError('invalid previous catalogue layers')
    for layer in layers:
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', layer['digest']) or not 0 < layer['size'] <= 1 << 20:
            raise ValueError('invalid previous catalogue blob bounds')
        destination = directory/layer['annotations']['org.opencontainers.image.title']
        result = bounded_fetch(tools, auth, destination, REPOSITORY+'@'+layer['digest'], blob=True)
        if result.returncode or reference(destination) != {key: layer[key] for key in ('digest', 'size')}:
            raise ValueError('previous catalogue blob failed verification')
    verify_signature(tools, directory, 'catalogue')
    previous = json.loads((directory/'catalogue.json').read_text())
    if previous.get('schema') != 1 or previous.get('sequence', 0) >= int(os.environ['GITHUB_RUN_NUMBER']):
        raise ValueError('refusing catalogue sequence rollback/equivocation')
    return previous


def next_catalogue(previous, sequence, now, entries=None, revoke=()):
    if previous and previous['sequence'] >= sequence:
        raise ValueError('new catalogue sequence must increase')
    approved = previous['images'] if entries is None else entries
    # Catalogues before VM and machine images carry disks the CLI no longer reads;
    # only a publication replaces them.
    if any(entry.get('kind') not in ('vm', 'machine') for entry in approved):
        raise ValueError('the current catalogue predates VM and machine images; publish before refreshing')
    revoked = set(previous['revoked'] if previous else [])
    advertised = {entry['manifest'] for entry in (previous['images'] if previous else [])} | {entry['manifest'] for entry in approved}
    if not set(revoke) <= advertised | revoked:
        raise ValueError('withdrawal digest must identify a currently approved or already revoked image')
    revoked.update(revoke)
    approved = [entry for entry in approved if entry['manifest'] not in revoked]
    return dict(schema=1, sequence=sequence, created=now.isoformat().replace('+00:00', 'Z'),
                expires=(now+timedelta(days=30)).isoformat().replace('+00:00', 'Z'),
                images=approved, revoked=sorted(revoked))


def entry(item, manifest):
    """The catalogue entry for a pushed image (docs/specs/image-delivery.md#catalogue)."""
    if item['kind'] == 'vm':
        return dict(kind='vm', architecture=item['architecture'], agent_protocol=item['agent_protocol'],
                    manifest=manifest, build_id=item['build_id'])
    return dict(kind='machine', selectors=item['selectors'], architecture=item['architecture'],
                machine_protocol=item['machine_protocol'], manifest=manifest, build_id=item['build_id'])


def complete(built):
    """A publication carries one VM image and every machine profile."""
    kinds = [item['kind'] for item in built]
    selectors = {tuple(item['selectors']) for item in built if item['kind'] == 'machine'}
    if kinds.count('vm') != 1 or selectors != {tuple(v) for v in SELECTORS.values()} or len(built) != 1 + len(SELECTORS):
        raise ValueError('cannot promote an incomplete image matrix')


def publish(base, operation='publish'):
    if operation != 'publish':
        base.mkdir(parents=True)
    tools = ROOT/'build/publisher/tools'
    auth = base/'registry.json'
    run([tools/'oras', 'login', '--registry-config', auth, '--username', os.environ['GITHUB_ACTOR'], '--password-stdin', 'ghcr.io'],
        input=os.environ['GHCR_TOKEN'], text=True)
    auth.chmod(0o600)
    try:
        entries = None
        if operation == 'publish':
            built = json.loads((base/'built.json').read_text())
            complete(built)
            entries = []
            for item in built:
                directory = base/'public'/item['name']
                descriptor = json.loads((directory/'descriptor.json').read_text())
                payload = KINDS[item['kind']]['payload']
                for field, name in [('compressed', payload), ('packages', 'packages.json'), ('provenance', 'provenance.json'),
                                    ('acceptance', 'acceptance.json')]:
                    if reference(directory/name) != descriptor[field]:
                        raise ValueError('prepared payload changed before signing')
                sign(tools, directory, 'descriptor')
                tag = f'{item["name"]}-run{os.environ["GITHUB_RUN_NUMBER"]}'
                digest = push(tools, auth, directory, item['kind'], tag,
                              ['descriptor.json:application/json', 'descriptor.sigstore.json:application/json',
                               f'{payload}:{KINDS[item["kind"]]["media"]}', 'packages.json:application/json',
                               'provenance.json:application/json', 'acceptance.json:application/json'])
                entries.append(entry(item, digest))
        catalogue = base/'catalogue'
        catalogue.mkdir()
        now = datetime.now(timezone.utc).replace(microsecond=0)
        previous = previous_catalogue(tools, auth, base, allow_missing=operation == 'publish')
        revoke = os.environ.get('REVOKE_DIGESTS', '').replace(',', ' ').split() if operation == 'withdraw' else []
        if operation == 'withdraw' and (not revoke or any(not re.fullmatch(r'sha256:[a-f0-9]{64}', d) for d in revoke)):
            raise ValueError('withdraw requires one or more full SHA256 manifest digests')
        document = next_catalogue(previous, int(os.environ['GITHUB_RUN_NUMBER']), now, entries, revoke)
        write(catalogue/'catalogue.json', document)
        sign(tools, catalogue, 'catalogue')
        digest = push(tools, auth, catalogue, 'catalogue', 'catalogue-v1',
                      ['catalogue.json:application/json', 'catalogue.sigstore.json:application/json'])
        write(base/'published.json', dict(catalogue_manifest=digest, sequence=document['sequence'], images=document['images']))
        print(f'Published catalogue {digest}, sequence {document["sequence"]}', flush=True)
    finally:
        auth.unlink(missing_ok=True)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('phase', choices=['build', 'publish', 'refresh', 'withdraw'])
    a = p.parse_args()
    os.chdir(ROOT)
    require_actions()
    base = ROOT/'build/publication'/os.environ['GITHUB_RUN_ID']
    if a.phase == 'build':
        build(base)
    else:
        publish(base, a.phase)


if __name__ == '__main__':
    main()

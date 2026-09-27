#!/usr/bin/env python3
"""Build/test generic images, then sign and publish a complete catalogue in Actions."""
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
ALIASES = {('debian', 'trixie'): ['debian:trixie', 'debian:13'],
           ('ubuntu', 'noble'): ['ubuntu:noble', 'ubuntu:24.04'],
           ('fedora', '44'): ['fedora:44'], ('centos', '10'): ['centos:10', 'centos-stream:10'],
           ('opensuse', '16.0'): ['opensuse:16.0', 'opensuse-leap:16.0'],
           ('opensuse', 'tumbleweed'): ['opensuse:tumbleweed', 'opensuse-tumbleweed:rolling'],
           ('arch', 'rolling'): ['arch:rolling']}


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


def acceptance(evidence, image, raw):
    results = json.loads((evidence/'results.json').read_text())
    lifecycle = json.loads((evidence/'lifecycle.json').read_text())
    maintenance = json.loads((evidence/'maintenance.json').read_text())
    storage = json.loads((evidence/'storage.json').read_text())
    if results.get('passed') is not True or results.get('image_sha256') != raw['digest'][7:] or results.get('image') != image:
        raise ValueError('acceptance does not match the generic image')
    expected_cases = {'independent_vms_and_grown_roots', 'separate_homes', 'host_port_not_displaced',
                      'conflict_retry', 'cross_vm_conflict_and_handoff',
                      'forced_exit_recovery_preserves_data_and_peer', 'missing_forwarder_restarted'}
    if set(lifecycle) != expected_cases | {'stop_dev', 'stop_peer'}:
        raise ValueError('incomplete lifecycle coverage')
    for name in expected_cases:
        if lifecycle[name].get('pass') is not True:
            raise ValueError(f'failed lifecycle case {name}')
    if any(lifecycle[name].get('exit') != 0 for name in ('stop_dev', 'stop_peer')):
        raise ValueError('guest shutdown failed')
    for report in (maintenance, storage):
        if report.get('passed') is not True:
            raise ValueError('maintenance/storage acceptance failed')
    if maintenance.get('image') != image:
        raise ValueError('maintenance image mismatch')
    # Explicit public fields only. Never publish logs, host paths, private keys,
    # guest disks or the backup archives alongside the acceptance report.
    return dict(schema=1, passed=True, raw=raw, image=image,
                systemd=results['systemd'], root_filesystem=results['root_filesystem'],
                argv_binary_streams_exit_and_pty=results['argv_binary_streams_exit_and_pty'],
                share_ownership=results['share_ownership'],
                selinux=results.get('selinux'), apparmor=bool(results.get('apparmor')),
                lifecycle={key: lifecycle[key]['pass'] for key in sorted(expected_cases)},
                maintenance={key: maintenance[key] for key in (
                    'kernel_before', 'kernel_after', 'podman_version', 'rootless', 'storage_driver',
                    'container_base_digest', 'build_bind_volume_dns_https', 'host_localhost_http',
                    'kernel_reinstall_and_reboot', 'newer_kernel_upgrade_tested',
                    'container_and_volume_survive_vm_restart')},
                storage={key: storage[key] for key in (
                    'root_bytes_before', 'root_bytes_after', 'running_mutations_refused',
                    'growth_preserved_identity_and_data', 'grown_backup_restores_without_cache',
                    'removal_preserved_project_cache_backups', 'name_reused_with_new_identity', 'peer_uninterrupted')})


def require_actions():
    if (os.environ.get('GITHUB_REPOSITORY') != 'frostyard/nsl' or os.environ.get('GITHUB_REF') != 'refs/heads/main'
            or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch'
            or os.environ.get('GITHUB_WORKFLOW_REF') != 'frostyard/nsl/.github/workflows/images.yml@refs/heads/main'):
        raise ValueError('publication requires the designated manually dispatched main-branch workflow')
    for key in ('GITHUB_RUN_ID', 'GITHUB_RUN_NUMBER', 'GITHUB_RUN_ATTEMPT'):
        if not re.fullmatch(r'[1-9][0-9]*', os.environ.get(key, '')):
            raise ValueError(f'invalid {key}')
    if os.environ['GITHUB_RUN_ATTEMPT'] != '1':
        raise ValueError('dispatch a new run instead of rerunning a publication sequence')
    if run(['git', 'rev-parse', 'HEAD'], capture_output=True, text=True).stdout.strip() != os.environ['GITHUB_SHA']:
        raise ValueError('checkout does not match workflow source')
    if run(['git', 'status', '--porcelain'], capture_output=True, text=True).stdout:
        raise ValueError('publication requires clean tracked source')


def build(base):
    base.mkdir(parents=True)  # retries use a new run; never overwrite prior evidence
    run(['make', 'ci'])
    run(['make', 'build'])
    run(['go', 'build', '-o', base/'compress-image', 'scripts/compress-image.go'])
    built = []
    for distribution, release in compose.PROFILES:
        profile = compose.select(ROOT, distribution, release)
        name = 'nsl-{distribution}-{release}-{architecture}-v{revision}'.format(**profile)
        print(f'::group::Build and validate {name}', flush=True)
        start = time.monotonic()
        run(['scripts/build-image.sh', '--distribution', distribution, '--release', release])
        raw_path = ROOT/'build/image/share'/f'{name}.raw'
        image = json.loads(raw_path.with_suffix('.json').read_text())
        evidence = base/name/'private-evidence'
        run(['python3', 'scripts/probe-distribution.py', '--image', raw_path,
             '--home', base/name/'vm-state', '--project', base/name/'project', '--evidence', evidence])
        raw = reference(raw_path)
        report = acceptance(evidence, image, raw)
        public = base/name/'public'
        public.mkdir()
        shutil.copyfile(raw_path.with_suffix('.manifest'), public/'packages.json')
        write(public/'acceptance.json', report)
        run([base/'compress-image', raw_path, public/'disk.raw.zst'])
        compressed = reference(public/'disk.raw.zst')
        if raw['size'] > 32<<30 or compressed['size'] > 8<<30:
            raise ValueError('image exceeds client format bounds')
        host = {tool: run(args, capture_output=True, text=True).stdout.splitlines()[0]
                for tool, args in [('systemd', ['systemctl', '--version']), ('qemu', ['qemu-system-x86_64', '--version']),
                                   ('virtiofsd', ['/usr/libexec/virtiofsd', '--version'])]}
        write(public/'provenance.json', dict(schema=1, source='https://github.com/frostyard/nsl',
              revision=os.environ['GITHUB_SHA'], workflow=PUBLISHER, run_id=os.environ['GITHUB_RUN_ID'],
              run_attempt=os.environ['GITHUB_RUN_ATTEMPT'], host=host, image=image,
              raw=raw, compressed=compressed, build_test_compress_seconds=time.monotonic()-start))
        descriptor = dict(schema=1, image=image, raw=raw, compressed=compressed,
                          packages=reference(public/'packages.json'), provenance=reference(public/'provenance.json'),
                          acceptance=reference(public/'acceptance.json'))
        if any(descriptor[key]['size'] > 16<<20 for key in ('packages', 'provenance', 'acceptance')):
            raise ValueError('evidence exceeds client bounds')
        write(public/'descriptor.json', descriptor)
        built.append(dict(name=name, selectors=ALIASES[distribution, release], architecture=image['architecture']))
        print('::endgroup::', flush=True)
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
    revoked = set(previous['revoked'] if previous else [])
    advertised = {entry['manifest'] for entry in approved}
    if not set(revoke) <= advertised | revoked:
        raise ValueError('withdrawal digest must identify a currently approved or already revoked image')
    revoked.update(revoke)
    approved = [entry for entry in approved if entry['manifest'] not in revoked]
    return dict(schema=1, sequence=sequence, created=now.isoformat().replace('+00:00', 'Z'),
                expires=(now+timedelta(days=30)).isoformat().replace('+00:00', 'Z'),
                images=approved, revoked=sorted(revoked))


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
            if {tuple(item['selectors']) for item in built} != {tuple(v) for v in ALIASES.values()} or len(built) != 7:
                raise ValueError('cannot promote an incomplete image matrix')
            entries = []
            for item in built:
                directory = base/item['name']/'public'
                descriptor = json.loads((directory/'descriptor.json').read_text())
                for field, name in [('compressed','disk.raw.zst'), ('packages','packages.json'), ('provenance','provenance.json'), ('acceptance','acceptance.json')]:
                    if reference(directory/name) != descriptor[field]:
                        raise ValueError('prepared payload changed before signing')
                sign(tools, directory, 'descriptor')
                tag = f'{item["name"]}-run{os.environ["GITHUB_RUN_NUMBER"]}'
                digest = push(tools, auth, directory, 'image', tag,
                              ['descriptor.json:application/json', 'descriptor.sigstore.json:application/json',
                               'disk.raw.zst:application/zstd', 'packages.json:application/json',
                               'provenance.json:application/json', 'acceptance.json:application/json'])
                entries.append(dict(selectors=item['selectors'], architecture=item['architecture'], manifest=digest, build_id=item['name']))
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
        write(base/'published.json', dict(catalogue_manifest=digest, images=document['images']))
        print('Published catalogue '+digest, flush=True)
    finally:
        auth.unlink(missing_ok=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
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

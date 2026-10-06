#!/usr/bin/env python3
"""Build pinned, identified images locally. Never pushes or deploys an image."""
import argparse
import gzip
import json
from pathlib import Path
import re
import subprocess

from artifacts import file_sha256

ROOT = Path(__file__).resolve().parent.parent


def capture(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--prefix', default='novelbot')
    parser.add_argument('--output')
    args = parser.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', args.version):
        parser.error('version must be an explicit Docker tag (use local for test builds)')
    if not re.fullmatch(r'[a-z0-9][a-z0-9./:_-]*', args.prefix):
        parser.error('invalid image prefix')
    revision = capture('git', 'rev-parse', 'HEAD')
    dirty = bool(capture('git', 'status', '--porcelain'))
    if args.version != 'local' and dirty:
        parser.error('release builds require a clean checkout; commit reviewed changes first')
    inputs = json.loads((ROOT / 'deploy/build-inputs.json').read_text())
    roles = ('api', 'worker', 'migrate')
    if args.output:
        output = Path(args.output).resolve()
        output.mkdir(parents=True, exist_ok=True)
        # A rebuild invalidates earlier scan evidence for these image names.
        for name in ['scan-summary.json', 'dependencies.vulnerabilities.json', 'SHA256SUMS'] + [
                role + suffix for role in roles
                for suffix in ('.cdx.json', '.vulnerabilities.json')]:
            path = output / name
            if path.exists():
                path.unlink()
    images = {}
    for role in roles:
        image = f'{args.prefix}/{role}:{args.version}'
        command = ['docker', 'build', '--target', role, '--tag', image,
                   '--label', f'org.opencontainers.image.version={args.version}',
                   '--label', f'org.opencontainers.image.revision={revision}']
        for key, value in inputs.items():
            command.extend(['--build-arg', f'{key}={value}'])
        command.append('.')
        subprocess.run(command, cwd=ROOT, check=True)
        images[role] = {
            'name': image,
            'image_config_digest': capture('docker', 'image', 'inspect', image, '--format', '{{.Id}}'),
            'architecture': capture('docker', 'image', 'inspect', image, '--format', '{{.Architecture}}'),
            'binary_sha256': capture('docker', 'run', '--rm', '--network=none', '--cap-drop=ALL',
                                     '--entrypoint', 'sha256sum', image, f'/usr/local/bin/{role}').split()[0],
        }
    if args.output:
        output = Path(args.output).resolve()
        output.mkdir(parents=True, exist_ok=True)
        worker = images['worker']['name']
        browser = capture('docker', 'run', '--rm', '--network=none', '--cap-drop=ALL',
                          '--entrypoint', 'chromium', worker, '--version')
        packages = capture('docker', 'run', '--rm', '--network=none', '--cap-drop=ALL',
                           '--entrypoint', 'dpkg-query', worker, '-W', '-f=${Package}\t${Version}\n')
        (output / 'worker-packages.txt').write_text(packages + '\n')
        archive = output / 'images.tar.gz'
        with archive.open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', mtime=0) as compressed:
            child = subprocess.Popen(['docker', 'save', *[x['name'] for x in images.values()]], stdout=subprocess.PIPE)
            while data := child.stdout.read(1024 * 1024):
                compressed.write(data)
            child.stdout.close()
            if child.wait():
                raise RuntimeError('docker save failed')
        manifest = {'version': args.version, 'revision': revision, 'dirty': dirty,
                    'inputs': inputs, 'images': images, 'browser': browser}
        (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        sums = []
        for path in sorted(output.iterdir()):
            if path.is_file() and path.name != 'SHA256SUMS':
                digest = file_sha256(path)
                sums.append(f'{digest}  {path.name}')
        (output / 'SHA256SUMS').write_text('\n'.join(sums) + '\n')


if __name__ == '__main__':
    main()

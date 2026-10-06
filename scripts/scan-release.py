#!/usr/bin/env python3
"""Produce release SBOMs/scans; block fixed HIGH/CRITICAL vulnerabilities."""
import argparse
import datetime
import hashlib
import io
import json
from pathlib import Path
import platform
import subprocess
import tarfile
import urllib.request

from artifacts import file_sha256

ROOT = Path(__file__).resolve().parent.parent
VERSION = '0.75.0'
# Official trivy_0.75.0_checksums.txt; review these when updating the scanner.
DOWNLOADS = {
    ('Linux', 'x86_64'): ('Linux-64bit', 'c6e65abddb348e25f10549df887045629cf28cc72453cd1c63acb717316b3f3f'),
    ('Linux', 'aarch64'): ('Linux-ARM64', 'a1ee9f6ffb7d112b64ff726a2a0717c21175c1114361391f4a132956751a13b3'),
    ('Darwin', 'arm64'): ('macOS-ARM64', '4a77108cccf8e55c8d6823e1e759939a622277e66cd0daa3c1fc621ed69e4568'),
    ('Darwin', 'x86_64'): ('macOS-64bit', '291edaa9778acbe4693d067b5ad60ee11570e5ac68296e85595417528ca641e4'),
}


def scanner():
    suffix, digest = DOWNLOADS[(platform.system(), platform.machine())]
    binary = ROOT / 'bin' / f'trivy-{VERSION}'
    if not binary.exists():
        url = f'https://github.com/aquasecurity/trivy/releases/download/v{VERSION}/trivy_{VERSION}_{suffix}.tar.gz'
        with urllib.request.urlopen(url, timeout=120) as response:
            data = response.read()
        if hashlib.sha256(data).hexdigest() != digest:
            raise RuntimeError('Trivy archive checksum mismatch')
        # Extract only the named binary; no archive paths are trusted.
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
            member = archive.getmember('trivy')
            if not member.isfile():
                raise RuntimeError('Trivy archive binary is not a regular file')
            binary.parent.mkdir(exist_ok=True)
            binary.write_bytes(archive.extractfile(member).read())
        binary.chmod(0o755)
    return binary


def checksums(directory):
    lines = []
    for path in sorted(directory.iterdir()):
        if path.is_file() and path.name != 'SHA256SUMS':
            digest = file_sha256(path)
            lines.append(f'{digest}  {path.name}')
    (directory / 'SHA256SUMS').write_text('\n'.join(lines) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', help='artifact directory containing manifest.json')
    args = parser.parse_args()
    directory = Path(args.directory).resolve()
    manifest = json.loads((directory / 'manifest.json').read_text())
    # Invalidate earlier scan evidence before any startup/download can fail.
    for name in ['scan-summary.json', 'dependencies.vulnerabilities.json'] + [
            role + suffix for role in manifest['images']
            for suffix in ('.cdx.json', '.vulnerabilities.json')]:
        path = directory / name
        if path.exists():
            path.unlink()
    tool = str(scanner())
    reports = []
    try:
        for role, image in manifest['images'].items():
            subprocess.run([tool, 'image', '--format', 'cyclonedx', '--output',
                            str(directory / f'{role}.cdx.json'), image['name']], check=True, cwd=ROOT)
            report = directory / f'{role}.vulnerabilities.json'
            subprocess.run([tool, 'image', '--scanners', 'vuln', '--format', 'json',
                            '--output', str(report), image['name']], check=True, cwd=ROOT)
            reports.append(report)
        report = directory / 'dependencies.vulnerabilities.json'
        subprocess.run([tool, 'fs', '--scanners', 'vuln', '--format', 'json', '--skip-dirs',
                        'bin,test-results,release-artifacts,loadtest-results', '--output', str(report), '.'],
                       check=True, cwd=ROOT)
        reports.append(report)
        blocked = []
        for report in reports:
            data = json.loads(report.read_text())
            for result in data.get('Results', []):
                for vulnerability in result.get('Vulnerabilities', []):
                    if vulnerability.get('Severity') in ('HIGH', 'CRITICAL') and vulnerability.get('FixedVersion'):
                        blocked.append({'report': report.name, 'target': result['Target'],
                                        'id': vulnerability['VulnerabilityID'], 'package': vulnerability['PkgName'],
                                        'installed': vulnerability['InstalledVersion'], 'fixed': vulnerability['FixedVersion']})
        summary = {'scanner': f'trivy {VERSION}',
                   'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   'scanner_database_info': subprocess.check_output([tool, '--version'], text=True),
                   'blocking_findings': blocked,
                   'policy': 'Block fixed HIGH/CRITICAL findings; record all severities and unfixed findings for review.'}
        (directory / 'scan-summary.json').write_text(json.dumps(summary, indent=2) + '\n')
        if blocked:
            raise SystemExit(f'{len(blocked)} fixed HIGH/CRITICAL findings block release; see scan-summary.json')
    finally:
        checksums(directory)


if __name__ == '__main__':
    main()

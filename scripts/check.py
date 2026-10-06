#!/usr/bin/env python3
"""Validate checked-in source/contracts/manifests without reading developer secrets."""
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent


def main():
    source = [str(p.relative_to(ROOT)) for p in ROOT.rglob('*.go')
              if not any(part.startswith('.') or part == 'bin' for part in p.relative_to(ROOT).parts)]
    unformatted = subprocess.check_output(['gofmt', '-l', *source], cwd=ROOT, text=True).strip()
    if unformatted:
        sys.exit('Run gofmt on:\n' + unformatted)
    spec = json.loads((ROOT / 'api/openapi.json').read_text())
    if spec.get('openapi') != '3.0.3' or not spec.get('paths'):
        sys.exit('Invalid OpenAPI contract')
    # Full OpenAPI validation and route/response coverage run in make test.
    inputs = json.loads((ROOT / 'deploy/build-inputs.json').read_text())
    dockerfile = (ROOT / 'Dockerfile').read_text()
    for key, value in inputs.items():
        if not re.search(r'^ARG ' + re.escape(key + '=' + value) + r'$', dockerfile, re.M):
            sys.exit(f'Dockerfile and build-inputs.json disagree on {key}')
    helper = (ROOT / 'internal/testutil/containers.go').read_text()
    compose = (ROOT / 'compose.yaml').read_text()
    for constant in ('PostgresImage', 'RedisImage'):
        image = re.search(r'const ' + constant + r' = "([^"]+)"', helper).group(1)
        if '@sha256:' not in image or 'image: ' + image not in compose:
            sys.exit(f'Compose and test dependencies disagree on {constant}')
    for overlay in (None, 'compose.capacity.yaml'):
        command = ['docker', 'compose', '--env-file', '/dev/null', '-f', 'compose.yaml']
        if overlay:
            command.extend(['-f', overlay])
        command.extend(['--profile', 'app', '--profile', 'benchmark', 'config', '--quiet'])
        subprocess.run(command, cwd=ROOT, check=True)
    # Future production manifests must be added to this validation and CI before use.
    print('Formatting, OpenAPI JSON, pinned inputs, and Compose validation passed.')


if __name__ == '__main__':
    main()

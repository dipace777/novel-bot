#!/usr/bin/env python3
"""Measure one isolated worker; never modify the user's running deployment or tenant policies."""
import argparse
import base64
import datetime
import json
import math
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import time
import urllib.request
import urllib.error

ROOT = Path(__file__).resolve().parent.parent

def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--levels', default='1,2,4,6,8')
    parser.add_argument('--rounds', type=int, default=3)
    parser.add_argument('--hold', default='20s')
    parser.add_argument('--soak-rounds', type=int, default=3)
    parser.add_argument('--soak-hold', default='60s')
    parser.add_argument('--churn-rounds', type=int, default=15)
    parser.add_argument('--cpus', type=float, default=4)
    parser.add_argument('--memory-mib', type=int, default=4096)
    parser.add_argument('--output', default='loadtest-results/capacity')
    parser.add_argument('--no-build', action='store_true', help='Reuse locally built novel-bot images')
    args = parser.parse_args()
    levels = [int(n) for n in args.levels.split(',')]
    if not levels or sorted(set(levels)) != levels or min(levels) < 1 or max(levels) > 64:
        parser.error('levels must increase between 1 and 64')
    if not 0 < args.cpus <= 16 or not 256 <= args.memory_mib <= 65536 or min(args.rounds, args.soak_rounds, args.churn_rounds) < 1:
        parser.error('invalid CPU/memory/round settings')
    engine = json.loads(subprocess.check_output(['docker', 'info', '--format', '{{json .}}'], text=True))
    if engine.get('CgroupVersion') != '2':
        parser.error('capacity qualification requires Docker with cgroup v2')
    if args.cpus > engine.get('NCPU', 0):
        parser.error('worker CPU budget exceeds the CPUs available to Docker')
    if args.memory_mib * 1024 * 1024 > engine.get('MemTotal', 0) * .8:
        parser.error('worker memory budget must leave at least 20% of Docker RAM for the generator/infrastructure')
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    project = 'novelbot-capacity-' + secrets.token_hex(4)
    env = os.environ.copy()
    for name in ('COMPOSE_WORKER_MEMORY', 'COMPOSE_WORKER_CPUS', 'COMPOSE_WORKER_MAX_SESSIONS', 'PUBLIC_API_URL', 'TRUSTED_PROXY_CIDRS'):
        env.pop(name, None)
    ports = []
    while len(ports) < 5:
        port = free_port()
        if port not in ports:
            ports.append(port)
    # The compose env file is deliberately disabled. Every secret is temporary.
    env.update(API_KEY_PEPPER=base64.b64encode(secrets.token_bytes(32)).decode(),
               WORKER_AUTH_TOKEN=secrets.token_hex(32), COMPOSE_PROJECT_NAME=project,
               COMPOSE_API_PORT=str(ports[0]), COMPOSE_WORKER_A_PORT=str(ports[1]),
               COMPOSE_WORKER_B_PORT=str(ports[2]), COMPOSE_POSTGRES_PORT=str(ports[3]),
               COMPOSE_REDIS_PORT=str(ports[4]), COMPOSE_REDIS_NAMESPACE='capacity',
               COMPOSE_WORKER_MEMORY=f'{args.memory_mib}m', COMPOSE_WORKER_CPUS=str(args.cpus),
               CAPACITY_MAX_SESSIONS=str(max(levels)), CAPACITY_PIDS_LIMIT='2048',
               AUTH_REQUESTS_PER_MINUTE='1000', AUTH_TIMEOUT='2s', SESSION_TTL='24h',
               COMPOSE_API_MEMORY='512m', COMPOSE_API_CPUS='1', COMPOSE_API_PIDS_LIMIT='128',
               BROWSER_SESSION_TTL='15m', BROWSER_STARTUP_TIMEOUT='10s', SESSION_STARTUP_TIMEOUT='10s',
               WORKER_LEASE_TTL='15s', WORKER_DRAIN_TIMEOUT='1s', WORKER_STOP_GRACE_PERIOD='31s', DB_MAX_CONNS='20', PUBLIC_API_URL='', TRUSTED_PROXY_CIDRS='',
               LOADTEST_WORKER_TOKEN='')
    services = ['api', 'migrate', 'worker-a', 'fixtures']
    compose_args = ['docker', 'compose', '--env-file', '/dev/null', '-p', project,
                    '-f', str(ROOT / 'compose.yaml'), '-f', str(ROOT / 'compose.capacity.yaml'),
                    '--profile', 'app', '--profile', 'benchmark']
    def compose(*cmd, capture=False, stdin=None):
        return subprocess.run(compose_args + list(cmd), cwd=ROOT, env=env,
                              input=stdin, text=True, stdout=subprocess.PIPE if capture else None,
                              check=True).stdout
    api = f'http://127.0.0.1:{ports[0]}'
    metrics = f'http://127.0.0.1:{ports[1]}'
    def request(method, path, body=None, token=None):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        req = urllib.request.Request(api + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=30) as res:
                data = res.read()
                return res.status, json.loads(data) if data else None
        except urllib.error.HTTPError as err:
            # Never echo API response bodies or credentials into shell output.
            return err.code, None
    reports = []
    def reset(slots=None, pids=None):
        if slots is not None:
            env['CAPACITY_MAX_SESSIONS'] = str(slots)
        if pids is not None:
            env['CAPACITY_PIDS_LIMIT'] = str(pids)
        compose('up', '-d', '--no-build', '--force-recreate', '--wait', 'worker-a')
        time.sleep(2)  # Allow complete idle resource samples before the next run.
    def run_test(name, workload, concurrency, rounds, hold):
        path = output / f'{name}.json'
        cmd = [str(ROOT / 'bin/loadtest'), '--url', api, '--metrics-urls', metrics,
               '--workload', workload, '--fixture-url', 'http://fixtures:8082',
               '--concurrency', concurrency, '--rounds', str(rounds), '--hold', hold,
               '--sample-interval', '1s', '--worker-memory-mib', str(args.memory_mib),
               '--worker-cpus', str(args.cpus), '--require-container-resources',
               '--max-startup-p95', '2s', '--max-navigation-p95', '5s', '--output', str(path)]
        print(f'Running {name}: workload={workload}, concurrency={concurrency}, rounds={rounds}, hold={hold}', flush=True)
        result = subprocess.run(cmd, cwd=ROOT, env=env)
        if result.returncode not in (0, 1) or not path.exists():
            raise RuntimeError(f'{name} could not produce a report')
        report = json.loads(path.read_text())
        reports.append({'name': name, 'report': report})
        return report
    try:
        if args.no_build:
            # Different Compose project names otherwise imply different default image names.
            overrides = output / 'images.yaml'
            overrides.write_text('services:\n' + ''.join(f'  {s}:\n    image: novel-bot-{s}:latest\n' for s in services))
            compose_args.extend(['-f', str(overrides)])
        compose('up', '-d', '--no-build' if args.no_build else '--build', '--wait', *services)
        credentials = {'email': 'capacity-'+secrets.token_hex(8)+'@example.com', 'password': secrets.token_urlsafe(32)}
        status, _ = request('POST', '/v1/auth/register', dict(credentials, name='Isolated capacity tenant'))
        if status != 201:
            raise RuntimeError('could not create isolated benchmark tenant')
        status, login = request('POST', '/v1/auth/login', credentials)
        if status != 200:
            raise RuntimeError('could not log in benchmark tenant')
        status, issued = request('POST', '/v1/api-keys', {'name': 'capacity measurement', 'ttl_seconds': 7200}, login['access_token'])
        if status != 201:
            raise RuntimeError('could not provision benchmark key')
        env['LOADTEST_API_KEY'] = issued['api_key']
        tenant = login['user']['client_id']
        if len(tenant) != 32 or any(c not in '0123456789abcdef' for c in tenant):
            raise RuntimeError('invalid benchmark tenant identity')
        # Only the new database belonging to this generated Compose project is modified.
        sql = f"UPDATE clients SET max_concurrent_sessions=64,max_session_seconds=900,session_requests_per_minute=10000 WHERE id='{tenant}';"
        compose('exec', '-T', 'postgres', 'psql', '-U', 'novelbot', '-d', 'novelbot', '-v', 'ON_ERROR_STOP=1', capture=True, stdin=sql)
        time.sleep(2)
        sweep = run_test('mixed-sweep', 'mixed', args.levels, args.rounds, args.hold)
        qualified = [s['concurrency'] for s in (sweep['stages'] or []) if s['qualified']]
        if not qualified:
            raise RuntimeError('no concurrency level qualified; inspect the sweep report')
        upper = max(qualified)
        candidate = max(1, math.floor(upper * .75))  # Reserve placement headroom below the directly measured upper bound.
        while candidate >= 1:
            passing = True
            for workload in ('article', 'feed', 'dashboard'):
                reset(candidate)
                report = run_test(f'{workload}-verify-{candidate}', workload, str(candidate), args.rounds, args.hold)
                if not report['stages'] or not all(s['qualified'] for s in report['stages']):
                    passing = False
                    break
            if not passing:
                candidate -= 1
                continue
            peaks = [w['peak_container_tasks'] for entry in reports for stage in entry['report']['stages']
                     if stage['qualified'] and stage['concurrency'] == candidate for w in stage['workers'].values()]
            pid_limit = max(128, 2 ** math.ceil(math.log2(max(peaks, default=64) * 2)))
            reset(candidate, pid_limit)
            soak = run_test(f'mixed-soak-{candidate}', 'mixed', str(candidate), args.soak_rounds, args.soak_hold)
            if not soak['stages'] or not all(s['qualified'] for s in soak['stages']):
                candidate -= 1
                continue
            reset(candidate, pid_limit)
            churn = run_test(f'mixed-churn-{candidate}', 'mixed', str(candidate), args.churn_rounds, '5s')
            if not churn['stages'] or not all(s['qualified'] for s in churn['stages']):
                candidate -= 1
                continue
            break
        if candidate < 1:
            raise RuntimeError('no operating capacity passed every workload and validation stage')
        info = json.loads(subprocess.check_output(['docker', 'info', '--format', '{{json .}}'], env=env, text=True))
        chrome = compose('exec', '-T', 'worker-a', 'chromium', '--version', capture=True).strip()
        api_peak = int(compose('exec', '-T', 'api', 'cat', '/sys/fs/cgroup/memory.peak', capture=True).strip())
        image_ids = {}
        for role in ('api', 'worker-a', 'fixtures'):
            container = compose('ps', '-q', role, capture=True).strip()
            image_ids[role] = subprocess.check_output(['docker', 'inspect', '--format', '{{.Image}}', container], env=env, text=True).strip()
        summary = {
            'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'environment': {k: info.get(k) for k in ('Architecture', 'NCPU', 'MemTotal', 'KernelVersion', 'CgroupVersion', 'ServerVersion')},
            'image_ids': image_ids, 'chromium': chrome, 'fixture_version': 'mixed-scraping-v1',
            'limits': {'worker_cpus': args.cpus, 'worker_memory_bytes': args.memory_mib * 1024 * 1024,
                       'worker_capacity': candidate, 'worker_pids_limit': pid_limit, 'shared_memory_bytes': 512 * 1024 * 1024},
            'highest_qualified_sweep_concurrency': upper,
            'api_peak_container_memory_bytes': api_peak,
            'reports': reports,
            'scope': 'Controlled mixed scraping fixtures on this Docker host. Repeat with real customer workloads on the target production hardware.'}
        (output / 'summary.json').write_text(json.dumps(summary, indent=2)+'\n')
        (output / 'capacity.env').write_text(f'COMPOSE_WORKER_MAX_SESSIONS={candidate}\nCOMPOSE_WORKER_CPUS={args.cpus}\nCOMPOSE_WORKER_MEMORY={args.memory_mib}m\nCOMPOSE_WORKER_PIDS_LIMIT={pid_limit}\n')
        print(f'Validated operating capacity: {candidate} browsers, {args.cpus} CPUs, {args.memory_mib} MiB RAM, task limit {pid_limit}. Reports: {output}', flush=True)
    finally:
        # This project name is generated by this script; remove only its own test resources.
        subprocess.run(compose_args + ['down', '--volumes', '--remove-orphans'], cwd=ROOT, env=env, check=False)

if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError, OSError, ValueError, KeyError) as err:
        print(f'Capacity measurement failed: {err}', file=sys.stderr)
        sys.exit(1)

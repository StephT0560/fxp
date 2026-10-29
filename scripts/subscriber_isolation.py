#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Same corrected harness at two revisions; publish counts and ratios, never times."""
import argparse
import json
import os
from pathlib import Path
import shutil
import statistics
import subprocess

from phase1_lite import ROOT, run
from profile_guard import ensure_ports_free, wait_port


def main():
    parser = argparse.ArgumentParser(__doc__)
    parser.add_argument('--before', default='663cb26196eb528223d03ebb828fab427fa91f50')
    parser.add_argument('--output', default='benchmarks/subscriber-isolation')
    parser.add_argument('--no-build', action='store_true')
    args = parser.parse_args()
    out = ROOT / args.output
    scratch = ROOT / 'work/subscriber-isolation'
    out.mkdir(parents=True, exist_ok=True)
    scratch.mkdir(parents=True, exist_ok=True)
    env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
    env.update(FXP_JWT_SECRET='phase1-local-only-0123456789abcdef0123456789',
               FXP_JWT_ISSUER='phase1', FXP_JWT_AUDIENCE='phase1',
               FXP_TLS_REQUIRED='false', FXP_CLIENT_TLS='false',
               FXP_GW_RATE='1000000', FXP_GW_RATE_BURST='1000000',
               GOMAXPROCS='1', TOKIO_WORKER_THREADS='1', RUST_LOG='error', FXP_LOG_LEVEL='error')
    base = scratch / 'before-source'
    before = run(['git', 'rev-parse', args.before]).decode().strip()
    after = run(['git', 'rev-parse', 'HEAD']).decode().strip()
    if not args.no_build:
        if base.exists():
            raise RuntimeError('before-source exists; reuse --no-build or remove the worktree explicitly')
        run(['git', 'worktree', 'add', '--detach', base, before])
        run(['bash', 'scripts/gen_proto.sh'], cwd=base, env=env, log=scratch/'before-proto.log')
        # One build cache, copied executable artifacts per revision. The benchmark
        # is built ONCE from the after revision and used for both sides.
        run(['go', 'build', '-o', scratch/'bench', './benchmark'], cwd=ROOT/'go_gateway', env=env)
        for label, source in [('before', base), ('after', ROOT)]:
            dest = scratch / label
            dest.mkdir(exist_ok=True)
            build_env = dict(env, CARGO_TARGET_DIR=str(ROOT/'fxp_core/target'))
            run(['cargo', 'build', '--release', '--locked', '--bin', 'fxp_core'], cwd=source/'fxp_core', env=build_env, log=dest/'build.log')
            shutil.copy2(ROOT/'fxp_core/target/release/fxp_core', dest/'core')
            data = run(['cargo', 'test', '--release', '--locked', '--features', 'phase1', '--lib', '--no-run', '--message-format=json'], cwd=source/'fxp_core', env=build_env, log=dest/'core-test-build.log')
            binaries = [obj['executable'] for line in data.splitlines() if line.startswith(b'{')
                        for obj in [json.loads(line)] if obj.get('reason') == 'compiler-artifact'
                        and obj.get('executable') and obj.get('profile', {}).get('test')]
            shutil.copy2(binaries[-1], dest/'core.test')
            run(['go', 'build', '-o', dest/'gateway', '.'], cwd=source/'go_gateway', env=env, log=dest/'gateway-build.log')
            run(['go', 'test', '-tags', 'phase1', '-c', '-o', dest/'gateway.test', './transport'], cwd=source/'go_gateway', env=env, log=dest/'gateway-test-build.log')
    counts = []
    for n in [0, 10, 100]:
        for repetition in range(1, 6):
            for label in ['before', 'after']:
                dest = scratch / label
                entry = {'subscribers': n, 'repetition': repetition, 'revision': label}
                for component, binary, command in [
                    ('core', dest/'core.test', ['--ignored', '--exact', 'server::phase1::core_attribution', '--test-threads=1']),
                    ('gateway', dest/'gateway.test', ['-test.run', '^TestPhase1Attribution$'])]:
                    path = scratch/f'{label}-{n}-{repetition}-{component}.json'
                    run([binary, *command], env=dict(env, FXP_PHASE1_SUBSCRIBERS=str(int(n > 0) if component == 'core' else n), FXP_PHASE1_OUTPUT=str(path)), log=scratch/f'{label}-{n}-{repetition}-{component}.log')
                    entry[component] = json.loads(path.read_text())
                counts.append(entry)
    (out/'counts.json').write_text(json.dumps(counts, indent=2)+'\n')
    cpus = sorted(os.sched_getaffinity(0))
    if len(cpus) < 3:
        raise RuntimeError('three CPUs required; adapt and document placement before running')
    core_cpu, gateway_cpu, generator_cpu = cpus[:3]
    subscriber_cpu = cpus[3] if len(cpus) > 3 else core_cpu
    layout = {'core': core_cpu, 'gateway': gateway_cpu, 'generator': generator_cpu, 'subscriber': subscriber_cpu}
    ratios = []
    for n in [0, 10, 100]:
        for pair in range(1, 6):
            values = {}
            for label in ['before', 'after']:
                ensure_ports_free([8080, 8081, 8082])
                children, logs = [], []
                name = f'{n}-{pair}-{label}'
                try:
                    for component, cpu, port in [('core', core_cpu, 8080), ('gateway', gateway_cpu, 8082)]:
                        log = open(scratch/f'{name}-{component}.log', 'wb')
                        logs.append(log)
                        child = subprocess.Popen(['taskset', '-c', str(cpu), scratch/label/component], env=env, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
                        children.append(child)
                        wait_port(port, child)
                    report = scratch/f'{name}.json'
                    cmd = ['taskset', '-c', str(generator_cpu), scratch/'bench', '--mode', 'closed', '--pacer', 'sleep', '--concurrency', '1', '--orders', '2000', '--warmup', '100', '--subscribers', str(n), '--subscriber-drain-updates', '4000', '--subscriber-process', '--subscriber-cpu', str(subscriber_cpu), '--repetitions', '1', '--json', report, '--markdown', scratch/f'{name}.md']
                    run(cmd, env=env, log=scratch/f'{name}-client.log')
                    result = json.loads(report.read_text())
                    value = result['runs'][0]
                    if result.get('error') or value['orders'] != 2000 or value.get('market_data_updates_received', 0) != 4000*n:
                        raise RuntimeError('incomplete workload')
                    values[label] = value['orders_per_second']
                finally:
                    for child in reversed(children):
                        child.terminate()
                        try:
                            child.wait(timeout=20)
                        except subprocess.TimeoutExpired:
                            child.kill()
                            child.wait()
                            raise
                    for log in logs:
                        log.close()
            ratios.append({'subscribers': n, 'pair': pair, 'after_over_before': values['after']/values['before']})
    summary = []
    for n in [0, 10, 100]:
        values = [x['after_over_before'] for x in ratios if x['subscribers'] == n]
        summary.append({'subscribers': n, 'median': statistics.median(values), 'min': min(values), 'max': max(values)})
    (out/'relative.json').write_text(json.dumps({'label': 'relative, shared hardware', 'before': before, 'after': after, 'same_harness': after, 'layout': layout, 'order': 'before,after repeated five times for each N; fresh stack each half', 'pairs': ratios, 'summary': summary}, indent=2)+'\n')
    # Independent socket/queue check, not part of paired throughput measurement.
    run(['go', 'test', '-race', '-tags', 'phase1', './transport', '-run', '^TestSubscriberIsolationRelative$', '-count=1'], cwd=ROOT/'go_gateway', env=dict(env, FXP_ISOLATION_OUTPUT=str(out/'slow-consumer.json')), log=scratch/'slow-consumer.log')
    (out/'environment.json').write_text(json.dumps({'before': before, 'after': after, 'working_tree_dirty': bool(run(['git', 'status', '--porcelain']).strip()), 'cpu': next(x.split(':', 1)[1].strip() for x in Path('/proc/cpuinfo').read_text().splitlines() if x.startswith('model name')), 'cpu_max': Path('/sys/fs/cgroup/cpu.max').read_text().strip(), 'layout': layout, 'rust': run(['rustc', '--version']).decode().strip(), 'go': run(['go', 'version']).decode().strip(), 'build': 'Rust release, Go normal; no counters during throughput; allocation fixtures phase1; slow functional check race enabled', 'durability': 'recording and event log disabled', 'absolute_timings': 'smoke only, ignored work/; never published'}, indent=2)+'\n')
    print('PASS counts, ratios and bounded slow-consumer functional check; no absolute timing results')


if __name__ == '__main__':
    main()

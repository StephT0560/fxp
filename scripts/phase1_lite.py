#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Subscriber work attribution + paired ratios. Absolute times stay in work/ only."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import statistics
import subprocess
import time

from profile_guard import ensure_ports_free, wait_port
ROOT = Path(__file__).resolve().parents[1]


def run(cmd, *, env=None, log=None, cwd=ROOT):
    result = subprocess.run([str(x) for x in cmd], cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if log:
        Path(log).write_bytes(result.stdout)
    if result.returncode:
        raise RuntimeError(f'{cmd[0]} failed ({result.returncode}); inspect {log}: {result.stdout[-1500:].decode(errors="replace")}')
    return result.stdout


def syscall_counts(path):
    # -xx -s 8 captures length prefix + envelope tag; do not count profile files,
    # trades, snapshots or benchmark input. Pending entries are keyed by TID.
    count = errors = shorts = complete = 0
    pending = {}
    for line in Path(path).read_text().splitlines():
        tid = line.split(maxsplit=1)[0] if line.strip() else ''
        size = None
        if re.search(r'\b(?:write|writev|sendto|sendmsg)\(.*<TCP:', line) and '"' in line:
            wire = re.findall(r'\\x([0-9a-f]{2})', line.split('"', 2)[1])
            if len(wire) >= 5 and wire[4] == '52':
                count += 1
                expected = re.search(r', (\d+)(?:\)| <unfinished)', line)
                size = int(expected[1]) if expected else None
                if '<unfinished' in line:
                    pending[tid] = size
                    continue
        elif 'resumed>' in line and tid in pending:
            size = pending.pop(tid)
        if size is not None:
            result = re.search(r'= (-?\d+)', line)
            if result:
                returned = int(result[1])
                if returned < 0: errors += 1
                elif returned < size: shorts += 1
                else: complete += 1
    return {'incremental_write_syscall_attempts': count, 'errors': errors,
            'short_writes': shorts, 'complete_writes': complete,
            'unresolved_entries': len(pending),
            'scope': 'incremental framed TCP writes; syscall entries counted including resumed errors'}


def save_dhat_counts(target, scratch, label):
    raw_path = Path(str(target) + '.dhat.json')
    raw = json.loads(raw_path.read_text())
    raw_path.replace(scratch / f'{label}.dhat.json')
    profile = {'source': 'dhat, allocation counts only; raw time/lifetime metadata stays in work/',
               'frames': raw['ftbl'],
               'points': [{'bytes': x['tb'], 'allocations': x['tbk'], 'frames': x['fs']} for x in raw['pps']]}
    Path(str(target) + '.allocations.json').write_text(json.dumps(profile, indent=2) + '\n')


def main():
    p = argparse.ArgumentParser(__doc__)
    p.add_argument('--output', default='benchmarks/phase1-lite')
    p.add_argument('--fixtures-only', action='store_true')
    p.add_argument('--pairs-only', action='store_true')
    p.add_argument('--no-build', action='store_true')
    a = p.parse_args()
    out = ROOT / a.output
    out.mkdir(parents=True, exist_ok=True)
    scratch = ROOT / 'work/phase1-lite'
    scratch.mkdir(parents=True, exist_ok=True)
    env = {k: v for k, v in os.environ.items() if not k.startswith('FXP_')}
    env.update(FXP_JWT_SECRET='phase1-local-only-0123456789abcdef0123456789', FXP_JWT_ISSUER='phase1',
               FXP_JWT_AUDIENCE='phase1', FXP_TLS_REQUIRED='false', FXP_CLIENT_TLS='false',
               FXP_GW_RATE='1000000', FXP_GW_RATE_BURST='1000000', GOMAXPROCS='1',
               TOKIO_WORKER_THREADS='1', RUST_LOG='error', FXP_LOG_LEVEL='error')
    if not a.no_build:
        run(['bash', 'scripts/gen_proto.sh'], env=env, log=scratch/'proto.log')
        run(['cargo','build','--release','--locked','--manifest-path','fxp_core/Cargo.toml','--bin','fxp_core'],env=env,log=scratch/'core-build.log')
        for name, package in [('gateway','.'),('bench','./benchmark')]:
            run(['go','build','-o',scratch/name,package],env=env,cwd=ROOT/'go_gateway',log=scratch/f'{name}-build.log')
        run(['go','test','-tags','phase1','-c','-o',scratch/'gateway.test','./transport'],env=env,cwd=ROOT/'go_gateway',log=scratch/'go-test-build.log')
        data=run(['cargo','test','--release','--locked','--manifest-path','fxp_core/Cargo.toml','--features','phase1','--lib','--no-run','--message-format=json'],env=env,log=scratch/'core-test-build.log')
        executables=[j['executable'] for line in data.splitlines() if line.startswith(b'{') for j in [json.loads(line)] if j.get('reason')=='compiler-artifact' and j.get('executable') and j.get('profile',{}).get('test')]
        (scratch/'core-test-path').write_text(executables[-1])
    core_test=Path((scratch/'core-test-path').read_text()) if not a.pairs_only else None
    if not a.pairs_only:
        for n in [0,1,10,100]:
            # Gateway has ONE core MD connection, regardless of external readers.
            for label, core_n in [(f'core-gateway-{n}', int(n>0)), (f'core-direct-{n}', n)]:
                target=out/f'{label}.json'
                run([core_test,'--ignored','--exact','server::phase1::core_attribution','--test-threads=1'],env=dict(env,FXP_PHASE1_SUBSCRIBERS=str(core_n),FXP_PHASE1_OUTPUT=str(target)),log=scratch/f'{label}.log')
                save_dhat_counts(target,scratch,label)
            target=out/f'gateway-{n}.json'
            trace=scratch/f'gateway-{n}.strace'
            run(['strace','-f','-yy','-xx','-s','8','-e','trace=write,writev,sendto,sendmsg','-o',trace,scratch/'gateway.test','-test.run','^TestPhase1Attribution$','-test.memprofile',scratch/f'gateway-{n}.allocs.pprof','-test.memprofilerate','1','-test.mutexprofile',scratch/f'gateway-{n}.mutex.pprof','-test.mutexprofilefraction','1'],env=dict(env,FXP_PHASE1_SUBSCRIBERS=str(n),FXP_PHASE1_OUTPUT=str(target)),log=scratch/f'gateway-{n}.log')
            data=json.loads(target.read_text());data['kernel']=syscall_counts(trace)
            data['kernel']['updates_including_warmup']=2128
            data['kernel']['delivered_including_warmup']=2128*n
            target.write_text(json.dumps(data,indent=2)+'\n')
            with gzip.open(out/f'gateway-{n}.strace.gz','wb') as f:f.write(trace.read_bytes())
            for index in ['alloc_objects','alloc_space']:
                run(['go','tool','pprof','-top',f'-sample_index={index}','-nodefraction=0',scratch/f'gateway-{n}.allocs.pprof'],env=env,log=out/f'gateway-{n}.{index}.txt')
                profile=out/f'gateway-{n}.{index}.txt';profile.write_text(''.join(line for line in profile.read_text().splitlines(keepends=True) if not line.startswith(('Time:', 'Duration:'))))
            run(['go','tool','pprof','-top','-sample_index=contentions','-nodefraction=0',scratch/f'gateway-{n}.mutex.pprof'],env=env,log=out/f'gateway-{n}.locks.txt')
            profile=out/f'gateway-{n}.locks.txt';profile.write_text(''.join(line for line in profile.read_text().splitlines(keepends=True) if not line.startswith(('Time:', 'Duration:'))))
        run([core_test,'--ignored','--exact','server::phase1::core_slow_consumer','--test-threads=1'],env=dict(env,FXP_PHASE1_OUTPUT=str(out/'core-slow.json')),log=scratch/'core-slow.log')
        save_dhat_counts(out/'core-slow.json',scratch,'core-slow')
        run([scratch/'gateway.test','-test.run','^TestPhase1SlowConsumer$'],env=dict(env,FXP_PHASE1_OUTPUT=str(out/'gateway-slow.json')),log=scratch/'gateway-slow.log')
    if not a.fixtures_only:
        cpus=sorted(os.sched_getaffinity(0))
        if len(cpus)<3: raise RuntimeError('three CPUs required for the recorded layout; choose an adapted layout on this machine')
        core_cpu, gateway_cpu, generator_cpu=cpus[:3]
        sub_cpu=cpus[3] if len(cpus)>3 else core_cpu
        layout=f'core={core_cpu}, gateway={gateway_cpu}, generator={generator_cpu}, separate subscriber={sub_cpu}; only {len(cpus)} visible CPUs; GOMAXPROCS=1 TOKIO_WORKER_THREADS=1'
        pairs=[]
        for pair in range(1,6):
            values={}
            for separate,label in [(False,'A'),(True,'B')]:
                ensure_ports_free([8080,8081,8082])
                name=f'pair-{pair}-{label}'
                children=[];logs=[]
                try:
                    for binary,cpu,port,component in [(ROOT/'fxp_core/target/release/fxp_core',core_cpu,8080,'core'),(scratch/'gateway',gateway_cpu,8082,'gateway')]:
                        trace=scratch/f'{name}-{component}.strace'
                        log=open(scratch/f'{name}-{component}.log','wb');logs.append(log)
                        command=['taskset','-c',str(cpu),str(binary)]
                        # Trace only a separate smoke on pair 1; all paired timing is untraced.
                        child=subprocess.Popen(command,env=env,cwd=ROOT,stdout=log,stderr=subprocess.STDOUT)
                        children.append(child);wait_port(port,child)
                    report=scratch/f'{name}.json'
                    cmd=['taskset','-c',str(generator_cpu),scratch/'bench','--mode','closed','--pacer','sleep','--concurrency','1','--orders','2000','--warmup','100','--subscribers','100','--subscriber-drain-updates','4000','--repetitions','1','--pinning',layout,'--json',report,'--markdown',scratch/f'{name}.md']
                    if separate:cmd+=['--subscriber-process','--subscriber-cpu',str(sub_cpu)]
                    run(cmd,env=env,log=scratch/f'{name}-client.log')
                    data=json.loads(report.read_text());r=data['runs'][0]
                    if data.get('error') or r['orders']!=2000 or r['market_data_updates_received']!=400000: raise RuntimeError('incomplete paired workload')
                    values[label]=r['orders_per_second']
                    # Public artifacts contain no absolute elapsed or throughput values.
                finally:
                    for child in reversed(children):
                        child.terminate()
                        try:child.wait(timeout=20)
                        except subprocess.TimeoutExpired:child.kill();child.wait();raise
                    for log in logs:log.close()
            pairs.append({'pair':pair,'B_over_A':values['B']/values['A']})
        ratios=[x['B_over_A'] for x in pairs]
        (out/'contention.json').write_text(json.dumps({'label':'relative, shared hardware','layout':layout,'order':'A,B repeated five times, fresh stack per run','subscribers':100,'orders':2000,'pairs':pairs,'median':statistics.median(ratios),'min':min(ratios),'max':max(ratios)},indent=2)+'\n')
    metadata={'source_commit':run(['git','rev-parse','HEAD']).decode().strip(),'working_tree_dirty':bool(run(['git','status','--porcelain']).strip()),'rust':run(['rustc','--version']).decode().strip(),'go':run(['go','version']).decode().strip(),'build':'release; isolated Rust test fixture uses phase1/dhat; Go fixtures use phase1 tag; paired live processes have neither','durability':'event log and recording disabled','absolute_timing':'smoke only; retained under ignored work/, not results','cpu_max':Path('/sys/fs/cgroup/cpu.max').read_text().strip(),'cpu':next(x.split(':',1)[1].strip() for x in Path('/proc/cpuinfo').read_text().splitlines() if x.startswith('model name'))}
    (out/'environment.json').write_text(json.dumps(metadata,indent=2)+'\n')
    print('PASS phase1 fixtures/paired experiment; absolute timings remain under work/')

if __name__=='__main__':main()

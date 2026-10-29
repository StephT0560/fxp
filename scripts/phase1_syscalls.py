#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Live production binaries, syscall counts only; strace elapsed is not a result."""
import gzip
import json
import os
from pathlib import Path
import signal
import subprocess
from phase1_lite import ROOT, run, syscall_counts
from profile_guard import ensure_ports_free, wait_port


def collect(out):
    scratch=ROOT/'work/phase1-lite'
    env={k:v for k,v in os.environ.items() if not k.startswith('FXP_')}
    env.update(FXP_JWT_SECRET='phase1-local-only-0123456789abcdef0123456789',FXP_JWT_ISSUER='phase1',FXP_JWT_AUDIENCE='phase1',FXP_TLS_REQUIRED='false',FXP_CLIENT_TLS='false',FXP_GW_RATE='1000000',FXP_GW_RATE_BURST='1000000',GOMAXPROCS='1',TOKIO_WORKER_THREADS='1',RUST_LOG='error',FXP_LOG_LEVEL='error')
    results=[]
    for n in [0,1,10,100]:
        ensure_ports_free([8080,8081,8082]);children=[];logs=[]
        try:
            for component,binary,port in [('core',ROOT/'fxp_core/target/release/fxp_core',8080),('gateway',scratch/'gateway',8082)]:
                log=open(scratch/f'kernel-{n}-{component}.log','wb');logs.append(log)
                command=['strace','-f','-yy','-xx','-s','8','-e','trace=write,writev,sendto,sendmsg','-o',str(scratch/f'kernel-{n}-{component}.strace'),str(binary)]
                child=subprocess.Popen(command,env=env,cwd=ROOT,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
                children.append(child);wait_port(port,child)
            run([scratch/'bench','--mode','closed','--pacer','sleep','--concurrency','1','--orders','2000','--warmup','100','--subscribers',str(n),'--subscriber-drain-updates','4000','--repetitions','1','--json',scratch/f'kernel-{n}-bench.json','--markdown',scratch/f'kernel-{n}-bench.md'],env=env,log=scratch/f'kernel-{n}-client.log')
        finally:
            for child in reversed(children):
                os.killpg(child.pid,signal.SIGTERM)
                try:child.wait(timeout=20)
                except subprocess.TimeoutExpired:os.killpg(child.pid,signal.SIGKILL);child.wait();raise
            for log in logs:log.close()
        result={'subscribers':n,'scope':'live 2000 orders, 100 warmup; syscall smoke only, no elapsed-time result'}
        for component in ['core','gateway']:
            trace=scratch/f'kernel-{n}-{component}.strace'
            result[component]=syscall_counts(trace)
            with gzip.open(out/f'live-{n}-{component}.strace.gz','wb') as f:f.write(trace.read_bytes())
        results.append(result)
    (out/'live-syscalls.json').write_text(json.dumps(results,indent=2)+'\n')

if __name__=='__main__':
    import argparse
    p=argparse.ArgumentParser(__doc__);p.add_argument('--output',default='benchmarks/phase1-lite');a=p.parse_args();collect(ROOT/a.output)

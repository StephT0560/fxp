#!/usr/bin/env python3
"""Pinned, fresh-stack round-4 experiments. Raw failed runs are retained too."""
import argparse, gzip, json, os, pathlib, resource, signal, socket, subprocess, time
ROOT = pathlib.Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument('--output', default='benchmarks/round4')
p.add_argument('--suite', choices=['pacing','capacity','hops','profiles','coverage','fanout','pacer-traces','rust-profiles','ab'], required=True)
p.add_argument('--core-binary',default=str(ROOT/'fxp_core/target/release/fxp_core'))
p.add_argument('--perf-binary')
p.add_argument('--dhat',action='store_true')
p.add_argument('--label',default='baseline')
a = p.parse_args()
out = ROOT / a.output
out.mkdir(parents=True, exist_ok=True)
bins = ROOT / 'work/r4-profile'
base = dict(os.environ, FXP_JWT_SECRET='dev-secret-change-in-production', FXP_GW_RATE='1000000', FXP_GW_RATE_BURST='1000000', GOMAXPROCS='1', TOKIO_WORKER_THREADS='1', RUST_LOG='error', FXP_LOG_LEVEL='error')
for key in ['FXP_EVENT_LOG','FXP_SESSION_SCHEDULE','FXP_PROFILE_DIR','FXP_TRACE_FILE','FXP_HOP_TRACE']:
    base.pop(key, None)
layout = 'core=0 gateway=1 generator=2; GOMAXPROCS=1 TOKIO_WORKER_THREADS=1; shared cgroup quota 2 CPUs'
def ready(port):
    for _ in range(100):
        try:
            with socket.create_connection(('127.0.0.1',port),.1): return
        except OSError: time.sleep(.05)
    raise RuntimeError(f'port {port} not ready')
def cpu(pid):
    fields = pathlib.Path(f'/proc/{pid}/stat').read_text().split()
    return sum(map(int, fields[13:15])) / os.sysconf('SC_CLK_TCK')
def scenario(name,mode,c,rate,symbols,pacer='sleep',hops=False,profiles=False,fix_mode=None,persistence=False,subscribers=0,orders=1000):
    procs=[]; logs=[]; components=[]; sampler=None
    scratch=bins/name
    import shutil
    shutil.rmtree(scratch,ignore_errors=True);scratch.mkdir()
    try:
        launch=[('core',0,pathlib.Path(a.core_binary),8080),('gateway',1,bins/'gateway',8082)]
        if fix_mode is not None: launch.append(('bridge',1,bins/'bridge',8083))
        for component,cpuid,exe,port in launch:
            components.append(component)
            log=open(out/f'{name}-{component}.txt','w'); logs.append(log)
            env=base.copy()
            if component=='core' and a.dhat:env['FXP_DHAT_FILE']=str(out/f'{name}-dhat.json')
            if component=='core' and persistence: env['FXP_EVENT_LOG']=str(scratch/'events')
            if component=='bridge':
                env['FXP_FIX_STORE_DIR']=str(scratch/'store') if fix_mode!='memory' else ''
                env['FXP_FIX_STORE_FSYNC']=fix_mode if fix_mode!='memory' else 'async'
                env['FXP_FIX_CREDENTIALS']=','.join(f'BENCH{r}_{i}:benchmark-password' for r in range(5) for i in range(c))
            if hops: env['FXP_HOP_TRACE']=str(out/f'{name}-{component}-hops.txt')
            if profiles and component in ['gateway','bridge']:
                env['FXP_PROFILE_DIR']=str(out/f'{name}-{component}-profiles')
                if mode=='open': env['FXP_TRACE_FILE']=str(out/f'{name}-{component}.trace')
            procs.append(subprocess.Popen(['taskset','-c',str(cpuid),str(exe)],cwd=ROOT,env=env,stdout=log,stderr=subprocess.STDOUT))
            ready(port)
        if a.perf_binary:
            log=open(out/f'{name}-perf.txt','w');logs.append(log)
            sampler=subprocess.Popen([a.perf_binary,'record','-e','cpu-clock:u','-F','99','--call-graph','dwarf,8192','-p',str(procs[0].pid),'-o',str(scratch/'core.perf')],stdout=log,stderr=subprocess.STDOUT)
        before={str(x.pid):cpu(x.pid) for x in procs}
        throttle_before=pathlib.Path('/sys/fs/cgroup/cpu.stat').read_text()
        start=time.monotonic()
        cmd=['taskset','-c','2',str(bins/'bench'),'--mode',mode,'--pacer',pacer,'--concurrency',str(c),'--rate',str(rate),'--symbols',symbols,'--orders',str(orders),'--warmup','100','--repetitions','5','--pinning',layout,'--json',str(out/f'{name}.json'),'--markdown',str(out/f'{name}.md')]
        env=base.copy()
        if hops: env['FXP_HOP_TRACE']=str(out/f'{name}-client-hops.txt')
        if profiles and mode=='open':env['FXP_TRACE_FILE']=str(out/f'{name}-client.trace')
        if fix_mode is not None:
            cmd=['taskset','-c','2','python3',str(ROOT/'scripts/bench_fix.py'),'--concurrency',str(c),'--output',str(out/f'{name}.json')]
        else:cmd += ['--subscribers',str(subscribers)]
        usage_before=resource.getrusage(resource.RUSAGE_CHILDREN)
        result=subprocess.run(cmd,cwd=ROOT,env=env,capture_output=True,text=True)
        usage_after=resource.getrusage(resource.RUSAGE_CHILDREN)
        elapsed=time.monotonic()-start
        meta={'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),'working_tree_dirty':bool(subprocess.check_output(['git','status','--porcelain'],cwd=ROOT,text=True).strip()),'command':cmd,'returncode':result.returncode,'stdout':result.stdout,'stderr':result.stderr,'elapsed_seconds':elapsed,'generator_cpu_seconds':usage_after.ru_utime+usage_after.ru_stime-usage_before.ru_utime-usage_before.ru_stime,'process_cpu_seconds':{n:cpu(proc.pid)-before[str(proc.pid)] for n,proc in zip(components,procs)},'cgroup_before':throttle_before,'cgroup_after':pathlib.Path('/sys/fs/cgroup/cpu.stat').read_text()}
        (out/f'{name}-execution.json').write_text(json.dumps(meta,indent=2)+'\n')
        print(name, result.returncode, flush=True)
    finally:
        if sampler is not None:
            sampler.send_signal(signal.SIGINT);sampler.wait(timeout=20)
            with open(out/f'{name}-core-stacks.txt','w') as dest:
                subprocess.run([a.perf_binary,'script','-i',str(scratch/'core.perf')],stdout=dest,stderr=subprocess.DEVNULL)
            with open(out/f'{name}-core-perf-report.txt','w') as dest:
                subprocess.run([a.perf_binary,'report','--stdio','--no-children','-i',str(scratch/'core.perf')],stdout=dest,stderr=subprocess.DEVNULL)
        for proc in reversed(procs): proc.send_signal(signal.SIGTERM)
        for proc in procs:
            try: proc.wait(timeout=10)
            except subprocess.TimeoutExpired: proc.kill(); proc.wait()
        for log in logs: log.close()
        for path in out.glob(name+'*'):
            if path.suffix in ['.json','.trace'] and '-execution' not in path.name or '-hops.txt' in path.name or '-core-stacks.txt' in path.name:
                with path.open('rb') as src, gzip.open(str(path)+'.gz','wb') as dst: dst.write(src.read())
                path.unlink()
if a.suite=='pacing':
    for pacer in ['sleep','busy-spin','hybrid']:
        scenario(f'open-c1-{pacer}','open',1,1000,'single',pacer)
    scenario('closed-c1','closed',1,1000,'single')
    scenario('open-c10','open',10,1000,'spread')
elif a.suite=='hops':
    for name,mode,c in [('open-c1','open',1),('open-c10','open',10),('closed-c1','closed',1)]:
        scenario('hops-'+name,mode,c,1000,'single' if c==1 else 'spread',hops=True)
elif a.suite=='fanout':
    for n in [0,10,100]:scenario(f'fanout-{n}','closed',10,1000,'single',subscribers=n)
elif a.suite=='pacer-traces':
    for mode in ['sleep','busy-spin']:scenario('trace-'+mode,'open',1,1000,'single',mode,profiles=True)
elif a.suite=='rust-profiles':
    scenario(a.label+'-closed-c50','closed',50,1000,'spread',profiles=True)
    scenario(a.label+'-below-knee','open',10,2000,'spread',profiles=True,orders=10000)
elif a.suite=='ab':
    scenario(a.label+'-closed-c50','closed',50,1000,'spread')
    scenario(a.label+'-below-knee','open',10,2000,'spread',orders=10000)
elif a.suite=='coverage':
    scenario('persist-core','closed',1,1000,'single',persistence=True)
    for n in [0,10,100]:scenario(f'fanout-{n}','closed',10,1000,'single',subscribers=n)
    for persist in [False,True]:
        for mode in ['memory','strict','group','async']:
            scenario(f'fix-{mode}-corelog-{persist}','closed',1,1000,'single',fix_mode=mode,persistence=persist)
    scenario('profile-fix-c50','closed',50,1000,'single',fix_mode='async',profiles=True)
elif a.suite=='profiles':
    scenario('profile-open-c1','open',1,1000,'single',profiles=True)
    scenario('profile-closed-c50','closed',50,1000,'spread',profiles=True)
else:
    for symbols in ['single','spread']:
        for c in [1,10,50,100]: scenario(f'closed-{symbols}-c{c}','closed',c,1000,symbols)
        # Per-connection --rate; ten sessions share the aggregate offered rate.
        for rate in [5000,10000,20000,40000,80000]: scenario(f'open-{symbols}-r{rate}','open',10,rate//10,symbols,orders=max(1000,rate//10*5))

#!/usr/bin/env python3
"""Join local CLOCK_MONOTONIC side-channel events; never subtract quantiles."""
import argparse,gzip,json,pathlib,statistics
STAGES=['client_send','gateway_receive','gateway_core_write','core_receive','engine_start','engine_end','core_write','gateway_core_receive','gateway_client_write','client_receive']
def percentile(values,q):
    values=sorted(values)
    # Explicit nearest-rank percentile; unlike the harness's HDR these are raw ns.
    import math
    return values[max(0,math.ceil(len(values)*q)-1)]
def collect(paths):
    orders={}
    for path in paths:
        with gzip.open(path,'rt') as f:
            for line in f:
                if line.startswith('{'): e=json.loads(line)
                else:
                    stage,oid,ns=line.rstrip('\n').split('\t');e={'stage':stage,'id':json.loads(oid),'ns':int(ns)}
                oid=e['id'].split('::',1)[-1]
                if '-measured-' not in oid:continue
                events=orders.setdefault(oid,{})
                if e['stage'] in events:raise ValueError(f'duplicate {oid} {e["stage"]}')
                events[e['stage']]=e['ns']
    runs={}; incomplete=0
    for oid,events in orders.items():
        if not all(s in events for s in STAGES):incomplete+=1;continue
        run=oid.split('-measured-')[0]; samples=runs.setdefault(run,{})
        for a,b in zip(STAGES,STAGES[1:]):
            delta=events[b]-events[a]
            if delta<0:raise ValueError(f'clock/order violation {a}->{b}: {delta}')
            samples.setdefault(a+' -> '+b,[]).append(delta)
    summary={}
    for hop in next(iter(runs.values()),{}):
        summary[hop]={}
        for name,q in [('p50',.5),('p99',.99)]:
            values=[percentile(samples[hop],q) for samples in runs.values()]
            summary[hop][name+'_ns']={'median':statistics.median(values),'min':min(values),'max':max(values)}
    return {'complete_orders':sum(len(next(iter(s.values()))) for s in runs.values()),'incomplete_orders':incomplete,'repetitions':len(runs),'summary':summary}
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('prefix');a=p.parse_args();prefix=pathlib.Path(a.prefix)
    result=collect(prefix.parent.glob(prefix.name+'-*-hops.txt.gz'))
    prefix.with_name(prefix.name+'-attribution.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2))

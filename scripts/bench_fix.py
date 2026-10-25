#!/usr/bin/env python3
"""ACK-controlled FIX fixture. No test-client sleeps; dictionary stays enabled."""
import argparse,concurrent.futures,importlib.util,json,pathlib,socket,statistics,time
ROOT=pathlib.Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('fixclient',ROOT/'fix_test_client/fix_client.py');fix=importlib.util.module_from_spec(spec);spec.loader.exec_module(fix)
def quantile(values,q):
 import math
 return sorted(values)[max(0,math.ceil(len(values)*q)-1)]
def worker(rep,worker_id,orders,warmup):
 sock=socket.create_connection(('127.0.0.1',8083),timeout=30);sock.settimeout(30)
 sender=f'BENCH{rep}_{worker_id}';seq=1
 def send(kind,fields):
  nonlocal seq
  sock.sendall(fix.build_message(kind,sender,'FXPBRIDGE',seq,fields));seq+=1
 try:
  send('A',[(98,'0'),(108,'30'),(141,'Y'),(554,'benchmark-password')])
  if fix.read_message(sock).get(35)!='A':raise RuntimeError('logon rejected')
  def phase(label,count):
   ack=[];fill=[];pending={};seen_ack=set();seen_fill=set();start=time.monotonic_ns()
   def receive():
    msg=fix.read_message(sock);now=time.monotonic_ns()
    if msg is None:raise RuntimeError('disconnect')
    if msg.get(35)=='1':send('0',[(112,msg[112])]);return
    if msg.get(35)=='0':return
    if msg.get(35)!='8' or msg.get(150) not in ['0','F']:raise RuntimeError(f'unexpected FIX reply: {msg}')
    oid=msg[11]
    if oid not in pending:raise RuntimeError('uncorrelated report')
    if msg[150]=='0':
     if oid in seen_ack:raise RuntimeError('duplicate ACK')
     seen_ack.add(oid);ack.append(now-pending[oid])
    elif msg.get(39)=='2':
     if oid in seen_fill:raise RuntimeError('duplicate fill')
     seen_fill.add(oid);fill.append(now-pending[oid])
   for i in range(count):
    oid=f'{sender}-{label}-{i}';pending[oid]=time.monotonic_ns()
    send('D',[(11,oid),(55,'FIXBENCH'),(54,'1' if i%2==0 else '2'),(38,'1'),(40,'2'),(59,'1'),(44,'3000.00'),(60,fix.utc_now())])
    while oid not in seen_ack:receive()
   while len(seen_fill)<count:receive()
   return {'ack_ns':ack,'fill_ns':fill,'elapsed_ns':time.monotonic_ns()-start}
  phase('warmup',warmup)
  result=phase('measured',orders)
  send('5',[(58,'benchmark complete')])
  while fix.read_message(sock).get(35)!='5':pass
  return result
 finally:sock.close()
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--output',required=True);p.add_argument('--concurrency',type=int,default=1);p.add_argument('--orders',type=int,default=1000);p.add_argument('--warmup',type=int,default=100);p.add_argument('--repetitions',type=int,default=5);a=p.parse_args()
 if a.orders%2 or a.warmup%2:raise ValueError('paired even order counts required')
 result={'config':vars(a),'runs':[]}
 try:
  for rep in range(a.repetitions):
   with concurrent.futures.ThreadPoolExecutor(max_workers=a.concurrency) as pool:
    runs=list(pool.map(lambda i:worker(rep,i,a.orders,a.warmup),range(a.concurrency)))
   ack=[x for r in runs for x in r['ack_ns']];fills=[x for r in runs for x in r['fill_ns']]
   result['runs'].append({'ack_ns':ack,'fill_ns':fills,'ack_p50_ns':quantile(ack,.5),'ack_p99_ns':quantile(ack,.99),'completed_orders_per_second':len(ack)*1e9/max(r['elapsed_ns'] for r in runs)})
  result['summary']={k:{'median':statistics.median(r[k] for r in result['runs']),'min':min(r[k] for r in result['runs']),'max':max(r[k] for r in result['runs'])} for k in ['ack_p50_ns','ack_p99_ns','completed_orders_per_second']}
 except Exception as err:result['error']=str(err);raise
 finally:pathlib.Path(a.output).write_text(json.dumps(result,indent=2)+'\n')

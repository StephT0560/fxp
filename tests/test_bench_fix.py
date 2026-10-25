import importlib.util
import socket
import threading
import unittest
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('bench_fix','scripts/bench_fix.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class FixBenchmarkTest(unittest.TestCase):
 def test_open_counts_ack_and_terminal_fills_separately(self):
  listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen();address=listener.getsockname()
  errors=[]
  def server():
   try:
    conn,_=listener.accept();seq=1;previous=None
    with conn:
     def send(kind,fields):
      nonlocal seq
      conn.sendall(m.fix.build_message(kind,'FXPBRIDGE','BENCH0_0',seq,fields));seq+=1
     while True:
      msg=m.fix.read_message(conn)
      if msg is None:break
      if msg[35]=='A':send('A',[(98,'0'),(108,'30')])
      elif msg[35]=='5':send('5',[]);break
      else:
       oid=msg[11];send('8',[(11,oid),(150,'0'),(39,'0')])
       if previous is None:previous=oid
       else:
        for target in [previous,oid]:send('8',[(11,target),(150,'F'),(39,'2')])
        previous=None
   except Exception as err:errors.append(err)
  thread=threading.Thread(target=server);thread.start();connect=socket.create_connection
  common={};barrier=threading.Barrier(1,action=lambda:common.update(start=m.time.monotonic_ns()+1_000_000))
  try:
   with patch.object(m.socket,'create_connection',side_effect=lambda *args,**kwargs:connect(address,timeout=2)):
    result=m.worker(0,0,4,2,'open',1000,barrier,common)
   self.assertEqual(len(result['ack_ns']),4);self.assertEqual(len(result['fill_ns']),4)
   self.assertGreaterEqual(result['max_send_lag_ns'],0)
  finally:listener.close();thread.join(timeout=3)
  self.assertFalse(thread.is_alive());self.assertFalse(errors)
if __name__=='__main__':unittest.main()

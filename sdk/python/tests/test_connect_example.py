# SPDX-License-Identifier: MIT
"""Execute the documented TLS connect example; never retry orders or bad trust."""
from pathlib import Path
import ssl
import unittest
from unittest.mock import Mock, patch
from fxp_client import Client

class ConnectExample(unittest.TestCase):
    def program(self):
        text=(Path(__file__).resolve().parents[3]/'docs/bot-workflow.md').read_text()
        return text.split('For a TLS gateway',1)[1].split('```python\n',1)[1].split('```',1)[0]

    def test_transient_logon_retries_three_times(self):
        client=Mock()
        with patch.object(Client,'connect',side_effect=[PermissionError('synthetic transient'),ssl.SSLEOFError('synthetic transient'),client]) as connect,patch('ssl.create_default_context',return_value=object()),patch('time.sleep') as sleep:
            exec(self.program(),{'Client':Client,'token':'[REDACTED]'})
        self.assertEqual(connect.call_count,3)
        self.assertEqual([call.args for call in sleep.call_args_list],[(1,),(1,)])
        client.close.assert_called_once()

    def test_bad_certificate_is_not_retried(self):
        with patch.object(Client,'connect',side_effect=ssl.SSLCertVerificationError('synthetic trust failure')) as connect,patch('ssl.create_default_context',return_value=object()),patch('time.sleep') as sleep:
            with self.assertRaises(ssl.SSLCertVerificationError):exec(self.program(),{'Client':Client,'token':'[REDACTED]'})
        self.assertEqual(connect.call_count,1);sleep.assert_not_called()

    def test_successful_retry_preserves_six_second_idle_receive(self):
        from fxp_client import pb
        from fxp_client.client import encode_frame
        class Socket:
            def __init__(self,timeout):
                self.timeout=timeout;self.idled=False
                self.data=encode_frame(pb.FXPMessage(logon_ack=pb.LogonAck(accepted=True)))
            def setsockopt(self,*args):pass
            def sendall(self,data):pass
            def close(self):pass
            def gettimeout(self):return self.timeout
            def recv(self,n):
                if not self.data and not self.idled:
                    self.idled=True
                    if self.timeout<6:raise TimeoutError('synthetic six-second idle')
                    self.data=encode_frame(pb.FXPMessage(heartbeat=pb.Heartbeat()))
                data,self.data=self.data[:n],self.data[n:];return data
        original=Client.connect;attempts=[]
        def connect(*args,**kwargs):
            attempts.append(1)
            if len(attempts)==1:raise PermissionError('synthetic transient')
            client=original(*args,**kwargs)
            self.assertEqual(client.recv().kind,'heartbeat')
            return client
        context=Mock();context.wrap_socket.side_effect=lambda sock,**kw:sock
        with patch.object(Client,'connect',side_effect=connect),patch('socket.create_connection',side_effect=lambda addr,timeout:Socket(timeout)),patch('ssl.create_default_context',return_value=context),patch('time.sleep'):
            exec(self.program(),{'Client':Client,'token':'[REDACTED]'})
        self.assertEqual(len(attempts),2)

# SPDX-License-Identifier: MIT
"""Run by FXP_REPRO_TEST_CLIENT=1 make repro-demo against its isolated local stack."""
import os
import unittest
from fxp_client import Client, dev_token, pb


@unittest.skipUnless(os.environ.get('FXP_REPRO_TEST_CLIENT') == '1', 'requires local demo stack')
class IntegrationTests(unittest.TestCase):
    def connect(self, owner):
        return Client.connect('127.0.0.1', int(os.environ.get('FXP_REPRO_TCP_PORT', '8082')), dev_token(owner))

    def wait(self, client, kind, **fields):
        for _ in range(100):
            event = client.recv()
            self.assertIsNone(event.trade_gap)
            if event.kind == 'error_message': self.fail(event.message.error_message.error_message)
            body = getattr(event.message, event.kind)
            if event.kind == kind and all(getattr(body, key) == value for key, value in fields.items()):
                return body
        self.fail('expected event not observed')

    def test_auth_orders_subscribe_trade_replace_cancel_mass_cancel(self):
        with self.assertRaises(PermissionError):
            Client.connect('127.0.0.1', int(os.environ.get('FXP_REPRO_TCP_PORT', '8082')), 'invalid-jwt')
        with self.connect('sdk-maker') as maker, self.connect('sdk-taker') as taker:
            maker.new('sdk-ask', 'SDKTEST', pb.SELL, 10, 10000)
            self.wait(maker, 'execution_report', order_id='sdk-ask', execution_type=pb.EXEC_NEW)
            maker.subscribe('SDKTEST')
            self.wait(maker, 'market_data_snapshot', symbol='SDKTEST')
            taker.new('sdk-buy', 'SDKTEST', pb.BUY, 4, 10000)
            self.wait(taker, 'execution_report', order_id='sdk-buy', execution_type=pb.EXEC_FILLED)
            fill = self.wait(maker, 'execution_report', order_id='sdk-ask', execution_type=pb.EXEC_FILLED)
            self.assertEqual((fill.executed_quantity, fill.leaves_quantity), (4, 6))
            trade = self.wait(maker, 'trade_capture_report', symbol='SDKTEST')
            self.assertEqual((trade.seq, trade.trade_quantity, trade.buy_order_id, trade.sell_order_id), (1, 4, '', ''))
            maker.replace('sdk-ask', 'sdk-ask-r', 'SDKTEST', 'sdk-replace', new_price=10100, new_quantity=10, new_display_quantity=0)
            self.wait(maker, 'execution_report', order_id='sdk-ask', execution_type=pb.EXEC_REPLACED)
            replaced = self.wait(maker, 'execution_report', order_id='sdk-ask-r', execution_type=pb.EXEC_NEW)
            self.assertEqual((replaced.cum_quantity, replaced.leaves_quantity, replaced.order_quantity), (4, 6, 10))
            maker.cancel('sdk-ask-r', 'SDKTEST', 'sdk-cancel')
            self.wait(maker, 'execution_report', order_id='sdk-ask-r', execution_type=pb.EXEC_CANCELED)
            for i in range(2):
                maker.new(f'sdk-rest-{i}', 'SDKTEST', pb.BUY, 1, 9900)
                self.wait(maker, 'execution_report', order_id=f'sdk-rest-{i}', execution_type=pb.EXEC_NEW)
            maker.mass_cancel('sdk-mass', symbol='SDKTEST')
            report = self.wait(maker, 'order_mass_cancel_report', client_request_id='sdk-mass')
            self.assertEqual(report.total_affected, 2)

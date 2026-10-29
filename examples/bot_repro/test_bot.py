# SPDX-License-Identifier: MIT
import unittest
from fxp_client import pb
from bot import MarketMaker


class BotTests(unittest.TestCase):
    def test_documented_bug_sends_leaves_as_total_after_partial_fill(self):
        class Gateway:
            def replace(self, *args, **kwargs): self.request = (args, kwargs)
        gateway = Gateway()
        bot = MarketMaker(gateway, 'TEST', 'quote')
        bot.observe(pb.ExecutionReport(order_id='quote-ask', execution_type=pb.EXEC_NEW, leaves_quantity=10))
        bot.observe(pb.ExecutionReport(order_id='quote-ask', execution_type=pb.EXEC_FILLED,
                                      executed_quantity=7, leaves_quantity=3, cum_quantity=7))
        bot.reprice_ask()
        self.assertEqual(gateway.request[1]['new_quantity'], 3)  # deliberate bug, should be total 10

    def test_unfilled_quote_has_no_stale_quantity(self):
        class Gateway:
            def replace(self, *args, **kwargs): self.quantity = kwargs['new_quantity']
        gateway = Gateway()
        bot = MarketMaker(gateway, 'TEST', 'quote')
        bot.observe(pb.ExecutionReport(order_id='quote-ask', execution_type=pb.EXEC_NEW, leaves_quantity=10))
        bot.reprice_ask()
        self.assertEqual(gateway.quantity, 10)

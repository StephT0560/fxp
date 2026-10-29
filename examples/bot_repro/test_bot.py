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


class ScenarioTests(unittest.TestCase):
    def test_quote_barrier_accepts_ask_before_bid(self):
        from fxp_client import Event
        from scenario import wait_for_quotes
        class Feed:
            received = 0
            def events(self):
                for order in ('quote-ask', 'quote-bid'):
                    self.received += 1
                    yield Event(pb.FXPMessage(execution_report=pb.ExecutionReport(
                        order_id=order, execution_type=pb.EXEC_NEW, leaves_quantity=10)))
        feed = Feed()
        bot = MarketMaker(feed, 'TEST', 'quote')
        wait_for_quotes(feed, bot)
        self.assertEqual(feed.received, 2)
        self.assertEqual(bot.cached_leaves, 10)

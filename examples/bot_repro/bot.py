# SPDX-License-Identifier: MIT
"""Tiny two-sided quoter with ONE DELIBERATE BUG; do not use for real trading.

The bot records leaves correctly, but passes remaining quantity as the TOTAL
replacement quantity. In the supplied harness seed 7 fills 7/10 first: the bot
submits new_quantity=3, below the already filled 7, so the engine rejects it.
Seed 1 reprices without a fill and hides the defect. This is a teaching example,
not a production market-making strategy. Fix: send cumulative filled + desired
leaves (or zero to preserve quantity); reconcile after disconnect. FXP does not
replay bot decisions.
"""
from fxp_client import Client, pb


class MarketMaker:
    def __init__(self, client: Client, symbol: str, prefix: str):
        self.client, self.symbol, self.prefix = client, symbol, prefix
        self.ask_id = prefix + '-ask'
        self.cached_leaves = 0

    def quote(self):
        self.client.new(self.prefix + '-bid', self.symbol, pb.BUY, 10, 9900)
        self.client.new(self.ask_id, self.symbol, pb.SELL, 10, 10100)

    def observe(self, report: pb.ExecutionReport):
        if report.order_id == self.ask_id and report.execution_type in (pb.EXEC_NEW, pb.EXEC_FILLED):
            self.cached_leaves = report.leaves_quantity
        # DELIBERATE BUG below: replacement OrderQty is total, not leaves.

    def reprice_ask(self):
        replacement = self.prefix + '-ask-r'
        self.client.replace(self.ask_id, replacement, self.symbol, self.prefix + '-reprice',
                            new_price=10200, new_quantity=self.cached_leaves)
        return replacement

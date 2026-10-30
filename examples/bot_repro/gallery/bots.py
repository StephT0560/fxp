# SPDX-License-Identifier: MIT
"""Deliberately broken state transitions; these are teaching variants, not bots to deploy."""
from fxp_client import pb


class SharedOwnerBot:
    inventory = 0

    def observe(self, report):
        # BUG: assumes a disappearing ask is always a sale, including STP cancel.
        if report.execution_type == pb.EXEC_CANCELED:
            self.inventory -= report.order_quantity


class LateReaderBot:
    hedges = 0

    def observe(self, report):
        # BUG: hedges a late fill, then treats its cancel reject as another fill.
        if report.execution_type in (pb.EXEC_FILLED, pb.EXEC_REJECTED):
            self.hedges += 1


class ContinuousBot:
    expected_price = 9900
    expected_phase = 'Continuous'

    def quote(self, client, order, quantity):
        # BUG: assumes this crosses immediately at the resting ask in continuous.
        client.new(order, 'BOTTEST', pb.BUY, quantity, 10200)

# SPDX-License-Identifier: MIT
"""Seeded stimulus; waits for acknowledgments instead of racing sleeps."""
import argparse
import json
import os
from pathlib import Path
import random
import time

from fxp_client import Client, dev_token, pb
from bot import MarketMaker


def until(client, predicate, observer=lambda _: None):
    deadline = time.monotonic() + 15
    for event in client.events():
        if time.monotonic() > deadline:
            raise TimeoutError('scenario response deadline')
        if event.trade_gap:
            raise AssertionError(f'unexpected trade gap: {event.trade_gap}')
        if event.kind == 'error_message':
            raise AssertionError(event.message.error_message.error_message)
        observer(event)
        if predicate(event):
            return event.message
    raise EOFError('gateway closed during scenario')


def run(seed, port, destination):
    quantity = 7 if random.Random(seed).randrange(2) else 0
    prefix = f'quote-{seed}'
    with Client.connect('127.0.0.1', port, dev_token('demo-maker')) as maker, \
         Client.connect('127.0.0.1', port, dev_token('demo-taker')) as taker:
        bot = MarketMaker(maker, 'BOTTEST', prefix)
        bot.quote()
        def observe(event):
            if event.kind == 'execution_report': bot.observe(event.message.execution_report)
        def report(order, kind):
            return lambda e: e.kind == 'execution_report' and e.message.execution_report.order_id == order and e.message.execution_report.execution_type == kind
        until(maker, report(bot.ask_id, pb.EXEC_NEW), observe)
        maker.subscribe('BOTTEST')
        until(maker, lambda e: e.kind == 'market_data_snapshot', observe)
        if quantity:
            taker.new(f'take-{seed}', 'BOTTEST', pb.BUY, quantity, 10100, time_in_force=pb.IOC)
            until(taker, report(f'take-{seed}', pb.EXEC_FILLED))
            filled = until(maker, report(bot.ask_id, pb.EXEC_FILLED), observe).execution_report
            assert (filled.cum_quantity, filled.leaves_quantity) == (7, 3)
        replacement = bot.reprice_ask()
        response = until(maker, report(bot.ask_id if quantity else replacement,
                         pb.EXEC_REJECTED if quantity else pb.EXEC_NEW), observe).execution_report
        if quantity:
            assert response.reject_reason == 'Replace quantity not above filled quantity'
        else:
            assert (response.leaves_quantity, response.order_quantity) == (10, 10)
        result = {'seed': seed, 'symbol': 'BOTTEST', 'owner': 'demo-maker',
                  'order_id': bot.ask_id, 'replacement_id': replacement,
                  'core_order_id': 'demo-maker::' + bot.ask_id,
                  'core_replacement_id': 'demo-maker::' + replacement,
                  'filled': quantity, 'leaves': 10 - quantity, 'requested_total': bot.cached_leaves,
                  'reject_reason': response.reject_reason, 'bug_reproduced': bool(quantity)}
        Path(destination).write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))
        # Keep no live quotes after the demonstration. Wait for the mass-cancel barrier.
        maker.mass_cancel('demo-cleanup', symbol='BOTTEST')
        cleanup = until(maker, lambda e: e.kind == 'order_mass_cancel_report').order_mass_cancel_report
        assert not cleanup.reject_reason and cleanup.total_affected == 2
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--seed', type=int, default=7)
    parser.add_argument('--result', required=True)
    args = parser.parse_args()
    run(args.seed, int(os.environ.get('FXP_REPRO_TCP_PORT', '8082')), args.result)

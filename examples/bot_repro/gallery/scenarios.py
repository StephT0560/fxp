# SPDX-License-Identifier: MIT
"""Real ACK/fill barriers, deterministic stimuli; no sleeps to order commands."""
import argparse
import contextlib
import io
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import random

from fxp_client import Client, dev_token, pb
from scenario import until, run as stale_replace
from gallery.bots import SharedOwnerBot, LateReaderBot, ContinuousBot

BUGS = ('stale-replace', 'stp-surprise', 'fill-cancel-race', 'auction-phase')


def operator_token():
    # Local-only role on the normal SDK's disposable token. The Go gateway
    # independently verifies the signature and operator claim in the integration.
    header, payload, _ = dev_token('demo-operator').split('.')
    claims = json.loads(base64.urlsafe_b64decode(payload + '=' * (-len(payload) % 4)))
    claims['fxp_role'] = 'operator'
    payload = base64.urlsafe_b64encode(json.dumps(claims).encode()).rstrip(b'=').decode()
    signed = (header + '.' + payload).encode()
    signature = hmac.new(os.environ['FXP_JWT_SECRET'].encode(), signed, hashlib.sha256).digest()
    return signed.decode() + '.' + base64.urlsafe_b64encode(signature).rstrip(b'=').decode()


def report(client, order, kind):
    return until(client, lambda e: e.kind == 'execution_report'
                 and e.message.execution_report.order_id == order
                 and e.message.execution_report.execution_type == kind).execution_report


def run(bug, seed, destination):
    if bug == 'stale-replace':
        with contextlib.redirect_stdout(io.StringIO()):
            result = stale_replace(seed, 8082, destination)
        result.update(bug=bug, symptom='Quote reprice failed after a partial fill; the old quote is still live.'
                      if result['bug_reproduced'] else 'Reprice succeeded without a preceding fill.')
    else:
        quantity = 3 + random.Random(seed).randrange(3)
        order = f'q-{seed}'
        result = dict(bug=bug, seed=seed, owner='demo-maker', symbol='BOTTEST',
                      order_id=order, core_order_id='demo-maker::' + order, quantity=quantity)
        with Client.connect('127.0.0.1', 8082, dev_token('demo-maker'), timeout=30) as maker, \
             Client.connect('127.0.0.1', 8082, dev_token('demo-taker'), timeout=30) as taker:
            if bug == 'stp-surprise':
                bot = SharedOwnerBot()
                maker.new(order, 'BOTTEST', pb.SELL, quantity, 10100, stp_mode=pb.SELF_TRADE_PREVENTION_CANCEL_OLDEST)
                report(maker, order, pb.EXEC_NEW)
                opposing = order + '-b'
                # A second strategy on the SAME authenticated owner/session.
                maker.new(opposing, 'BOTTEST', pb.BUY, quantity, 10100, stp_mode=pb.SELF_TRADE_PREVENTION_CANCEL_OLDEST)
                pending = {order: pb.EXEC_CANCELED, opposing: pb.EXEC_NEW}
                def observe(event):
                    if event.kind == 'execution_report':
                        r = event.message.execution_report
                        bot.observe(r)
                        if r.order_id in pending and r.execution_type == pending[r.order_id]:
                            del pending[r.order_id]
                until(maker, lambda _: not pending, observe)
                assert bot.inventory == -quantity
                result.update(opposing_order='demo-maker::' + opposing,
                              symptom=f'Inventory is {bot.inventory}, but no sale was logged; one strategy quote vanished.')
            elif bug == 'fill-cancel-race':
                bot = LateReaderBot()
                maker.new(order, 'BOTTEST', pb.SELL, quantity, 10100)
                report(maker, order, pb.EXEC_NEW)
                taker.new(order + '-take', 'BOTTEST', pb.BUY, quantity, 10100)
                report(taker, order + '-take', pb.EXEC_FILLED)
                # Real taker fill is the barrier. Maker has deliberately NOT read
                # its fill yet, so the following cancel deterministically loses.
                maker.cancel(order, 'BOTTEST', order + '-cancel')
                pending = {pb.EXEC_FILLED, pb.EXEC_REJECTED}
                def observe(event):
                    if event.kind == 'execution_report':
                        r = event.message.execution_report
                        if r.order_id == order and r.execution_type in pending:
                            bot.observe(r)
                            pending.remove(r.execution_type)
                until(maker, lambda _: not pending, observe)
                assert bot.hedges == 2
                taker.new(order + '-liquidity', 'BOTTEST', pb.SELL, quantity * 2, 10200)
                report(taker, order + '-liquidity', pb.EXEC_NEW)
                for i in range(bot.hedges):
                    hedge = order + f'-h{i}'
                    maker.new(hedge, 'BOTTEST', pb.BUY, quantity, 10200)
                    report(maker, hedge, pb.EXEC_FILLED)
                result.update(symptom=f'Quote manager sent two {quantity}-unit hedges for one sale; inventory overshot by {quantity}.')
            else:
                bot = ContinuousBot()
                # Establish a real last trade used by the auction tie-breaker.
                taker.new(order + '-ref', 'BOTTEST', pb.SELL, 1, 10200)
                report(taker, order + '-ref', pb.EXEC_NEW)
                maker.new(order + '-ref-take', 'BOTTEST', pb.BUY, 1, 10200)
                report(maker, order + '-ref-take', pb.EXEC_FILLED)
                report(taker, order + '-ref', pb.EXEC_FILLED)
                maker.subscribe('BOTTEST')
                until(maker, lambda e: e.kind == 'trading_session_status')
                with Client.connect('127.0.0.1', 8082, operator_token(), timeout=30) as operator:
                    operator.send(pb.FXPMessage(session_control=pb.SessionControl(
                        request_id='call', symbol='BOTTEST', opening_call_seconds=10)))
                    ack = until(operator, lambda e: e.kind == 'session_control_report').session_control_report
                    assert not ack.reject_reason and ack.symbols_affected == 1
                    until(maker, lambda e: e.kind == 'trading_session_status'
                          and e.message.trading_session_status.phase == pb.OPENING_CALL)
                    taker.new(order + '-ask', 'BOTTEST', pb.SELL, quantity, 9900)
                    report(taker, order + '-ask', pb.EXEC_NEW)
                    bot.quote(maker, order, quantity)
                    report(maker, order, pb.EXEC_NEW)  # rests despite crossed limits
                    fill = report(maker, order, pb.EXEC_FILLED)
                    assert fill.auction and fill.execution_price == 10200 != bot.expected_price
                    result.update(symptom=f'Expected a fill at {bot.expected_price}; the order rested first and later filled at {fill.execution_price}.')
            maker.mass_cancel('cleanup', symbol='BOTTEST')
            cleanup = until(maker, lambda e: e.kind == 'order_mass_cancel_report').order_mass_cancel_report
            assert not cleanup.reject_reason
    Path(destination).write_text(json.dumps(result, indent=2) + '\n')
    print(result['symptom'])
    return result


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('--bug', choices=BUGS, required=True)
    p.add_argument('--seed', type=int, default=7)
    p.add_argument('--result', required=True)
    args = p.parse_args()
    run(args.bug, args.seed, args.result)

# SPDX-License-Identifier: MIT
"""Cross-check diagnosis against verified events; never guess a bot's intent."""
import json
import sys


def diagnose(timeline, scenario):
    assert timeline['verified'] and timeline['source'] == 'recorded'
    rows = timeline['events']
    order = scenario['core_order_id']
    fills = [r['data'] for r in rows if r['kind'] == 'fill' and r['order_id'] == order]
    commands = [r['data'] for r in rows if r['kind'] == 'command_received' and r['data']['kind'] == 'replace']
    assert len(commands) == 1
    original = next(r['data'] for r in rows if r['kind'] == 'accepted' and r['order_id'] == order)
    filled = sum(r['quantity'] for r in fills)
    leaves = fills[-1]['leaves_quantity'] if fills else original['leaves_quantity']
    requested = commands[0]['new_quantity']
    assert filled == scenario['filled'] and leaves == scenario['leaves']
    assert requested == leaves == scenario['requested_total']
    if filled:
        reject = next(r['data'] for r in rows if r['kind'] == 'rejected')
        assert reject['cause'] == scenario['reject_reason'] == 'Replace quantity not above filled quantity'
        assert requested <= filled and scenario['bug_reproduced']
        return (f'BUG REPRODUCED: {order} filled {filled}/{original["order_quantity"]}, leaving {leaves}. '
                f'The replace command sent new_quantity={requested} as TOTAL quantity, '
                f'which does not exceed the {filled} already filled. '
                f'Engine reason: {reject["cause"]}. '
                'To preserve 3 leaves, send total 10 (or 0 to keep quantity). '
                'These events prove the invalid request; bot.py documents the leaves/total bug. '
                'Replay does not infer or rerun bot decisions.')
    accepted = next(r['data'] for r in rows if r['kind'] == 'accepted' and r['order_id'] == scenario['core_replacement_id'])
    assert not scenario['bug_reproduced'] and accepted['order_quantity'] == 10
    return 'CONTROL: no partial fill before replace; leaves 10 equals total 10. The bug stays hidden.'


if __name__ == '__main__':
    print(diagnose(json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))))

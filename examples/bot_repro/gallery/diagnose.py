# SPDX-License-Identifier: MIT
"""Assertions against verified trace, not scenario labels passed off as evidence."""
import json
import sys


def diagnose(timeline, explanation, scenario):
    assert timeline['verified'] and explanation['verified']
    assert timeline['source'] == explanation['source'] == 'recorded'
    all_rows = timeline['events']
    rows = [r for r in all_rows if r['order_id'] == scenario['core_order_id']]
    summary = explanation['summary']
    bug = scenario['bug']
    if bug == 'stale-replace':
        from diagnose import diagnose as old_diagnose
        answer = old_diagnose(timeline, scenario)
        if scenario['bug_reproduced']:
            assert 'Replace quantity not above filled quantity' in summary
        return answer
    if bug == 'stp-surprise':
        cancel = next(r for r in rows if r['kind'] == 'canceled' and 'self-trade prevention' in r['data']['cause'])
        assert cancel['data']['cause'] == 'self-trade prevention (cancel-oldest)'
        assert cancel['data']['opposing_order'] == scenario['opposing_order']
        assert any(r['kind'] == 'accepted' and r['order_id'] == scenario['opposing_order'] and r['owner'] == scenario['owner'] for r in all_rows)
        assert 'cancel-oldest' in summary and scenario['opposing_order'] in summary
        assert not any(r['kind'] == 'fill' for r in rows)
        return f"STP cancel-oldest removed {scenario['core_order_id']} against opposing own order {scenario['opposing_order']}; no fill occurred. The bot counts cancellations as sales."
    if bug == 'fill-cancel-race':
        fill = next(r for r in rows if r['kind'] == 'fill')
        cancel = next(r for r in rows if r['kind'] == 'command_received' and r['data']['kind'] == 'cancel')
        reject = next(r for r in rows if r['kind'] == 'rejected')
        assert fill['global_seq'] < cancel['global_seq'] == reject['global_seq']
        assert fill['data']['leaves_quantity'] == 0 and fill['data']['quantity'] == scenario['quantity']
        assert reject['data']['cause'] and reject['data']['cause'] in summary
        assert 'cancel rejected' in summary
        hedges = [r for r in all_rows if r['kind'] == 'fill' and r['order_id'].startswith(scenario['core_order_id'] + '-h')]
        assert len(hedges) == 2 and sum(r['data']['quantity'] for r in hedges) == 2 * scenario['quantity']
        assert all(r['global_seq'] > reject['global_seq'] for r in hedges)
        return f"Fill journal seq {fill['global_seq']} precedes cancel seq {cancel['global_seq']}; cancel rejected: {reject['data']['cause']}. The late-reader bot hedges the fill and the cancel reject separately."
    accepted = next(r for r in rows if r['kind'] == 'accepted')
    fill = next(r for r in rows if r['kind'] == 'fill')
    from fxp_client import pb
    assert accepted['data']['session_phase'] == pb.OPENING_CALL
    assert fill['data']['auction'] and fill['data']['price'] == 10200
    assert 'OpeningCall' in summary and 'Auction fill at price 10200' in summary
    return f"Order accepted in OpeningCall at seq {accepted['global_seq']}, then auction fill seq {fill['global_seq']} at 10200. Continuous matching at the resting 9900 ask was an invalid assumption."


if __name__ == '__main__':
    print(diagnose(*(json.load(open(p)) for p in sys.argv[1:4])))

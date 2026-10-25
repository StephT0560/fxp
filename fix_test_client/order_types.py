#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Order-type integration checks. Run against scripts/e2e.sh's configured stack."""
from fix_client import FIXSession, order_id, utc_now


def run():
    s = FIXSession('127.0.0.1', 8083, 'ORDERTYPES01', 'FXPBRIDGE')
    s.connect()
    s.logon()
    checks = 0

    def check(condition, description):
        nonlocal checks
        assert condition, description
        checks += 1
        print(f'PASS {description}', flush=True)

    def order(symbol, side, qty, price, floor=None):
        oid = order_id('OT')
        fields = [(11, oid), (55, symbol), (54, side), (38, str(qty)),
                  (40, '2'), (59, '1'), (44, price), (60, utc_now())]
        if floor is not None:
            fields.append((111, str(floor)))
        s.send('D', fields)
        return oid

    def snapshot(symbol):
        s.send('V', [(262, order_id('MD')), (263, '0'), (264, '0'),
                     (267, '2'), (269, '0'), (269, '1'), (146, '1'), (55, symbol)])
        m = s.wait_for_md('W', lambda m: m.get(55) == symbol)
        assert m is not None, f'no snapshot {symbol}'
        return m

    try:
        for symbol, price, qty in [('P0', '12', 1), ('P2', '12.35', 10),
                                   ('P4', '12.3456', 1), ('P8', '12.34567890', 1)]:
            sell = order(symbol, '2', qty, price)
            check(s.wait_for(sell, '0') is not None, f'{symbol} accepts exact precision')
            m = snapshot(symbol)
            check(m.get(270) == price and m.get(271) == str(qty), f'{symbol} snapshot precision')
            buy = order(symbol, '1', qty, price)
            m = s.wait_for(buy, 'F')
            check(m is not None and m.get(31) == price and m.get(6) == price,
                  f'{symbol} LastPx and AvgPx precision')
        for price, qty in [('12.34', 10), ('12.35', 11), ('12.35', 1000010)]:
            oid = order('P2', '2', qty, price)
            check(s.wait_for(oid, '8') is not None, 'reject invalid tick/lot/maximum quantity')
        oid = order('P8', '2', 1, '92233720368.54775807')
        check(s.wait_for(oid, '0') is not None, 'int64 maximum accepted')
        s.cancel_order(order_id('C'), oid, 'P8', '2')
        check(s.wait_for(oid, '4') is not None, 'int64 maximum canceled')
        oid = order('P8', '2', 1, '92233720368.54775808')
        check(s.wait_for(oid, '8') is not None, 'int64 overflow rejected')

        symbol = 'ICEBERG'
        a = order(symbol, '2', 10, '100.00', 3)
        m = s.wait_for(a, '0')
        check(m is not None and m.get(111) == '3' and m.get(151) == '10',
              'MaxFloor ack keeps total leaves')
        b = order(symbol, '2', 2, '100.00')
        check(s.wait_for(b, '0') is not None, 'ordinary order behind iceberg')
        check(snapshot(symbol).get(271) == '5', 'snapshot excludes reserve')
        take = order(symbol, '1', 7, '100.00')
        check(s.wait_for(take, '0') is not None, 'taker ack')
        fills = [s.wait_for(take, 'F') for _ in range(3)]
        check([m.get(32) if m else None for m in fills] == ['3', '2', '2'],
              'slice replenishment gives peer priority')
        fills = [s.wait_for(a, 'F') for _ in range(2)]
        check([(m.get(14), m.get(151)) if m else None for m in fills] == [('3', '7'), ('5', '5')],
              'iceberg cumulative fills use total quantity')
        check(snapshot(symbol).get(271) == '1', 'partial slice shown after fills')
        replacement = order_id('R')
        s.send('G', [(11, replacement), (41, a), (55, symbol), (54, '2'),
                     (38, '12'), (40, '2'), (44, '100.00'), (111, '2'), (60, utc_now())])
        m = s.wait_for(replacement, '5')
        check(m is not None and (m.get(14), m.get(151), m.get(111)) == ('5', '7', '2'),
              'replace preserves cumulative and resets slice')
        check(snapshot(symbol).get(271) == '2', 'replacement displays fresh slice')
        s.cancel_order(order_id('C'), replacement, symbol, '2')
        m = s.wait_for(replacement, '4')
        check(m is not None and m.get(14) == '5' and m.get(151) == '0',
              'cancel removes remaining reserve')
        check(snapshot(symbol).get(268) == '0', 'book empty after reserve cancel')
        print(f'Order types: {checks}/{checks} passed', flush=True)
    finally:
        s.logout()


if __name__ == '__main__':
    run()

#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Independent JSON/HTML/CSP validation and semantic gallery snapshots (stdlib)."""
import argparse
import base64
from collections import Counter
import hashlib
from html.parser import HTMLParser
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]


class Markup(HTMLParser):
    def __init__(self):
        super().__init__()
        self.csp = None
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        assert not any(k.lower().startswith('on') for k in attrs), attrs
        assert not any(k in attrs for k in ('src', 'href')), (tag, attrs)
        if tag == 'meta' and attrs.get('http-equiv') == 'Content-Security-Policy':
            self.csp = attrs['content']


def validate(path):
    path = Path(path)
    data = json.loads(path.read_text())
    assert data['schema'] == 'fxp-run-report-v1'
    for key in ('manifest', 'verification', 'privacy', 'window', 'summary', 'orders', 'events', 'symbols', 'books', 'tape'):
        assert key in data
    assert data['verification']['result'] in ('identical', 'divergent', 'incomplete')
    def exact(value):
        assert not isinstance(value, (int, float)) or isinstance(value, bool), 'integers must be decimal strings'
        if isinstance(value, dict):
            for v in value.values(): exact(v)
        if isinstance(value, list):
            for v in value: exact(v)
    exact(data)
    html = path.with_suffix('.html').read_text()
    parser = Markup(); parser.feed(html)
    assert parser.csp and "default-src 'none'" in parser.csp and "connect-src 'none'" in parser.csp
    css = re.search(r'<style>(.*?)</style>', html, re.S)[1]
    script = re.search(r'<script>(.*?)</script>', html, re.S)[1]
    for kind, text in [('style', css), ('script', script)]:
        digest = base64.b64encode(hashlib.sha256(text.encode()).digest()).decode()
        assert f"{kind}-src 'sha256-{digest}'" in parser.csp
    embedded = re.search(r'<script id="report-data" type="application/json">(.*?)</script>', html, re.S)[1]
    assert '<' not in embedded and '&' not in embedded
    assert json.loads(embedded) == data
    assert len(html.encode()) < 100 * 1024 * 1024
    event_positions = {(r['global_seq'],r['event_index']) for r in data['events']}
    for order in data['orders']:
        positions = {tuple(p) for p in order['positions']}
        assert positions and positions <= event_positions
    return data


def event_summary(rows):
    owners = {}
    rejects, cancels = Counter(), Counter()
    orders = fills = 0
    for row in rows:
        kind, d = row['kind'], row['data']
        new = kind == 'command_received' and d.get('kind') == 'new'
        orders += new
        fills += kind == 'trade'
        if kind == 'rejected': rejects[d['cause']] += 1
        if kind in ('canceled', 'expired'): cancels[d['cause']] += 1
        if row['owner']:
            counts = owners.setdefault(row['owner'], dict(orders=0, fill_reports=0, rejects=0, cancels=0))
            for key, inc in [('orders', new), ('fill_reports', kind == 'fill'), ('rejects', kind == 'rejected'), ('cancels', kind in ('canceled', 'expired'))]:
                counts[key] += inc
    return dict(orders=str(orders), fills=str(fills),
                rejects_by_reason={k:str(v) for k,v in rejects.items()},
                cancels_by_cause={k:str(v) for k,v in cancels.items()},
                owners=[dict(owner=o, **{k:str(v) for k,v in n.items()}) for o,n in sorted(owners.items())])


def projection(data):
    # SDK smoke traffic shares the bundle but is outside the seeded gallery symbol.
    assert event_summary(data['events']) == data['summary']
    data = dict(data)
    for key in ('events', 'orders', 'books', 'tape'):
        data[key] = [row for row in data[key] if row['symbol'] == 'BOTTEST']
    data['summary'] = event_summary(data['events'])
    phases = []
    for frame in data['books']:
        phase = frame['ladder']['phase']
        if not phases or phase != phases[-1]: phases.append(phase)
    return {'summary': data['summary'],
            'orders': sorted([{k:o[k] for k in ('order_id','owner','symbol','status','reject_reason','cancel_cause','aliases')} for o in data['orders']], key=lambda o:(o['symbol'],o['owner'],o['order_id'])),
            'phases': phases,
            'tape': [{k:r['data'][k] for k in ('price','quantity','auction')} for r in data['tape']]}


def main():
    p = argparse.ArgumentParser(__doc__)
    p.add_argument('--file')
    p.add_argument('--scenario')
    p.add_argument('--write-snapshot', action='store_true')
    p.add_argument('--goldens', help='output directory; run the existing debug fxp-replay')
    args = p.parse_args()
    if args.goldens:
        out = Path(args.goldens); out.mkdir(parents=True, exist_ok=True)
        for bundle in sorted((ROOT/'fxp_core/tests/bundles').iterdir()):
            if not bundle.is_dir(): continue
            subprocess.run([str(ROOT/'fxp_core/target/debug/fxp-replay'),'report',str(bundle),'--html',str(out/(bundle.name+'.html')),'--json',str(out/(bundle.name+'.json')),'--owner','alice'],check=True)
            validate(out/(bundle.name+'.json'))
        print('PASS independent golden report JSON/HTML/CSP validation')
    if args.file:
        data = validate(args.file)
        if args.scenario:
            scenario = json.loads(Path(args.scenario).read_text())
            if scenario['seed'] == 7:
                path = ROOT/'examples/bot_repro/gallery'/scenario['bug']/'report-snapshot.json'
                encoded = json.dumps(projection(data),indent=2,sort_keys=True)+'\n'
                if args.write_snapshot: path.write_text(encoded)
                assert json.loads(path.read_text()) == projection(data), scenario['bug']
        print('PASS stable report schema, independent CSP SHA-256/Base64, embedded data and snapshot')


if __name__ == '__main__':
    main()

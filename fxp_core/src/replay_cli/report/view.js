'use strict';
(() => {
  const d = JSON.parse(document.getElementById('report-data').textContent);
  const el = id => document.getElementById(id);
  const put = (id, text) => { el(id).textContent = text; };
  const stringify = x => JSON.stringify(x, null, 2);
  const node = (tag, text) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = String(text); return n; };
  const table = (head, rows) => { const t = node('table'), h = node('tr'); head.forEach(s => h.append(node('th', s))); const th = node('thead'); th.append(h); t.append(th); const b = node('tbody'); rows.forEach(values => { const tr = node('tr'); values.forEach(s => tr.append(node('td', s))); b.append(tr); }); t.append(b); return t; };
  const choices = (id, values, all = true) => { const s = el(id); if (all) { const o = node('option', 'All'); o.value = ''; s.append(o); } [...new Set(values)].sort().forEach(v => { const o = node('option', v); o.value = all ? JSON.stringify(v) : v; s.append(o); }); };
  put('verification', d.verification.result.toUpperCase()); el('verification').className = d.verification.result === 'identical' ? 'good' : 'bad';
  put('notice', `${d.privacy.anonymized_owners ? 'Non-selected owners and all order/client identities pseudonymized; free-form manifest metadata omitted. ' : ''}Lifecycle source: ${d.verification.source}. Book source: replayed engine. Window ${d.window.from}–${d.window.to}. ${d.window.boundary}. ${d.verification.result !== 'identical' ? 'UNVERIFIED / DIVERGENT: inspect verification before relying on this report.' : ''}`);
  put('provenance', stringify({ manifest: d.manifest, verification: d.verification, window: d.window, privacy: d.privacy }));
  put('summary', `Orders: ${d.summary.orders} · public fills: ${d.summary.fills}\nRejects by reason: ${stringify(d.summary.rejects_by_reason)}\nCancels/expiry by cause: ${stringify(d.summary.cancels_by_cause)}`);
  el('owners').append(table(['Owner', 'Orders', 'Fill reports', 'Rejects', 'Cancels'], d.summary.owners.map(o => [o.owner, o.orders, o.fill_reports, o.rejects, o.cancels])));
  choices('owner', d.orders.map(o => o.owner)); choices('symbol', d.orders.map(o => o.symbol)); choices('status', d.orders.map(o => o.status)); choices('book-symbol', d.symbols, false);
  const eventIndex = new Map(d.events.map(r => [`${r.global_seq}:${r.event_index}`, r]));
  let offset = 0;
  const detail = order => {
    el('detail').querySelector('h3').textContent = `${order.order_id} · ${order.owner} · ${order.symbol}`;
    put('explain', order.explanation);
    const rows = order.positions.map(p => eventIndex.get(p.join(':'))).filter(Boolean);
    el('lifecycle').replaceChildren(table(['Seq:index', 'Event', 'Order', 'Detail'], rows.map(r => [`${r.global_seq}:${r.event_index}`, r.kind, r.order_id, stringify(r.data)])));
  };
  const renderOrders = () => {
    const query = el('search').value.toLowerCase(), reject = el('reject').value.toLowerCase(), cancel = el('cancel').value.toLowerCase();
    const found = d.orders.filter(o => (!el('owner').value || o.owner === JSON.parse(el('owner').value)) && (!el('symbol').value || o.symbol === JSON.parse(el('symbol').value)) && (!el('status').value || o.status === JSON.parse(el('status').value)) && o.reject_reason.toLowerCase().includes(reject) && o.cancel_cause.toLowerCase().includes(cancel) && (!query || stringify(o).toLowerCase().includes(query)));
    offset = Math.min(offset, Math.max(0, Math.floor((found.length - 1) / 200) * 200));
    el('orders').replaceChildren();
    found.slice(offset, offset + 200).forEach(o => { const tr = node('tr'), td = node('td'), button = node('button', o.order_id || '(empty ID)'); button.type = 'button'; button.addEventListener('click', () => detail(o)); td.append(button); tr.append(td); [o.owner, o.symbol, o.status, o.reject_reason, o.cancel_cause].forEach(s => tr.append(node('td', s))); el('orders').append(tr); });
    put('order-count', `${found.length} chains · showing ${found.length ? offset + 1 : 0}–${Math.min(offset + 200, found.length)}. Replacements share a lifecycle.`);
    el('previous').disabled = offset === 0; el('next').disabled = offset + 200 >= found.length;
  };
  ['owner', 'symbol', 'status', 'reject', 'cancel', 'search'].forEach(id => el(id).addEventListener('input', () => { offset = 0; renderOrders(); }));
  el('previous').addEventListener('click', () => { offset = Math.max(0, offset - 200); renderOrders(); }); el('next').addEventListener('click', () => { offset += 200; renderOrders(); }); renderOrders();
  const compare = (a, b) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0;
  const seqs = [...new Set([d.window.from, ...d.books.map(b => b.seq), ...d.events.map(r => r.global_seq)])].sort(compare);
  el('sequence').max = String(Math.max(0, seqs.length - 1));
  const frames = new Map(d.symbols.map(s => [s, []])), prints = new Map(d.symbols.map(s => [s, []]));
  d.books.forEach(b => frames.get(b.symbol).push(b)); d.tape.forEach(r => prints.get(r.symbol).push(r));
  const prefix = (array, seq, key) => { let a = 0, b = array.length; while (a < b) { const m = (a + b) >>> 1; if (compare(key(array[m]), seq) <= 0) a = m + 1; else b = m; } return a; };
  const book = () => {
    const symbol = el('book-symbol').value, seq = seqs[Number(el('sequence').value)] || '0', f = frames.get(symbol) || [], at = prefix(f, seq, r => r.seq), ladder = at ? f[at - 1].ladder : { bids: [], asks: [], phase: 'not observed' };
    put('book-caption', `Sequence ${seq} · ${symbol} · phase ${ladder.phase} · working-order marks: ${d.privacy.selected_owner || 'none (generate with --owner NAME)'}`);
    for (const side of ['bids', 'asks']) { el(side).replaceChildren(table(['Price (fixed point)', 'Displayed qty', 'Selected owner: ID / displayed / leaves'], ladder[side].map(l => [l.price, l.quantity, l.own_orders.map(o => `${o.order_id} / ${o.displayed} / ${o.leaves}`).join('\n')]))); el(side).classList.toggle('own', ladder[side].some(l => l.own_orders.length)); }
    const tape = prints.get(symbol) || [], count = prefix(tape, seq, r => r.global_seq);
    put('tape-caption', `${count} window prints up to this boundary; showing the last ${Math.min(100, count)}. Earlier prints remain in the embedded JSON.`);
    el('tape').replaceChildren(table(['Seq', 'Price', 'Quantity', 'Auction'], tape.slice(Math.max(0, count - 100), count).reverse().map(r => [r.global_seq, r.data.price, r.data.quantity, r.data.auction ? 'yes' : 'no'])));
  };
  el('sequence').addEventListener('input', book); el('book-symbol').addEventListener('change', book); book();
})();

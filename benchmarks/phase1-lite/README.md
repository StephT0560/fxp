# Phase1-lite evidence

See [method and diagnosis](../../docs/performance/phase1-lite.md).
These artifacts contain counts, bytes and B/A ratios only. Raw duration-bearing
profiles and absolute benchmark output remain in ignored `work/`; no absolute
performance result is published.

`core-gateway-N` maps N external clients to one core feed (zero when N=0).
`core-direct-N` is an alternative direct-subscription stress. Gateway fixtures
inject 2,000 fixed incremental messages; N=0 is an empty-function-fanout baseline,
not a claim that a fresh un-subscribed gateway receives MD. Kernel fixture traces
include 128 warmup updates, unlike measured allocation/counter deltas.
`live-syscalls.json` uses a separate 2,000-order workload producing 4,000
incrementals. `.allocations.json` extracts only dhat count/stack fields;
pprof text files use allocation objects/bytes or mutex contentions, not delay.

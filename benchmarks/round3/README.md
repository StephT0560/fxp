# Recorded round-3 measurements

Source revision f0edb3eda1719506366a0835070ddb3cbb52641b, clean source tree;
2026-10-25, shared Linux VM. See ../../Benchmark.md for workload and caveats.

- live-summary.json: all per-run endpoint statistics and aggregate spreads,
  environment/config/instrument metadata; no HDR counts duplicated here.
- live-hdr.json.gz: gzip-compressed full JSON for all three live scenarios,
  including every per-run HDR snapshot. Decode with gzip -dc.
- encoding.json: all Criterion estimates with confidence intervals (ns;
  batch_10k groups are ns per batch, not per order).
- build-environment.txt: source/compiler/kernel/affinity and launch settings.
- initial/: the entire first recorded dataset at dff2cc3, before the deadline fix.

No historical record was edited and no unfavorable repetition was removed.

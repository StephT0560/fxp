# Recorded round-3 measurements

Source revision dff2cc33bb86b189ad4ba2cc30ad3fc86f61cca2, clean source tree;
2026-10-25, shared Linux VM. See ../../Benchmark.md for workload and caveats.

- live-summary.json: all per-run endpoint statistics and aggregate spreads,
  environment/config/instrument metadata; no HDR counts duplicated here.
- live-hdr.json.gz: gzip-compressed full JSON for all three live scenarios,
  including every per-run HDR snapshot. Decode with gzip -dc.
- encoding.json: all Criterion estimates with confidence intervals (ns;
  batch_10k groups are ns per batch, not per order).
- build-environment.txt: source/compiler/kernel/affinity and launch settings.

No historical record was edited and no unfavorable repetition was removed.

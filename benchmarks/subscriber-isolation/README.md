# Subscriber isolation evidence

See [method and interpretation](../../docs/performance/subscriber-isolation.md).
`counts.json` and `slow-counts.json` contain five repetitions per revision.
`relative.json` contains all interleaved paired throughput ratios;
`slow-consumer.json` is the separate same-session functional check.
`environment.json` identifies source revisions, layout and durability.

No elapsed times, absolute latency histograms or orders/s are published.
Raw logs, executable copies and timing-bearing output remain ignored in work/.

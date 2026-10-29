# Subscriber baseline

The merged [phase 1-lite report](phase1-lite.md) is the baseline for subscriber
isolation. This index supplies the original task's baseline filename; it does
not introduce another measurement or change the report's recommendation:
**use the separate-process subscriber harness; no production optimization on
this evidence**.

Before implementing subscriber-isolation tests, declare a **4× median elapsed
ratio ceiling**, stalled/control, over five back-to-back pairs in one test
session. Each half delivers 2,000 private reports while injecting the same
market-data updates; only the presence of a saturated subscriber changes.
The baseline's largest observed harness ratio was 1.379× and its normalized
within-mode spread was at most 0.128. Four is a deliberately generous functional
ceiling, not a latency SLO, a statistical confidence interval, or a throughput
claim. Individual ratios and their range remain visible. This test complements
exact queue/drop assertions and retained-memory/goroutine checks.

Publish work counts before/after and relative ratios only. Absolute timings
remain ignored smoke artifacts. Use the same harness at both revisions, and
attribute each build to its actual source SHA. No production fix is authorized
by the baseline diagnosis.

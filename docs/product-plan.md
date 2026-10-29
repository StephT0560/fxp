# Product plan

FXP is an execution environment where trading-bot developers can reproduce
failures, inspect every order and fill, and verify strategy changes.

Phase A is a bounded performance phase (about 3–5 engineering days). Diagnose
work per update and harness contention before changing production paths. Record
hardware, build, durability and source revision; use paired runs and repetition
spread. Shared-host absolute timings are smoke only. There is no absolute
performance or FIX comparison claim without quiet hardware and equal topology.

Phase B is a reproducibility sprint: audit engine determinism, define canonical
business-event digests and final-state hashes, record command bundles, provide
an offline replay/inspection CLI, and demonstrate a real bot failure using a
small client and seeded scenario. Recorded commands reproduce ordered business
events and final market state. Bot decisions, clocks, randomness and network
scheduling are outside that guarantee.

Keep the demo small. Deferred work includes UDP market data, io_uring, pinned
thread-per-core execution, fixed-layout messages, group commit and journal-backed
sessions. Do not build these without an explicit task. Hosted sandboxes,
branching experiments and additional SDK languages are separate decisions.

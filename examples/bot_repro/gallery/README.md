# Seeded bug gallery (contains answers)

Each entry directory supplies a seed, bot variant, developer-visible symptom and
expected root cause in `entry.json`. `scenarios.py` orders real gateway commands
using received ACKs and fills. `bots.py` contains the three new deliberate state
errors; stale-replace keeps the original `../bot.py` and its seed-1 control.
`diagnose.py` checks recorded, verified timeline/explanation output before making
a source-level diagnosis. Events prove exchange behavior; source identifies the
bad bot transition. Replay never reruns or proves a bot's internal decisions.

Run `make repro-demo BUG=<entry>` to study an answer, or `make repro-blind` to
practice first. Avoid this directory's entry files until reveal when practicing.
These are deliberately unsafe teaching bots, not trading strategies to deploy.

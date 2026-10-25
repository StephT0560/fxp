"""Render only FXP ramp evidence; requires matplotlib (collection used 3.10.8)."""

import gzip
import json
import pathlib
import os

root = pathlib.Path(__file__).resolve().parent
cache = root.parents[1] / "work" / "r4-profile"
cache.mkdir(parents=True, exist_ok=True)
os.environ.setdefault("MPLCONFIGDIR", str(cache / "mpl"))
os.environ.setdefault("XDG_CACHE_HOME", str(cache / "cache"))
import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import FuncFormatter

fig, axes = plt.subplots(1, 2, figsize=(12, 4.5), layout="constrained")
for mode, label, color in [
    ("single", "Single symbol", "#1769aa"),
    ("spread", "5 symbols / 10 sessions", "#c65d16"),
]:
    rates, latency, throughput = [], [], []
    for rate in [5000, 10000, 20000, 40000, 80000]:
        with gzip.open(root / f"open-{mode}-r{rate}.json.gz", "rt") as stream:
            data = json.load(stream)
        if data.get("error"):
            continue  # Missing reports are not a zero or a valid percentile.
        rates.append(rate)
        latency.append(data["summary"]["ack_p99_ns"])
        throughput.append(data["summary"]["orders_per_second"])
    for ax, values, scale in [(axes[0], latency, 1e6), (axes[1], throughput, 1)]:
        centers = [v["median"] / scale for v in values]
        errors = [
            [(v["median"] - v["min"]) / scale for v in values],
            [(v["max"] - v["median"]) / scale for v in values],
        ]
        ax.errorbar(
            rates, centers, yerr=errors, color=color, marker="o", capsize=4, label=label
        )
axes[0].set_yscale("log")
axes[0].set_ylabel("ACK p99 (ms, logarithmic)")
axes[0].set_title("Queueing increases before reports go missing")
axes[1].plot([0, 40000], [0, 40000], "--", color="#777777", label="Completed = offered")
axes[1].set_ylabel("Completed orders/s (including drain)")
axes[1].set_title("Finite-window completion, not production capacity")
for ax in axes:
    ax.axvspan(20000, 40000, color="#f2cb5c", alpha=0.15)
    ax.axvline(80000, color="#ac3030", linestyle=":")
    ax.text(
        0.98,
        0.12,
        "80k: failed\nno valid summary",
        ha="right",
        transform=ax.transAxes,
        color="#ac3030",
        fontsize=9,
    )
    ax.set_xlim(0, 85000)
    ax.set_xticks([5000, 10000, 20000, 40000, 80000])
    ax.xaxis.set_major_formatter(FuncFormatter(lambda v, _: f"{v/1000:g}k"))
    ax.set_xlabel("Aggregate offered orders/s")
    ax.grid(alpha=0.2)
    ax.legend(fontsize=8, loc="upper left")
fig.suptitle(
    "Round 4: five-second offered windows, five repetitions (median and min/max)"
)
matplotlib.rcParams["svg.fonttype"] = "none"
fig.savefig(
    root / "capacity-knee.svg",
    metadata={
        "Description": "FXP loopback on a shared container, core CPU 0, gateway CPU 1, generator CPU 2, two-CPU quota. Yellow region spans the tested throughput transition; 80k failed and is not plotted as throughput or p99."
    },
)

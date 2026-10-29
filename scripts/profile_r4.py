#!/usr/bin/env python3
"""Pinned, fresh-stack round-4 experiments. Raw failed runs are retained too."""

import argparse, gzip, hashlib, json, os, pathlib, resource, signal, subprocess, time
from profile_guard import ensure_ports_free, wait_port

ROOT = pathlib.Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser()
p.add_argument("--output", default="benchmarks/round4")
p.add_argument(
    "--suite",
    choices=[
        "pacing",
        "capacity",
        "hops",
        "profiles",
        "coverage",
        "fanout",
        "pacer-traces",
        "rust-profiles",
        "ab",
        "fix-capacity",
        "fix-below",
        "rust-c50",
        "overload",
    ],
    required=True,
)
p.add_argument("--core-binary", default=str(ROOT / "fxp_core/target/release/fxp_core"))
p.add_argument("--perf-binary")
p.add_argument("--dhat", action="store_true")
p.add_argument("--label", default="baseline")
p.add_argument("--subscriber-process", action="store_true", help="separate subscriber runtime")
p.add_argument("--subscriber-cpu", default="0", help="CPU for separate subscribers; shares core on three-CPU machines")
p.add_argument("--offered-rate", type=int, default=2000)
a = p.parse_args()
out = ROOT / a.output
out.mkdir(parents=True, exist_ok=True)
bins = ROOT / "work/r4-profile"
base = dict(
    os.environ,
    FXP_JWT_SECRET="dev-secret-change-in-production",
    FXP_GW_RATE="1000000",
    FXP_GW_RATE_BURST="1000000",
    GOMAXPROCS="1",
    TOKIO_WORKER_THREADS="1",
    RUST_LOG="error",
    FXP_LOG_LEVEL="error",
)
for key in [
    "FXP_EVENT_LOG",
    "FXP_SESSION_SCHEDULE",
    "FXP_PROFILE_DIR",
    "FXP_TRACE_FILE",
    "FXP_HOP_TRACE",
]:
    base.pop(key, None)
layout = "core=0 gateway=1 generator=2; GOMAXPROCS=1 TOKIO_WORKER_THREADS=1; shared cgroup quota 2 CPUs"
if a.subscriber_process:
    layout += f"; separate subscribers={a.subscriber_cpu} (core shares CPU if 0)"


def cpu(pid):
    fields = pathlib.Path(f"/proc/{pid}/stat").read_text().split()
    return sum(map(int, fields[13:15])) / os.sysconf("SC_CLK_TCK")


def scenario(
    name,
    mode,
    c,
    rate,
    symbols,
    pacer="sleep",
    hops=False,
    profiles=False,
    fix_mode=None,
    persistence=False,
    subscribers=0,
    orders=1000,
):
    ensure_ports_free([8080, 8081, 8082] + ([8083] if fix_mode is not None else []))
    procs = []
    logs = []
    components = []
    sampler = None
    binaries = {}
    scratch = bins / name
    import shutil

    shutil.rmtree(scratch, ignore_errors=True)
    scratch.mkdir()
    try:
        launch = [
            ("core", 0, pathlib.Path(a.core_binary), 8080),
            ("gateway", 1, bins / "gateway", 8082),
        ]
        if fix_mode is not None:
            launch.append(("bridge", 1, bins / "bridge", 8083))
        for component, cpuid, exe, port in launch:
            components.append(component)
            binaries[component] = {
                "path": str(exe),
                "sha256": hashlib.sha256(exe.read_bytes()).hexdigest(),
            }
            log = open(out / f"{name}-{component}.txt", "w")
            logs.append(log)
            env = base.copy()
            if component == "core" and a.dhat:
                env["FXP_DHAT_FILE"] = str(out / f"{name}-dhat.json")
            if component == "core" and persistence:
                env["FXP_EVENT_LOG"] = str(scratch / "events")
            if component == "bridge":
                env["FXP_FIX_STORE_DIR"] = (
                    str(scratch / "store") if fix_mode != "memory" else ""
                )
                env["FXP_FIX_STORE_FSYNC"] = (
                    fix_mode if fix_mode != "memory" else "async"
                )
                env["FXP_FIX_CREDENTIALS"] = ",".join(
                    f"BENCH{r}_{i}:benchmark-password"
                    for r in range(5)
                    for i in range(c)
                )
            if hops:
                env["FXP_HOP_TRACE"] = str(out / f"{name}-{component}-hops.txt")
            if profiles and component in ["gateway", "bridge"]:
                env["FXP_PROFILE_DIR"] = str(out / f"{name}-{component}-profiles")
                if mode == "open" and c == 1 and rate == 1000:
                    env["FXP_TRACE_FILE"] = str(out / f"{name}-{component}.trace")
            procs.append(
                subprocess.Popen(
                    ["taskset", "-c", str(cpuid), str(exe)],
                    cwd=ROOT,
                    env=env,
                    stdout=log,
                    stderr=subprocess.STDOUT,
                )
            )
            wait_port(port, procs[-1])
        if a.perf_binary:
            log = open(out / f"{name}-perf.txt", "w")
            logs.append(log)
            sampler = subprocess.Popen(
                [
                    a.perf_binary,
                    "record",
                    "-e",
                    "cpu-clock:u",
                    "-F",
                    "99",
                    "--call-graph",
                    "dwarf,8192",
                    "-p",
                    str(procs[0].pid),
                    "-o",
                    str(scratch / "core.perf"),
                ],
                stdout=log,
                stderr=subprocess.STDOUT,
            )
        before = {str(x.pid): cpu(x.pid) for x in procs}
        throttle_before = pathlib.Path("/sys/fs/cgroup/cpu.stat").read_text()
        start = time.monotonic()
        cmd = [
            "taskset",
            "-c",
            "2",
            str(bins / "bench"),
            "--mode",
            mode,
            "--pacer",
            pacer,
            "--concurrency",
            str(c),
            "--rate",
            str(rate),
            "--symbols",
            symbols,
            "--orders",
            str(orders),
            "--warmup",
            "100",
            "--repetitions",
            "5",
            "--pinning",
            layout,
            "--json",
            str(out / f"{name}.json"),
            "--markdown",
            str(out / f"{name}.md"),
        ]
        env = base.copy()
        if hops:
            env["FXP_HOP_TRACE"] = str(out / f"{name}-client-hops.txt")
        if profiles and mode == "open" and c == 1 and rate == 1000:
            env["FXP_TRACE_FILE"] = str(out / f"{name}-client.trace")
        if fix_mode is not None:
            cmd = [
                "taskset",
                "-c",
                "2",
                "python3",
                str(ROOT / "scripts/bench_fix.py"),
                "--concurrency",
                str(c),
                "--output",
                str(out / f"{name}.json"),
                "--mode",
                mode,
                "--rate",
                str(rate),
                "--orders",
                str(orders),
            ]
        else:
            cmd += ["--subscribers", str(subscribers)]
            if a.subscriber_process:
                cmd += ["--subscriber-process", "--subscriber-cpu", a.subscriber_cpu]
        usage_before = resource.getrusage(resource.RUSAGE_CHILDREN)
        result = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True)
        usage_after = resource.getrusage(resource.RUSAGE_CHILDREN)
        elapsed = time.monotonic() - start
        generator = (
            ROOT / "scripts/bench_fix.py" if fix_mode is not None else bins / "bench"
        )
        binaries["generator"] = {
            "path": str(generator),
            "sha256": hashlib.sha256(generator.read_bytes()).hexdigest(),
        }
        meta = {
            "source_commit": subprocess.check_output(
                ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
            ).strip(),
            "binaries": binaries,
            "working_tree_dirty": bool(
                subprocess.check_output(
                    ["git", "status", "--porcelain"], cwd=ROOT, text=True
                ).strip()
            ),
            "command": cmd,
            "returncode": result.returncode,
            "stdout": result.stdout,
            "stderr": result.stderr,
            "elapsed_seconds": elapsed,
            "generator_cpu_seconds": usage_after.ru_utime
            + usage_after.ru_stime
            - usage_before.ru_utime
            - usage_before.ru_stime,
            "process_cpu_seconds": {
                n: cpu(proc.pid) - before[str(proc.pid)]
                for n, proc in zip(components, procs)
            },
            "cgroup_before": throttle_before,
            "cgroup_after": pathlib.Path("/sys/fs/cgroup/cpu.stat").read_text(),
        }
        (out / f"{name}-execution.json").write_text(json.dumps(meta, indent=2) + "\n")
        print(name, result.returncode, flush=True)
    finally:
        if sampler is not None:
            if sampler.poll() is None:
                sampler.send_signal(signal.SIGINT)
            sampler.wait(timeout=20)
            with open(out / f"{name}-core-stacks.txt", "w") as dest:
                subprocess.run(
                    [a.perf_binary, "script", "-i", str(scratch / "core.perf")],
                    stdout=dest,
                    stderr=subprocess.DEVNULL,
                )
            with open(out / f"{name}-core-perf-report.txt", "w") as dest:
                subprocess.run(
                    [
                        a.perf_binary,
                        "report",
                        "--stdio",
                        "--no-children",
                        "-i",
                        str(scratch / "core.perf"),
                    ],
                    stdout=dest,
                    stderr=subprocess.DEVNULL,
                )
        for proc in reversed(procs):
            if proc.poll() is None:
                proc.send_signal(signal.SIGTERM)
        for proc in procs:
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        for log in logs:
            log.close()
        for path in out.glob(name + "*"):
            if (
                path.suffix in [".json", ".trace"]
                and "-execution" not in path.name
                or "-hops.txt" in path.name
                or "-core-stacks.txt" in path.name
            ):
                with path.open("rb") as src, gzip.open(str(path) + ".gz", "wb") as dst:
                    dst.write(src.read())
                path.unlink()


if a.suite == "pacing":
    for pacer in ["sleep", "busy-spin", "hybrid"]:
        scenario(f"open-c1-{pacer}", "open", 1, 1000, "single", pacer)
    scenario("closed-c1", "closed", 1, 1000, "single")
    scenario("open-c10", "open", 10, 1000, "spread")
elif a.suite == "hops":
    for name, mode, c in [
        ("open-c1", "open", 1),
        ("open-c10", "open", 10),
        ("closed-c1", "closed", 1),
    ]:
        scenario(
            "hops-" + name, mode, c, 1000, "single" if c == 1 else "spread", hops=True
        )
elif a.suite == "fix-capacity":
    for rate in [1000, 2000, 5000, 10000, 20000]:
        scenario(
            f"fix-open-r{rate}",
            "open",
            10,
            rate // 10,
            "single",
            fix_mode="async",
            orders=rate // 10 * 5,
        )
elif a.suite == "fix-below":
    scenario(
        "profile-fix-below-knee",
        "open",
        10,
        a.offered_rate // 10,
        "single",
        fix_mode="async",
        orders=a.offered_rate // 10 * 5,
        profiles=True,
    )
elif a.suite == "overload":
    base["RUST_LOG"] = "warn"
    base["FXP_LOG_LEVEL"] = "info"
    for symbols in ["single", "spread"]:
        scenario(f"overload-{symbols}", "open", 10, 8000, symbols, orders=40000)
elif a.suite == "fanout":
    for n in [0, 10, 100]:
        scenario(f"fanout-{n}", "closed", 10, 1000, "single", subscribers=n)
elif a.suite == "pacer-traces":
    for mode in ["sleep", "busy-spin"]:
        scenario("trace-" + mode, "open", 1, 1000, "single", mode, profiles=True)
elif a.suite == "rust-c50":
    scenario("perf-closed-c50", "closed", 50, 1000, "spread", profiles=True)
elif a.suite == "rust-profiles":
    scenario(
        a.label + "-closed-c50",
        "closed",
        50,
        1000,
        "spread",
        profiles=True,
        orders=100 if a.dhat else 1000,
    )
    scenario(
        a.label + "-below-knee",
        "open",
        10,
        2000,
        "spread",
        profiles=True,
        orders=1000 if a.dhat else 10000,
    )
elif a.suite == "ab":
    scenario(a.label + "-closed-c50", "closed", 50, 1000, "spread")
    scenario(a.label + "-below-knee", "open", 10, 2000, "spread", orders=10000)
elif a.suite == "coverage":
    scenario("persist-core", "closed", 1, 1000, "single", persistence=True)
    for n in [0, 10, 100]:
        scenario(f"fanout-{n}", "closed", 10, 1000, "single", subscribers=n)
    for persist in [False, True]:
        for mode in ["memory", "strict", "group", "async"]:
            scenario(
                f"fix-{mode}-corelog-{persist}",
                "closed",
                1,
                1000,
                "single",
                fix_mode=mode,
                persistence=persist,
            )
    scenario(
        "profile-fix-c50", "closed", 50, 1000, "single", fix_mode="async", profiles=True
    )
elif a.suite == "profiles":
    scenario("profile-open-c1", "open", 1, 1000, "single", profiles=True)
    scenario("profile-closed-c50", "closed", 50, 1000, "spread", profiles=True)
else:
    for symbols in ["single", "spread"]:
        for c in [1, 10, 50, 100]:
            scenario(f"closed-{symbols}-c{c}", "closed", c, 1000, symbols)
        # Per-connection --rate; ten sessions share the aggregate offered rate.
        for rate in [5000, 10000, 20000, 40000, 80000]:
            scenario(
                f"open-{symbols}-r{rate}",
                "open",
                10,
                rate // 10,
                symbols,
                orders=max(1000, rate // 10 * 5),
            )

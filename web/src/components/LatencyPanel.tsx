// SPDX-License-Identifier: MIT
import { useEffect, useState } from "react";
import {
  Bar,
  BarChart,
  CartesianGrid,
  ErrorBar,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { chartRows, parseBench, type BenchDocument } from "../lib/bench";

export default function LatencyPanel() {
  const [data, setData] = useState<BenchDocument>();
  const [error, setError] = useState("");
  const [selected, setSelected] = useState("");
  useEffect(() => {
    const c = new AbortController();
    fetch("/api/bench", { signal: c.signal })
      .then((r) => {
        if (!r.ok) throw Error("Benchmark data unavailable");
        return r.json();
      })
      .then(parseBench)
      .then((d) => {
        setData(d);
        setSelected(d.scenarios[0]!.id);
      })
      .catch(() => {
        if (!c.signal.aborted)
          setError(
            "Benchmark data unavailable or malformed. No latency claim can be shown.",
          );
      });
    return () => c.abort();
  }, []);
  const s = data?.scenarios.find((v) => v.id === selected);
  return (
    <section className="p-4 overflow-auto" aria-label="Latency panel">
      <h2>Recorded ACK latency</h2>
      <p>
        Plaintext loopback only. Median and min/max across independent
        repetitions. These are not WAN measurements.
      </p>
      <p>
        Intended-time ACK includes pacer delay. Post-send ACK includes
        construction, serialization, writes, server hops and decoding.
      </p>
      <p>
        Capacity is a tested knee for the measured workload, never an SLO. No
        cross-protocol ranking is supported.
      </p>
      <a
        className="underline"
        href="/api/bench/profile-r4"
        target="_blank"
        rel="noreferrer"
      >
        Read profile-r4.md
      </a>
      {error ? (
        <p role="status">{error}</p>
      ) : !s ? (
        <p role="status">Loading recorded benchmarks…</p>
      ) : (
        <>
          <label className="block my-3">
            Scenario{" "}
            <select
              aria-label="Benchmark scenario"
              value={selected}
              onChange={(e) => setSelected(e.target.value)}
            >
              {data!.scenarios.map((v) => (
                <option key={v.id}>{v.id}</option>
              ))}
            </select>
          </label>
          <div className="h-72" data-testid="latency-chart">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart
                data={chartRows(s)}
                margin={{ top: 20, right: 30, left: 10, bottom: 10 }}
              >
                <CartesianGrid stroke="#404040" />
                <XAxis dataKey="percentile" />
                <YAxis
                  label={{
                    value: "ACK ms",
                    angle: -90,
                    position: "insideLeft",
                  }}
                />
                <Tooltip />
                <Legend />
                <Bar
                  dataKey="intended"
                  name="Intended-time ACK"
                  fill="#60a5fa"
                  isAnimationActive={false}
                >
                  <ErrorBar
                    dataKey="intendedError"
                    width={8}
                    stroke="#e5e5e5"
                  />
                </Bar>
                <Bar
                  dataKey="post"
                  name="Post-send ACK"
                  fill="#34d399"
                  isAnimationActive={false}
                >
                  <ErrorBar dataKey="postError" width={8} stroke="#e5e5e5" />
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          </div>
          <table className="w-full text-left" data-testid="latency-spread">
            <caption>
              Median [min–max], milliseconds; {s.config.repetitions} repetitions
            </caption>
            <thead>
              <tr>
                <th>Endpoint</th>
                <th>p50</th>
                <th>p99</th>
              </tr>
            </thead>
            <tbody>
              {[
                ["Intended-time ACK", "ack"],
                ["Post-send ACK", "ack_after_send"],
              ].map(([label, key]) => (
                <tr key={key}>
                  <th>{label}</th>
                  {["p50", "p99"].map((p) => {
                    const v = s.summary[`${key}_${p}_ns`]!;
                    return (
                      <td key={p}>
                        {(v.median / 1e6).toFixed(3)} [
                        {(v.min / 1e6).toFixed(3)}–{(v.max / 1e6).toFixed(3)}]
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
          <div
            className="mt-4 border border-neutral-700 p-3"
            data-testid="environment-card"
          >
            <h3>Measurement environment</h3>
            <p>
              {s.environment.cpu} · {s.environment.os}/{s.environment.arch} ·{" "}
              {s.network} · {s.protocol} · {s.config.mode} · pacer{" "}
              {s.config.pacer}
            </p>
            <p>{s.config.pinning_notes}</p>
            <p>
              Go: {s.environment.go}; local compiler (not proof of remote
              build): {s.environment.rustc_local}
            </p>
            <p>
              Measured git SHA: {s.git_sha} · {s.created_utc}
            </p>
            <p>Source: {s.source}</p>
            <p className="break-all">Artifact SHA-256: {s.source_sha256}</p>
          </div>
        </>
      )}
    </section>
  );
}

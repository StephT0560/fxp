// SPDX-License-Identifier: MIT
import { fromJson } from "@bufbuild/protobuf";
import { useEffect, useRef, useState } from "react";
import { FXPMessageSchema } from "../gen/fxp_pb";
import { applyScenarioMessage } from "../lib/scenarios";
import { useOrders } from "../lib/stores";
import { formatPrice } from "../lib/format";
import { Ladder } from "./Ladder";
import { TapeView } from "./TapeView";
import { AuctionBanner } from "./AuctionBanner";

interface Scenario {
  id: string;
  name: string;
  description: string;
}
interface Run {
  run_id: string;
  scenario: string;
  symbol: string;
  seed: number;
  state: string;
}
interface Event {
  seq: number;
  kind: string;
  step?: string;
  panel?: string;
  status?: string;
  message?: string;
  outcome?: string;
  elapsed_ms: number;
  frame?: unknown;
  actor?: string;
}
function ObserverBlotter({ symbol }: { symbol: string }) {
  useOrders((s) => s.version);
  const rows = [...useOrders.getState().blotter.rows.values()].filter(
    (r) => r.symbol === symbol,
  );
  return (
    <section className="overflow-auto p-3" aria-label="Scenario blotter">
      <h3>Scenario users' orders · observer view</h3>
      <table className="w-full text-left text-xs">
        <thead>
          <tr>
            <th>Order</th>
            <th>State</th>
            <th>Cum</th>
            <th>Leaves</th>
            <th>Average</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.orderId}>
              <td>{r.orderId.slice(r.orderId.indexOf("-") + 1)}</td>
              <td>
                {r.state}
                {r.restated ? " · restated" : ""}
              </td>
              <td>{r.cum}</td>
              <td>{r.leaves}</td>
              <td>{formatPrice(r.avgPx, 2)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
export default function ScenariosPage() {
  const [list, setList] = useState<Scenario[]>([]);
  const [run, setRun] = useState<Run>();
  const [seed, setSeed] = useState("71");
  const [events, setEvents] = useState<Event[]>([]);
  const [error, setError] = useState("");
  const [streamState, setStreamState] = useState("");
  const [busy, setBusy] = useState(false);
  const source = useRef<EventSource>();
  const current = useRef<Run>();
  const cursor = useRef(0);
  function watch(next: Run) {
    source.current?.close();
    current.current = next;
    cursor.current = 0;
    setRun(next);
    setEvents([]);
    setStreamState("Connecting narration…");
    const stream = new EventSource(
      `/api/scenarios/${next.scenario}/events?run=${encodeURIComponent(next.run_id)}`,
    );
    source.current = stream;
    stream.onopen = () => setStreamState("Narration connected");
    stream.onerror = () => {
      if (current.current?.state === "running")
        setStreamState(
          "Narration interrupted; reconnecting with event replay…",
        );
    };
    stream.onmessage = (message) => {
      try {
        const event = JSON.parse(message.data) as Event;
        if (
          current.current?.run_id !== next.run_id ||
          event.seq <= cursor.current
        )
          return;
        cursor.current = event.seq;
        if (event.kind === "frame" && event.frame) {
          const msg = fromJson(
            FXPMessageSchema,
            event.frame as Parameters<
              typeof fromJson<typeof FXPMessageSchema>
            >[1],
          );
          applyScenarioMessage(msg);
        } else {
          setEvents((old) => [...old, event]);
          if (event.kind === "complete") {
            const completed = { ...next, state: event.status ?? "failed" };
            current.current = completed;
            setRun(completed);
            setStreamState("Narration complete");
            stream.close();
          }
        }
      } catch {
        setError(
          "Could not decode a scenario observation. The timeline is incomplete.",
        );
        stream.close();
      }
    };
  }
  useEffect(() => {
    let active = true;
    fetch("/api/scenarios")
      .then((r) => {
        if (!r.ok) throw Error("Could not load scenarios");
        return r.json();
      })
      .then((d: { scenarios: Scenario[]; run?: Run }) => {
        if (!active) return;
        setList(d.scenarios);
        if (d.run) watch(d.run);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
      source.current?.close();
    };
  }, []);
  async function start(id: string) {
    if (!/^-?\d+$/.test(seed) || !Number.isSafeInteger(Number(seed))) {
      setError("Seed must be a safe integer.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const res = await fetch(`/api/scenarios/${id}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ seed: Number(seed) }),
      });
      if (!res.ok) throw Error((await res.text()).trim());
      watch((await res.json()) as Run);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Scenario could not start");
    } finally {
      setBusy(false);
    }
  }
  async function stop() {
    if (!run) return;
    setBusy(true);
    try {
      const res = await fetch(
        `/api/scenarios/${run.scenario}/stop?run=${run.run_id}`,
        { method: "POST" },
      );
      if (!res.ok) throw Error((await res.text()).trim());
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not stop scenario");
    } finally {
      setBusy(false);
    }
  }
  const running = run?.state === "running";
  const focus = running
    ? events.filter((e) => e.kind === "step").at(-1)?.panel
    : undefined;
  const panelClass = (name: string) =>
    `min-h-0 overflow-auto border ${focus === name ? "border-sky-400 ring-2 ring-sky-500" : "border-neutral-800"}`;
  // Merge each running/result event into one visible, self-checking timeline row.
  const steps = new Map<string, Event>();
  for (const e of events) if (e.kind === "step" && e.step) steps.set(e.step, e);
  return (
    <main
      className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4"
      aria-label="Scenarios"
    >
      <h2>Seeded scenarios</h2>
      <p>
        Real server-side demo users trade on a fresh SCN-* book. You watch;
        assertions determine pass or fail. Stop the simulator before running a
        scenario.
      </p>
      <div className="flex items-center gap-4">
        <label>
          Seed{" "}
          <input
            aria-label="Scenario seed"
            className="rounded bg-neutral-800 px-2 py-1"
            value={seed}
            onChange={(e) => setSeed(e.target.value)}
            disabled={running || busy}
          />
        </label>
        {running && (
          <button onClick={() => void stop()} disabled={busy}>
            Stop scenario
          </button>
        )}
      </div>
      <div className="grid grid-cols-1 gap-2 md:grid-cols-2 xl:grid-cols-3">
        {list.map((s) => (
          <article className="border border-neutral-800 p-2" key={s.id}>
            <h3>{s.name}</h3>
            <p className="text-xs text-neutral-400">{s.description}</p>
            <button
              data-testid={`scenario-run-${s.id}`}
              disabled={running || busy}
              onClick={() => void start(s.id)}
            >
              Run {s.name}
            </button>
          </article>
        ))}
      </div>
      {error && (
        <p role="alert" className="text-red-400">
          {error}
        </p>
      )}
      {run && (
        <>
          <p data-testid="scenario-state" data-state={run.state}>
            {run.state} · {run.scenario} · seed {run.seed} ·{" "}
            <span data-testid="scenario-symbol">{run.symbol}</span>
          </p>
          <p className="text-xs text-neutral-400">{streamState}</p>
          <ol aria-live="polite" data-testid="scenario-timeline">
            {[...steps.values()].map((e) => (
              <li
                className="border-l-2 border-neutral-700 pl-3 my-2"
                key={e.step}
                data-testid="scenario-step"
                data-status={e.status}
                data-outcome={e.outcome ?? ""}
              >
                <span className="num text-xs text-neutral-400">
                  +{(e.elapsed_ms / 1000).toFixed(3)}s
                </span>{" "}
                ·{" "}
                <strong>
                  {e.status}: {e.step}
                </strong>
                <p>{e.message}</p>
              </li>
            ))}
          </ol>
          {events
            .filter((e) => e.kind === "complete")
            .map((e) => (
              <p
                key={e.seq}
                role="status"
                className={
                  e.status === "failed" ? "text-red-400" : "text-emerald-400"
                }
              >
                {e.message}
              </p>
            ))}
          {run.scenario === "stp-modes" && (
            <div
              className="grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-5"
              aria-label="STP mode comparison"
            >
              {[...steps.values()]
                .filter((e) => e.step?.includes(": identical setup"))
                .map((e) => (
                  <article
                    className="border border-neutral-700 p-2 text-xs"
                    key={e.step}
                  >
                    <strong>{e.step?.split(":")[0]}</strong>
                    <p>
                      {e.status}: {e.outcome || "Checking real reports…"}
                    </p>
                  </article>
                ))}
            </div>
          )}
          {events.some((e) => e.kind === "evidence") && (
            <details open data-testid="scenario-evidence">
              <summary>Observed FIX recovery fields</summary>
              {events
                .filter((e) => e.kind === "evidence")
                .map((e) => (
                  <pre
                    className="whitespace-pre-wrap break-all text-xs"
                    key={e.seq}
                  >
                    {e.message}
                  </pre>
                ))}
            </details>
          )}
          <AuctionBanner symbol={run.symbol} scale={2} />
          <div className="grid min-h-64 grid-cols-1 gap-2 xl:grid-cols-3">
            <div
              data-testid="scenario-ladder"
              data-highlighted={focus === "ladder"}
              className={panelClass("ladder")}
            >
              <Ladder symbol={run.symbol} scale={2} />
            </div>
            <div
              data-testid="scenario-tape"
              data-highlighted={focus === "tape"}
              className={panelClass("tape")}
            >
              <TapeView symbol={run.symbol} scale={2} />
            </div>
            <div
              data-testid="scenario-blotter"
              data-highlighted={focus === "blotter"}
              className={panelClass("blotter")}
            >
              <ObserverBlotter symbol={run.symbol} />
            </div>
          </div>
        </>
      )}
    </main>
  );
}

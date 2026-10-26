// SPDX-License-Identifier: MIT
import { createChart, LineSeries, type IChartApi, type ISeriesApi, type UTCTimestamp } from "lightweight-charts";
import { useEffect, useRef } from "react";
import type { Print } from "../lib/tape";
import { useTape } from "../lib/stores";

/** One point per second (last print wins), oldest first, as lightweight-charts requires. */
export function chartPoints(prints: Print[], scale: number): { time: UTCTimestamp; value: number }[] {
  const out: { time: UTCTimestamp; value: number }[] = [];
  const div = 10 ** scale;
  for (let i = prints.length - 1; i >= 0; i--) {
    const p = prints[i]!;
    const time = Math.floor(p.timeMs / 1000) as UTCTimestamp;
    const value = Number(p.price) / div;
    const last = out[out.length - 1];
    if (last && last.time >= time) last.value = value;
    else out.push({ time, value });
  }
  return out;
}

export function Chart({ symbol, scale }: { symbol: string; scale: number }) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<IChartApi>(undefined);
  const series = useRef<ISeriesApi<"Line">>(undefined);
  const version = useTape((s) => s.version);

  useEffect(() => {
    if (!el.current) return;
    const c = createChart(el.current, {
      autoSize: true,
      layout: { background: { color: "#0a0a0a" }, textColor: "#a3a3a3" },
      grid: { vertLines: { color: "#1f1f1f" }, horzLines: { color: "#1f1f1f" } },
      timeScale: { timeVisible: true, secondsVisible: true },
    });
    chart.current = c;
    series.current = c.addSeries(LineSeries, { color: "#60a5fa", lineWidth: 2, priceFormat: { type: "price", precision: scale, minMove: 1 / 10 ** scale } });
    return () => {
      c.remove();
      chart.current = undefined;
      series.current = undefined;
    };
  }, [symbol, scale]);

  useEffect(() => {
    const prints = useTape.getState().tapes.get(symbol)?.prints ?? [];
    series.current?.setData(chartPoints(prints, scale));
  }, [symbol, scale, version]);

  return <div ref={el} className="min-h-0 flex-1" data-testid="chart" />;
}

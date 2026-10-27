// SPDX-License-Identifier: MIT
import { createChart, LineSeries, type IChartApi, type ISeriesApi, type UTCTimestamp } from "lightweight-charts";
import { useEffect, useRef } from "react";
import type { Print } from "../lib/tape";
import { chartViewport } from "../lib/chart-viewport";
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
  const manual = useRef(false);
  const previousMode = useRef("keep");
  const previousCount = useRef(0);
  const version = useTape((s) => s.version);

  useEffect(() => {
    if (!el.current) return;
    manual.current = false;
    previousMode.current = "keep";
    previousCount.current = 0;
    const c = createChart(el.current, {
      autoSize: true,
      layout: { background: { color: "#0a0a0a" }, textColor: "#a3a3a3" },
      grid: { vertLines: { color: "#1f1f1f" }, horzLines: { color: "#1f1f1f" } },
      rightPriceScale: { scaleMargins: { top: 0.15, bottom: 0.15 } },
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
    const points = chartPoints(prints, scale);
    const mode = chartViewport(points.length, manual.current);
    series.current?.setData(points);
    const time = chart.current?.timeScale();
    if (mode === "fit" && (previousMode.current !== mode || previousCount.current !== points.length)) time?.fitContent();
    if (mode === "follow" && previousMode.current !== mode) {
      time?.applyOptions({ barSpacing: 6, rightOffset: 0 });
      time?.scrollToPosition(0, false);
    }
    previousMode.current = mode;
    previousCount.current = points.length;
  }, [symbol, scale, version]);

  const markManual = () => {
    manual.current = true;
    chart.current?.timeScale().applyOptions({ shiftVisibleRangeOnNewBar: false });
  };
  const empty = !(useTape.getState().tapes.get(symbol)?.prints.length);
  return (
    <div className="relative min-h-0 flex-1" data-testid="chart"
      onPointerDownCapture={markManual}
      onWheelCapture={markManual}
      onTouchStartCapture={markManual}>
      <div ref={el} className="absolute inset-0" />
      {empty && <div className="pointer-events-none absolute inset-0 flex items-center justify-center text-sm text-neutral-500">No trades yet</div>}
    </div>
  );
}

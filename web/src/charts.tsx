import { useEffect, useRef, useState, type ReactNode } from "react";
import { usdCompact, usdShort, when } from "./format";

/** Tracks the rendered width of a container so SVG charts can lay out in real pixels. */
function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useEffect(() => {
    if (!ref.current) return;
    const ro = new ResizeObserver((e) => setW(Math.floor(e[0].contentRect.width)));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  return [ref, w] as const;
}

function niceTicks(min: number, max: number, count = 4): number[] {
  if (max <= min) max = min + 1;
  const raw = (max - min) / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag;
  const out: number[] = [];
  for (let v = Math.ceil(min / step) * step; v <= max + step * 1e-9; v += step) out.push(v);
  return out;
}

function Tip({ x, y, width, children }: { x: number; y: number; width: number; children: ReactNode }) {
  const flip = x > width - 190;
  return (
    <div className="tip" style={{ left: flip ? undefined : x + 12, right: flip ? width - x + 12 : undefined, top: Math.max(y - 12, 0) }}>
      {children}
    </div>
  );
}

/** Cumulative P&L: one series, so no legend; crosshair and tooltip on hover. */
export function EquityChart({ points }: { points: { ts: string; pnlUSD: number }[] }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const H = 240;
  const m = { l: 56, r: 76, t: 12, b: 26 };
  const data = points.map((p) => ({ t: Date.parse(p.ts), v: p.pnlUSD, ts: p.ts }));
  if (data.length === 0) return <div ref={ref} className="empty">No held flags in this run.</div>;

  const t0 = data[0].t;
  const t1 = Math.max(data[data.length - 1].t, t0 + 1);
  const vMax = Math.max(...data.map((d) => d.v), 1);
  const ticks = niceTicks(0, vMax);
  const top = Math.max(ticks[ticks.length - 1], vMax);
  const iw = Math.max(width - m.l - m.r, 10);
  const ih = H - m.t - m.b;
  const X = (t: number) => m.l + ((t - t0) / (t1 - t0)) * iw;
  const Y = (v: number) => m.t + ih - (v / top) * ih;

  // Step line: P&L only changes when a trade is filled.
  let path = `M${X(t0)},${Y(0)}`;
  for (const d of data) path += `H${X(d.t)}V${Y(d.v)}`;
  const last = data[data.length - 1];
  const area = `${path}V${Y(0)}H${X(t0)}Z`;

  const dayTicks: number[] = [];
  const day = 86400000;
  const span = t1 - t0;
  const every = span > 12 * day ? 3 * day : span > 5 * day ? 2 * day : day;
  for (let t = Math.ceil(t0 / day) * day; t <= t1; t += every) dayTicks.push(t);

  const h = hover === null ? null : data[hover];
  return (
    <div
      ref={ref}
      className="chart"
      onMouseLeave={() => setHover(null)}
      onMouseMove={(e) => {
        const r = e.currentTarget.getBoundingClientRect();
        const t = t0 + ((e.clientX - r.left - m.l) / iw) * (t1 - t0);
        let idx = -1;
        for (let i = 0; i < data.length && data[i].t <= t; i++) idx = i;
        setHover(idx < 0 ? null : idx);
      }}
    >
      {width > 0 && (
        <svg width={width} height={H} role="img" aria-label="Cumulative profit and loss over time">
          {ticks.map((v) => (
            <g key={v}>
              <line x1={m.l} x2={m.l + iw} y1={Y(v)} y2={Y(v)} className={v === 0 ? "axis" : "grid"} />
              <text x={m.l - 8} y={Y(v) + 4} textAnchor="end" className="tick">
                {usdCompact(v)}
              </text>
            </g>
          ))}
          {dayTicks.map((t) => (
            <text key={t} x={X(t)} y={H - 8} textAnchor="middle" className="tick">
              {when(new Date(t).toISOString()).slice(0, 6)}
            </text>
          ))}
          <path d={area} className="area" />
          <path d={path} className="line" />
          {h && <line x1={X(h.t)} x2={X(h.t)} y1={m.t} y2={m.t + ih} className="crosshair" />}
          <circle cx={X(h ? h.t : last.t)} cy={Y(h ? h.v : last.v)} r={5} className="dot" />
          {!h && (
            <text x={X(last.t) + 10} y={Y(last.v) + 4} className="endlabel">
              {usdCompact(last.v)}
            </text>
          )}
        </svg>
      )}
      {h && (
        <Tip x={X(h.t)} y={Y(h.v)} width={width}>
          <b>{usdCompact(h.v)}</b> cumulative
          <br />
          <span className="muted">
            after {hover! + 1} held flag{hover ? "s" : ""} · {when(h.ts)} UTC
          </span>
        </Tip>
      )}
    </div>
  );
}

/** Columns for a short ordered axis (tenors). Value on the cap, hover for the detail. */
export function ColumnChart({ rows, detail }: { rows: { key: string; value: number }[]; detail: (key: string) => ReactNode }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const H = 240;
  const m = { l: 12, r: 12, t: 24, b: 26 };
  const vMax = Math.max(...rows.map((r) => r.value), 1);
  const iw = Math.max(width - m.l - m.r, 10);
  const ih = H - m.t - m.b;
  const band = iw / Math.max(rows.length, 1);
  const bw = Math.min(24, band * 0.6);
  const Y = (v: number) => m.t + ih - (v / vMax) * ih;
  return (
    <div ref={ref} className="chart" onMouseLeave={() => setHover(null)}>
      {width > 0 && (
        <svg width={width} height={H} role="img" aria-label="Profit and loss by tenor">
          <line x1={m.l} x2={m.l + iw} y1={Y(0)} y2={Y(0)} className="axis" />
          {rows.map((r, i) => {
            const cx = m.l + band * (i + 0.5);
            const y = Y(r.value);
            const hgt = Y(0) - y;
            const rad = Math.min(4, hgt);
            return (
              <g key={r.key} onMouseEnter={() => setHover(i)}>
                <rect x={m.l + band * i} y={m.t - 20} width={band} height={ih + 20} fill="transparent" />
                {hgt > 0.5 && (
                  <path
                    d={`M${cx - bw / 2},${Y(0)}V${y + rad}q0,${-rad} ${rad},${-rad}H${cx + bw / 2 - rad}q${rad},0 ${rad},${rad}V${Y(0)}Z`}
                    className={hover === i ? "bar hot" : "bar"}
                  />
                )}
                <text x={cx} y={y - 6} textAnchor="middle" className="value">
                  {r.value > 0 ? usdShort(r.value) : "—"}
                </text>
                <text x={cx} y={H - 8} textAnchor="middle" className="tick">
                  {r.key}
                </text>
              </g>
            );
          })}
        </svg>
      )}
      {hover !== null && (
        <Tip x={m.l + band * (hover + 0.5)} y={Y(rows[hover].value)} width={width}>
          {detail(rows[hover].key)}
        </Tip>
      )}
    </div>
  );
}

/** Ranked horizontal bars (pairs). Value at the tip. */
export function BarList({ rows, detail }: { rows: { key: string; value: number }[]; detail: (key: string) => ReactNode }) {
  const [hover, setHover] = useState<number | null>(null);
  const vMax = Math.max(...rows.map((r) => r.value), 1);
  if (rows.length === 0) return <div className="empty">No held flags in this run.</div>;
  return (
    <div className="barlist" onMouseLeave={() => setHover(null)}>
      {rows.map((r, i) => (
        <div key={r.key} className="barrow" onMouseEnter={() => setHover(i)}>
          <span className="barkey">{r.key}</span>
          <span className="bartrack">
            <span className={hover === i ? "barfill hot" : "barfill"} style={{ width: `${Math.max((r.value / vMax) * 100, 0.5)}%` }} />
            <span className="barval">{usdCompact(r.value)}</span>
          </span>
          {hover === i && <div className="tip rowtip">{detail(r.key)}</div>}
        </div>
      ))}
    </div>
  );
}

/**
 * Where the market forward sits against the no-arbitrage band, in forward points.
 * Inside the band there is no trade; outside it the gap is the locked-in edge.
 */
export function ParityGauge(p: { bandLow: number; bandHigh: number; fair: number; bid: number; ask: number; side: string; fmt: (v: number) => string }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const H = 84;
  const lo = Math.min(p.bandLow, p.bid);
  const hi = Math.max(p.bandHigh, p.ask);
  const padV = (hi - lo) * 0.18 || 1;
  const X = (v: number) => 12 + ((v - (lo - padV)) / (hi - lo + 2 * padV)) * Math.max(width - 24, 10);
  const outside = p.bid > p.bandHigh || p.ask < p.bandLow;
  const midBand = (X(p.bandLow) + X(p.bandHigh)) / 2;
  const midMkt = (X(p.bid) + X(p.ask)) / 2;
  const clampX = (x: number, half: number) => Math.min(Math.max(x, half), Math.max(width - half, half));
  return (
    <div ref={ref} className="chart">
      {width > 0 && (
        <svg width={width} height={H} role="img" aria-label="Market forward points against the no-arbitrage band">
          <text x={clampX(midMkt, 70)} y={14} textAnchor="middle" className="value">
            Mkt pts {p.fmt(p.bid)} / {p.fmt(p.ask)}
          </text>
          <rect x={X(p.bid)} y={24} width={Math.max(X(p.ask) - X(p.bid), 3)} height={10} rx={3} className={outside ? "gauge-mkt " + p.side : "gauge-mkt"} />
          <rect x={X(p.bandLow)} y={44} width={Math.max(X(p.bandHigh) - X(p.bandLow), 3)} height={10} rx={3} className="gauge-band" />
          <line x1={X(p.fair)} x2={X(p.fair)} y1={40} y2={58} className="gauge-fair" />
          <text x={clampX(midBand, 120)} y={76} textAnchor="middle" className="tick">
            No-arb band {p.fmt(p.bandLow)} to {p.fmt(p.bandHigh)} · parity {p.fmt(p.fair)}
          </text>
        </svg>
      )}
    </div>
  );
}

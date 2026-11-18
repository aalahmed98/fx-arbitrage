import type { Pair, Quote } from "./api";

export const usd = (v: number) => "$" + Math.round(v).toLocaleString("en-US");

export function usdCompact(v: number) {
  const a = Math.abs(v);
  const sign = v < 0 ? "-" : "";
  if (a >= 1e9) return `${sign}$${(a / 1e9).toFixed(2)}B`;
  if (a >= 1e6) return `${sign}$${(a / 1e6).toFixed(2)}M`;
  if (a >= 1e4) return `${sign}$${(a / 1e3).toFixed(1)}K`;
  return `${sign}$${Math.round(a).toLocaleString("en-US")}`;
}

/** Short enough to sit on top of a chart column: $109K, $1.1M. */
export function usdShort(v: number) {
  const a = Math.abs(v);
  if (a >= 1e9) return `$${(v / 1e9).toFixed(1)}B`;
  if (a >= 1e6) return `$${(v / 1e6).toFixed(1)}M`;
  if (a >= 1e3) return `$${Math.round(v / 1e3)}K`;
  return `$${Math.round(v)}`;
}

/** Notional as a dealer writes it: $10M, $2.5M. */
export const size = (v: number) => `$${+(v / 1e6).toFixed(2)}M`;

export function bps(v: number, digits = 1) {
  const s = Math.abs(v).toFixed(digits);
  if (Number(s) === 0) return s; // no sign on a rounded zero
  return (v > 0 ? "+" : "−") + s;
}

/** Whole basis points for a grid cell; four digits and up are shortened (−37k) so a broken quote cannot widen a column. */
export function bpsCell(v: number) {
  const a = Math.abs(v);
  if (a < 1000) return bps(v, 0);
  return (v > 0 ? "+" : "−") + (a < 9950 ? (a / 1000).toFixed(1) : Math.round(a / 1000)) + "k";
}

export const pct = (v: number) => `${v.toFixed(3)}%`;

export const px = (v: number, pair?: Pair) => v.toFixed(pair && pair.pipFactor === 100 ? 3 : 5);

export const pts = (v: number) => v.toFixed(Math.abs(v) >= 100 ? 1 : 2);

const pad = (n: number) => String(n).padStart(2, "0");

/** 09 Nov 14:31 (UTC) */
export function when(iso: string, withSeconds = false) {
  if (!iso || iso.startsWith("0001")) return "—";
  const d = new Date(iso);
  const mon = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][d.getUTCMonth()];
  return `${pad(d.getUTCDate())} ${mon} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}${withSeconds ? ":" + pad(d.getUTCSeconds()) : ""}`;
}

/** 14:31 (UTC) */
export function hhmm(iso: string) {
  if (!iso || iso.startsWith("0001")) return "—";
  const d = new Date(iso);
  return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}

export function duration(sec: number) {
  if (sec < 60) return `${Math.round(sec)}s`;
  if (sec < 3600) return `${Math.round(sec / 60)}m`;
  return `${Math.floor(sec / 3600)}h ${Math.round((sec % 3600) / 60)}m`;
}

/** ISO string -> value for <input type="datetime-local"> (UTC) and back. */
export const toLocalInput = (iso: string) => iso.slice(0, 16);
export const fromLocalInput = (v: string) => (v.length === 16 ? v + ":00Z" : v + "Z");

export const pairLabel = (symbol: string) => `${symbol.slice(0, 3)}/${symbol.slice(3)}`;

/** Borrowing the quote currency means selling the forward, so the forward is rich; the other way it is cheap. */
export const isRich = (direction: string) => direction === "BORROW_QUOTE";
export const side = (direction: string) => (isRich(direction) ? "rich" : "cheap");
export const sideLabel = (direction: string) => (isRich(direction) ? "Fwd rich" : "Fwd cheap");
export const sideAction = (direction: string) => (isRich(direction) ? "buy spot, sell fwd" : "sell spot, buy fwd");

export function tradeLabel(direction: string, pair: string) {
  const base = pair.slice(0, 3);
  const quote = pair.slice(3);
  return isRich(direction) ? `borrow ${quote}, lend ${base}` : `borrow ${base}, lend ${quote}`;
}

/** The four legs in dealing order. Each is [what to do, the level, a note on which side or how it is built]. */
export function legs(q: Quote, pair: Pair): [string, string, string][] {
  const { base, quote } = pair;
  const spread = `${q.borrowBps.toFixed(0)}bp`;
  if (isRich(q.direction)) {
    return [
      [`Borrow ${quote} ${q.days}d`, pct(q.rateQuote + q.borrowBps / 100), `${pct(q.rateQuote)} + ${spread}`],
      [`Buy ${base} spot`, px(q.spotAsk, pair), "offer"],
      [`Deposit ${base} ${q.days}d`, pct(q.rateBase), ""],
      [`Sell ${base} fwd`, px(q.fwdBid, pair), `bid, pts ${pts(q.ptsBid)}`],
    ];
  }
  return [
    [`Borrow ${base} ${q.days}d`, pct(q.rateBase + q.borrowBps / 100), `${pct(q.rateBase)} + ${spread}`],
    [`Sell ${base} spot`, px(q.spotBid, pair), "bid"],
    [`Deposit ${quote} ${q.days}d`, pct(q.rateQuote), ""],
    [`Buy ${base} fwd`, px(q.fwdAsk, pair), `offer, pts ${pts(q.ptsAsk)}`],
  ];
}

/** Plain-text ticket for pasting into chat. */
export function ticketText(q: Quote, pair: Pair) {
  const head = `${pair.base}/${pair.quote} ${q.tenor} (${q.days}d) ${sideLabel(q.direction).toLowerCase()}, net ${bps(q.edgeBps)} bp p.a. / ${pts(q.edgePips)} pips. Indicative, ${when(q.ts)} UTC.`;
  const lv = `Spot ${px(q.spotBid, pair)}/${px(q.spotAsk, pair)}, pts ${pts(q.ptsBid)}/${pts(q.ptsAsk)}, outright ${px(q.fwdBid, pair)}/${px(q.fwdAsk, pair)}, parity ${px(q.fairFwd, pair)} (pts ${pts(q.fairPts)}).`;
  const steps = legs(q, pair).map(([a, b, c], i) => `${i + 1}. ${a} @ ${b}${c ? ` (${c})` : ""}`);
  return [head, lv, ...steps].join("\n");
}

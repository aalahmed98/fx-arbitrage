// Types mirror the Go structs in internal/engine and internal/backtest.

export interface Config {
  applyBorrowSpread: boolean;
  borrowSpreadBps: number;
  borrowSpreadByCcy: Record<string, number> | null;
  minEdgeBps: number;
  
    maxStaleSec: number;
  notionalUSD: number;
  confirmSec: number;
  reference: "parity" | "normal";
  normalHalfLifeMin: number;
  normalWarmupMin: number;
}

export interface Pair {
  symbol: string;
  base: string;
  quote: string;
    pipFactor: number;
  spotLag: number;
  group: string;
}

export interface Currency {
  code: string;
  name: string;
    dayBasis: number;
  bank: string;
  /** True when the BIS has no policy rate for this currency and it is entered by hand. */
  manualRate: boolean;
}

export interface FeedSummary {
  source: string;
  last: string;
  pairsOk: number;
  pairsDown: string[];
  lagSec: number;
}

export interface DatasetInfo {
  name: string;
  label: string;
  dataFrom?: string;
  dataTo?: string;
  error?: string;
}

export interface Meta {
  live?: FeedSummary;
  datasets: DatasetInfo[];
  /** The data set currently selected in the dashboard (filled in by App). */
  dataset?: string;
    pairs: Pair[];
  groups: string[];
  tenors: { code: string }[];
  currencies: Currency[];
  defaults: Config;
  execDelaySec: number;
  dataFrom?: string;
  dataTo?: string;
  dataError?: string;
}

export interface Quote {
  ok: boolean;
  ts: string;
  pair: string;
  tenor: string;
  days: number;
  reason?: string;
  spotBid: number;
  spotAsk: number;
  ptsBid: number;
  ptsAsk: number;
  fwdBid: number;
  fwdAsk: number;
  rateBase: number;
  rateQuote: number;
  rateSrcBase: string;
  rateSrcQuote: string;
    mktFwd: number;
  fairFwd: number;
  fairPts: number;
  grossPips: number;
  edgePips: number;
  bandLow: number;
  bandHigh: number;
    deviationBps: number;
  
  normalBps: number;
  normalReady: boolean;
  reference: "parity" | "normal";
  direction: "BORROW_QUOTE" | "BORROW_BASE" | "";
  borrowCcy: string;
  borrowBps: number;
  edgeBps: number;
  edgePeriodBps: number;
  profitUSD: number;
  signal: boolean;
}

export interface Episode {
  id: number;
  pair: string;
  tenor: string;
  direction: "BORROW_QUOTE" | "BORROW_BASE";
  borrowCcy: string;
  days: number;
  detectedAt: string;
  endedAt: string;
  durationSec: number;
  edgeAtDetectBps: number;
  peakEdgeBps: number;
  deviationBps: number;
  status: "pending" | "filled" | "missed";
  fillAt: string;
  edgeAtFillBps: number;
  pnlUSD: number;
  detect: Quote;
  injected?: boolean;
}

export interface Bucket {
  key: string;
  alerts: number;
  trades: number;
  pnlUSD: number;
}

export interface Report {
  params: { from: string; to: string; config: Config; execDelaySec: number };
  dataFrom: string;
  dataTo: string;
  events: number;
  evaluations: number;
  wallMs: number;
  eventsPerSec: number;
  alerts: number;
  trades: number;
  missed: number;
  totalPnLUSD: number;
  avgPnLUSD: number;
  avgEdgeBps: number;
  medianDurSec: number;
  bestTradePnLUSD: number;
  byPair: Bucket[];
  byTenor: Bucket[];
  equity: { ts: string; pnlUSD: number }[];
  episodes: Episode[] | null;
}

export interface RateEntry extends Currency {
  policy: number | null;
  policySince: string;
  curve: Record<string, number>;
}

export interface Tick {
  ts: string;
  quotes: Quote[];
  alerts: number;
  trades: number;
  missed: number;
  pnlUSD: number;
  feed?: FeedSummary;
}

export function liveURL(p: { config: Config; execDelaySec: number }) {
  const q = new URLSearchParams({ execDelaySec: String(p.execDelaySec), config: JSON.stringify(p.config) });
  return "/api/live?" + q.toString();
}

async function json<T>(res: Response): Promise<T> {
  const body = await res.json();
  if (!res.ok) throw new Error(body?.error ?? res.statusText);
  return body as T;
}

export const getMeta = () => fetch("/api/meta").then((r) => json<Meta>(r));

export const getRates = (asof?: string, dataset?: string) => {
  const q = new URLSearchParams();
  if (asof) q.set("asof", asof);
  if (dataset) q.set("dataset", dataset);
  return fetch("/api/rates?" + q.toString()).then((r) => json<RateEntry[]>(r));
};

export const setRate = (body: { ccy: string; ratePct: number; since?: string }) =>
  fetch("/api/rates", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).then((r) =>
    json<{ ok: boolean }>(r),
  );

export const runBacktest = (body: {
  from: string;
  to: string;
  config: Config;
  execDelaySec: number;
  dataset?: string;
  pairs?: string[];
}) =>
  fetch("/api/backtest", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).then(
    (r) => json<Report>(r),
  );

export const controlStream = (sid: string, patch: { paused?: boolean; speed?: number }) =>
  fetch("/api/stream/control", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sid, ...patch }),
  });

export function streamURL(p: { sid: string; from: string; speed: number; config: Config; execDelaySec: number; dataset?: string }) {
  const q = new URLSearchParams({
    dataset: p.dataset ?? "",
    sid: p.sid,
    from: p.from,
    speed: String(p.speed),
    execDelaySec: String(p.execDelaySec),
    config: JSON.stringify(p.config),
  });
  return "/api/stream?" + q.toString();
}

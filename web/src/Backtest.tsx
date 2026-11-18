import { useMemo, useState } from "react";
import { runBacktest, type Config, type Episode, type Meta, type Report } from "./api";
import { BarList, ColumnChart, EquityChart } from "./charts";
import { bps, duration, fromLocalInput, pairLabel, side, sideLabel, size, toLocalInput, tradeLabel, usd, usdCompact, when } from "./format";

type SortKey = "detectedAt" | "pair" | "tenor" | "edgeAtDetectBps" | "edgeAtFillBps" | "durationSec" | "pnlUSD";

export function Backtest({ meta, cfg, execDelaySec, group }: { meta: Meta; cfg: Config; execDelaySec: number; group: string }) {
  const dataFrom = toLocalInput(meta.dataFrom ?? "");
  const dataTo = toLocalInput(meta.dataTo ?? "");
  const [from, setFrom] = useState(dataFrom);
  const [to, setTo] = useState(dataTo);
  const [rep, setRep] = useState<Report | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sort, setSort] = useState<{ key: SortKey; dir: 1 | -1 }>({ key: "pnlUSD", dir: -1 });
  const [status, setStatus] = useState("all");
  const [limit, setLimit] = useState(200);
  const [ranGroup, setRanGroup] = useState(group);

  // Quick windows, counted back from the last recorded quote and never before the first.
  const lastHours = (h: number) => {
    if (!meta.dataTo) return;
    const start = new Date(Date.parse(meta.dataTo) - h * 3600e3).toISOString();
    setFrom(toLocalInput(start) < dataFrom ? dataFrom : toLocalInput(start));
    setTo(dataTo);
  };
  const windows: [string, () => void][] = [
    ["Last 4h", () => lastHours(4)],
    ["Last 24h", () => lastHours(24)],
    ["All", () => (setFrom(dataFrom), setTo(dataTo))],
  ];

  async function run() {
    setBusy(true);
    setError(null);
    try {
      // "to" is exclusive on the server; add a minute so the last bar on screen is included.
      const end = new Date(Date.parse(fromLocalInput(to)) + 60000).toISOString();
      const pairs = group === "All" ? undefined : meta.pairs.filter((p) => p.group === group).map((p) => p.symbol);
      setRep(await runBacktest({ from: fromLocalInput(from), to: end, config: cfg, execDelaySec, dataset: meta.dataset, pairs }));
      setRanGroup(group);
      setLimit(200);
    } catch (e) {
      setError(String((e as Error).message));
    } finally {
      setBusy(false);
    }
  }

  const rows = useMemo(() => {
    const list = (rep?.episodes ?? []).filter((e) => status === "all" || e.status === status);
    const k = sort.key;
    return list.slice().sort((a, b) => {
      const x = a[k];
      const y = b[k];
      const c = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
      return c * sort.dir;
    });
  }, [rep, sort, status]);

  const th = (key: SortKey, label: string, num = false) => (
    <th className={num ? "num" : undefined} aria-sort={sort.key === key ? (sort.dir === 1 ? "ascending" : "descending") : undefined}>
      <button onClick={() => setSort((s) => ({ key, dir: s.key === key ? (s.dir === 1 ? -1 : 1) : -1 }))}>
        {label}
        {sort.key === key ? (sort.dir === 1 ? " ↑" : " ↓") : ""}
      </button>
    </th>
  );

  const stale =
    rep && (JSON.stringify(rep.params.config) !== JSON.stringify(cfg) || rep.params.execDelaySec !== execDelaySec);
  const groupName = (g: string) => (g === "All" ? "all pairs" : `${g} pairs`);
  const byTenor = new Map((rep?.byTenor ?? []).map((b) => [b.key, b]));
  const byPair = new Map((rep?.byPair ?? []).map((b) => [b.key, b]));
  const bucketTip = (b?: { key: string; alerts: number; trades: number; pnlUSD: number }, label?: string) =>
    b && (
      <>
        <b>{label ?? b.key}</b> · {usd(b.pnlUSD)}
        <br />
        <span className="muted">
          {b.alerts} flags, {b.trades} held
        </span>
      </>
    );

  return (
    <>
      <div className="strip">
        <div className="field">
          <label htmlFor="bt-from">From (UTC)</label>
          <input id="bt-from" type="datetime-local" value={from} min={dataFrom} max={dataTo} onChange={(e) => setFrom(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="bt-to">To (UTC)</label>
          <input id="bt-to" type="datetime-local" value={to} min={dataFrom} max={dataTo} onChange={(e) => setTo(e.target.value)} />
        </div>
        <div className="seg" role="group" aria-label="Quick window">
          {windows.map(([label, pick]) => (
            <button key={label} onClick={pick}>
              {label}
            </button>
          ))}
        </div>
        <button className="btn primary" onClick={run} disabled={busy}>
          {busy ? "Running…" : rep ? "Run again" : "Run back-test"}
        </button>
        <div className="sub" style={{ maxWidth: 520, marginLeft: "auto" }}>
          Replays recorded quotes minute by minute with the settings above, no look-ahead. Each flag is re-checked {execDelaySec}s
          later; held flags take potential P&amp;L at that moment's prices. Nothing is traded.
        </div>
      </div>
      {error && (
        <div className="banner err" role="alert" style={{ margin: "0 0 10px" }}>
          <b>Back-test failed</b>
          <span>{error}</span>
        </div>
      )}
      {stale && (
        <div className="banner" style={{ margin: "0 0 10px" }}>
          <b>Out of date</b>
          <span>Settings changed since this run. Run again to update.</span>
        </div>
      )}
      {rep && !stale && ranGroup !== group && (
        <div className="banner" style={{ margin: "0 0 10px" }}>
          <b>Showing {groupName(ranGroup)}</b>
          <span>Run again to back-test {groupName(group)}.</span>
        </div>
      )}

      {!rep && !busy && (
        <div className="panel empty">
          <b>No run yet</b>
          {meta.dataFrom
            ? `Recorded quotes run from ${when(meta.dataFrom)} to ${when(meta.dataTo ?? "")} UTC. Pick a window and run.`
            : "No recorded quotes yet. The history grows while the collector runs."}
        </div>
      )}
      {!rep && busy && <div className="panel empty">Replaying the recorded quotes…</div>}

      {rep && (
        <>
          <div className="stats wide">
            <Stat
              label="Potential P&L"
              value={usdCompact(rep.totalPnLUSD)}
              sub={`held flags dealt once at ${size(rep.params.config.notionalUSD)}`}
            />
            <Stat label="Flags" value={rep.alerts.toLocaleString()} sub={`${rep.trades} held, ${rep.missed} gone`} />
            <Stat
              label="Hold rate"
              value={rep.trades + rep.missed > 0 ? `${Math.round((100 * rep.trades) / (rep.trades + rep.missed))}%` : "—"}
              sub={`still there after ${execDelaySec}s`}
            />
            <Stat label="Avg per flag" value={usdCompact(rep.avgPnLUSD)} sub={`best ${usdCompact(rep.bestTradePnLUSD)}`} />
            <Stat label="Avg edge at re-check" value={`${rep.avgEdgeBps.toFixed(1)} bp`} sub="p.a., after costs" />
            <Stat label="Median gap life" value={duration(rep.medianDurSec)} sub="flag to close" />
          </div>

          <div className="two">
            <section className="panel">
              <header>
                <div>
                  <h2>Cumulative potential P&amp;L</h2>
                  <div className="sub">
                    {when(rep.dataFrom)} to {when(rep.dataTo)} UTC. One step per held flag.
                  </div>
                </div>
              </header>
              <EquityChart points={rep.equity} />
            </section>
            <section className="panel">
              <header>
                <div>
                  <h2>By tenor</h2>
                  <div className="sub">Longer tenors earn more per bp: the edge runs longer.</div>
                </div>
              </header>
              <ColumnChart rows={rep.byTenor.map((b) => ({ key: b.key, value: b.pnlUSD }))} detail={(k) => bucketTip(byTenor.get(k))} />
            </section>
          </div>

          <div className="two flip">
            <section className="panel">
              <header>
                <div>
                  <h2>By pair</h2>
                  <div className="sub">Top 12 of {rep.byPair.filter((b) => b.trades > 0).length} pairs with held flags.</div>
                </div>
              </header>
              <BarList
                rows={rep.byPair.filter((b) => b.pnlUSD > 0).slice(0, 12).map((b) => ({ key: pairLabel(b.key), value: b.pnlUSD }))}
                detail={(k) => bucketTip(byPair.get(k.replace("/", "")), k)}
              />
            </section>

            <section className="panel">
              <header>
                <div>
                  <h2>Flags</h2>
                  <div className="sub">
                    {Math.min(limit, rows.length).toLocaleString()} of {rows.length.toLocaleString()} shown
                  </div>
                </div>
                <select aria-label="Filter by outcome" value={status} onChange={(e) => setStatus(e.target.value)}>
                  <option value="all">All outcomes</option>
                  <option value="filled">Held</option>
                  <option value="missed">Gone</option>
                  <option value="pending">Unchecked at end</option>
                </select>
              </header>
              <div className="tablewrap">
                <table className="data">
                  <thead>
                    <tr>
                      {th("detectedAt", "Flagged (UTC)")}
                      {th("pair", "Pair")}
                      {th("tenor", "Tenor")}
                      <th>Side</th>
                      {th("edgeAtDetectBps", "Edge at flag", true)}
                      {th("edgeAtFillBps", "At re-check", true)}
                      {th("durationSec", "Lasted", true)}
                      <th>Outcome</th>
                      {th("pnlUSD", "Potential P&L", true)}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.slice(0, limit).map((e: Episode) => (
                      <tr key={e.id}>
                        <td>{when(e.detectedAt)}</td>
                        <td>{pairLabel(e.pair)}</td>
                        <td>{e.tenor}</td>
                        <td>
                          <span className={side(e.direction)}>{sideLabel(e.direction)}</span>{" "}
                          <span className="muted">{tradeLabel(e.direction, e.pair)}</span>
                        </td>
                        <td className="num">{bps(e.edgeAtDetectBps)} bp</td>
                        <td className="num">{e.status === "filled" ? `${bps(e.edgeAtFillBps)} bp` : "—"}</td>
                        <td className="num">{duration(e.durationSec)}</td>
                        <td>
                          <span className={"chip " + e.status}>
                            {e.status === "filled" ? "Held" : e.status === "missed" ? "Gone" : "Unchecked"}
                          </span>
                        </td>
                        <td className="num">{e.status === "filled" ? usd(e.pnlUSD) : "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {rows.length === 0 && <div className="empty">No flags with this outcome in the run.</div>}
              </div>
              {rows.length > limit && (
                <button className="btn" style={{ marginTop: 8 }} onClick={() => setLimit((l) => l + 500)}>
                  Show 500 more
                </button>
              )}
            </section>
          </div>
          <p className="sub muted" style={{ margin: 0 }}>
            Scanned {rep.events.toLocaleString()} ticks in {(rep.wallMs / 1000).toFixed(1)}s (
            {rep.eventsPerSec >= 1e6 ? `${(rep.eventsPerSec / 1e6).toFixed(1)}M` : `${Math.round(rep.eventsPerSec / 1e3)}K`} a second).
            Potential P&amp;L ignores credit lines, balance-sheet cost and settlement risk.
          </p>
        </>
      )}
    </>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <div className="stat">
      <small>{sub}</small>
      <b>{value}</b>
      <span>{label}</span>
    </div>
  );
}

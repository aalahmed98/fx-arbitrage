import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  controlStream,
  liveURL,
  streamURL,
  type Config,
  type Episode,
  type Meta,
  type Pair,
  type Quote,
  type Tick,
} from "./api";
import { ParityGauge } from "./charts";
import {
  bps,
  bpsCell,
  duration,
  fromLocalInput,
  hhmm,
  isRich,
  legs,
  pairLabel,
  pct,
  pts,
  px,
  side,
  sideAction,
  sideLabel,
  size,
  ticketText,
  toLocalInput,
  tradeLabel,
  usd,
  usdCompact,
  when,
} from "./format";

type RunState = "idle" | "playing" | "paused" | "ended";

const SPEEDS = [
  { v: 60, label: "1 min / sec" },
  { v: 300, label: "5 min / sec" },
  { v: 1800, label: "30 min / sec" },
  { v: 0, label: "As fast as possible" },
];

const isOpen = (e: Episode) => !e.endedAt || e.endedAt.startsWith("0001");

/** Background for the gross-gap view: blue = forward cheap, orange = forward rich, neutral = parity inside the bid/offer. */
function divergingFill(dev: number) {
  const a = Math.abs(dev);
  const step = a < 3 ? 0 : a < 10 ? 22 : a < 25 ? 42 : a < 50 ? 62 : 82;
  if (step === 0) return "var(--div-mid)";
  return `color-mix(in oklab, var(${dev > 0 ? "--div-pos" : "--div-neg"}) ${step}%, var(--div-mid))`;
}

export function Scanner(props: {
  meta: Meta;
  cfg: Config;
    execDelaySec: number;
  group: string;
  onClock: (ts: string) => void;
}) {
  const { meta, cfg, execDelaySec } = props;
  const [state, setState] = useState<RunState>("idle");
  const [mode, setMode] = useState<"live" | "replay">(
    meta.live ? "live" : "replay",
  );
  const [speed, setSpeed] = useState(300);
  const [startAt, setStartAt] = useState(toLocalInput(meta.dataFrom ?? ""));
  const [tick, setTick] = useState<Tick | null>(null);
  const [episodes, setEpisodes] = useState<Episode[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [view, setView] = useState<"edge" | "dev">("edge");
  const [error, setError] = useState<string | null>(null);
  const [sound, setSound] = useState(false);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 10000);
    return () => window.clearInterval(id);
  }, []);

  // Optional tone on a new flag, for a dashboard left on a side monitor. At most one every two seconds.
  const audio = useRef<AudioContext | null>(null);
  const lastChime = useRef(0);
  const soundOn = useRef(sound);
  soundOn.current = sound;
  const seen = useRef(new Set<number>());
  const chime = useCallback((force = false) => {
    if (!force && (!soundOn.current || Date.now() - lastChime.current < 2000)) return;
    lastChime.current = Date.now();
    try {
      const ctx = (audio.current ??= new AudioContext());
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.frequency.value = 880;
      gain.gain.setValueAtTime(0.0001, ctx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.12, ctx.currentTime + 0.01);
      gain.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + 0.3);
      osc.connect(gain).connect(ctx.destination);
      osc.start();
      osc.stop(ctx.currentTime + 0.32);
    } catch {
      /* no audio device: stay silent */
    }
  }, []);

  const es = useRef<EventSource | null>(null);
  const sid = useRef("");
  const clock = useRef<string | null>(null);
  const live = useRef({ cfg, execDelaySec, speed, state, mode });
  live.current = { cfg, execDelaySec, speed, state, mode };

  const retry = useRef<number | undefined>(undefined);
  const stop = useCallback(() => {
    window.clearTimeout(retry.current);
    es.current?.close();
    es.current = null;
  }, []);

  // start(from) replays history from a timestamp; start(null) attaches to the live feed.
  const start = useCallback(
    (from: string | null) => {
      stop();
      setError(null);
      setEpisodes([]);
      seen.current.clear();
      sid.current = Math.random().toString(36).slice(2);
      const { cfg, execDelaySec, speed } = live.current;
      const src = new EventSource(
        from === null
          ? liveURL({ config: cfg, execDelaySec })
          : streamURL({
              sid: sid.current,
              from,
              speed,
              config: cfg,
              execDelaySec,
              dataset: meta.dataset,
            }),
      );
      es.current = src;
      src.addEventListener("tick", (e) => {
        const t: Tick = JSON.parse((e as MessageEvent).data);
        clock.current = t.ts;
        setTick(t);
        props.onClock(t.ts);
      });
      const upsert = (e: Event) => {
        const ep: Episode = JSON.parse((e as MessageEvent).data);
        if (!seen.current.has(ep.id)) {
          seen.current.add(ep.id);
          chime();
        }
        setEpisodes((list) => {
          const i = list.findIndex((x) => x.id === ep.id);
          if (i < 0) return [ep, ...list].slice(0, 2000);
          const next = list.slice();
          next[i] = ep;
          return next;
        });
      };
      src.addEventListener("alert", upsert);
      src.addEventListener("fill", upsert);
      src.addEventListener("close", upsert);
      src.addEventListener("fault", (e) =>
        setError(JSON.parse((e as MessageEvent).data).error),
      );
      src.addEventListener("end", () => {
        stop();
        setState("ended");
      });
      src.onerror = () => {
        if (es.current === src) {
          stop();
          if (from === null) {
            // Live view: the grid would otherwise sit frozen on old numbers. Keep trying.
            setError(
              "Lost the connection to the scanner. Retrying every 5 seconds.",
            );
            retry.current = window.setTimeout(() => {
              if (live.current.mode === "live" && es.current === null)
                start(null);
            }, 5000);
            return;
          }
          setState((s) => (s === "ended" ? s : "idle"));
          setError(
            "Lost the connection to the scanner. Check that fxarb serve is running.",
          );
        }
      };
      setState("playing");
    },
    [stop, props, chime],
  );

  useEffect(() => stop, [stop]);

  // With a live feed running, attach to it straight away.
  useEffect(() => {
    if (meta.live) start(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Changing an assumption re-prices everything: from the current simulated minute in a
  // replay, or from the current market when live.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const { state: st, mode: md } = live.current;
    if (st !== "playing" && st !== "paused") return;
    if (md === "replay" && !clock.current) return;
    const id = setTimeout(
      () => start(md === "live" ? null : clock.current!),
      350,
    );
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cfg, execDelaySec]);

  const switchMode = (m: "live" | "replay") => {
    if (m === mode) return;
    stop();
    setMode(m);
    setTick(null);
    setEpisodes([]);
    setError(null);
    clock.current = null;
    if (m === "live") start(null);
    else setState("idle");
  };
  const feed = tick?.feed ?? meta.live;

  const quotes = useMemo(() => {
    const m = new Map<string, Quote>();
    tick?.quotes.forEach((q) => m.set(q.pair + "|" + q.tenor, q));
    return m;
  }, [tick]);
  const pairBySymbol = useMemo(
    () => new Map(meta.pairs.map((p) => [p.symbol, p])),
    [meta],
  );
    const tenors = meta.tenors;

  // The group filter (All / G7 / GCC / Other) narrows the grid, the flag list and the counts.
  const inGroup = (symbol: string) =>
    props.group === "All" || pairBySymbol.get(symbol)?.group === props.group;
  const shownPairs = meta.pairs.filter((p) => inGroup(p.symbol));
  const shownFlags = episodes.filter((e) => inGroup(e.pair));
  const stillThere = shownFlags.filter((e) => e.status === "filled");

  const [selSym, selTenor] = selected ? selected.split("|") : [];
  const sel = selected ? quotes.get(selected) : undefined;
  const selPair = selSym ? pairBySymbol.get(selSym) : undefined;
  const liveSignals = tick
    ? tick.quotes.filter((q) => q.ok && q.signal && inGroup(q.pair)).length
    : 0;
  const openFlags = shownFlags.filter((e) => isOpen(e)).length;

  // The tab title carries the open-flag count, so it reads from the taskbar.
  useEffect(() => {
    document.title = (openFlags > 0 ? `(${openFlags}) ` : "") + "FX Parity Scanner";
  }, [openFlags]);

  // Feed health. A live reading older than three minutes means the numbers on screen are stale.
  const ageSec =
    mode === "live" && tick ? Math.max(0, (now - Date.parse(tick.ts)) / 1000) : 0;
  const stale = ageSec > 180;
  const status =
    mode === "live"
      ? error
        ? { tone: "bad", label: "Disconnected" }
        : state !== "playing"
          ? { tone: "", label: "Not connected" }
          : !tick
            ? { tone: "", label: "Connecting" }
            : stale
              ? { tone: "warn", label: "Stale" }
              : { tone: "ok", label: "Live" }
      : state === "playing"
        ? { tone: "replay", label: "Replaying" }
        : state === "paused"
          ? { tone: "warn", label: "Paused" }
          : state === "ended"
            ? { tone: "", label: "End of data" }
            : { tone: "", label: "Replay ready" };

  // Arrow keys walk the grid and the ticket follows; Esc clears the selection.
  const gridRef = useRef<HTMLTableElement>(null);
  const firstKey = shownPairs.length && tenors.length ? shownPairs[0].symbol + "|" + tenors[0].code : null;
  const tabKey = selected && selSym && inGroup(selSym) ? selected : firstKey;
  const onGridKey = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      setSelected(null);
      return;
    }
    const step = (
      { ArrowLeft: [0, -1], ArrowRight: [0, 1], ArrowUp: [-1, 0], ArrowDown: [1, 0] } as Record<string, number[]>
    )[e.key];
    const k = (e.target as HTMLElement).dataset?.k;
    if (!step || !k) return;
    e.preventDefault();
    const [sym, ten] = k.split("|");
    const clamp = (v: number, n: number) => Math.min(Math.max(v, 0), n - 1);
    const r = clamp(shownPairs.findIndex((p) => p.symbol === sym) + step[0], shownPairs.length);
    const c = clamp(tenors.findIndex((t) => t.code === ten) + step[1], tenors.length);
    const next = shownPairs[r].symbol + "|" + tenors[c].code;
    setSelected(next);
    gridRef.current?.querySelector<HTMLElement>(`[data-k="${next}"]`)?.focus();
  };

  const groups = props.group === "All" ? meta.groups : [props.group];
  const total = feed ? feed.pairsOk + feed.pairsDown.length : 0;

  return (
    <>
      <div className="strip">
        {meta.live && (
          <div className="seg" role="group" aria-label="Market data">
            <button aria-pressed={mode === "live"} onClick={() => switchMode("live")}>
              Live
            </button>
            <button aria-pressed={mode === "replay"} onClick={() => switchMode("replay")}>
              Replay
            </button>
          </div>
        )}
        {mode === "replay" && (
          <>
            <div className="field">
              <label htmlFor="startAt">From (UTC)</label>
              <input
                id="startAt"
                type="datetime-local"
                value={startAt}
                min={toLocalInput(meta.dataFrom ?? "")}
                max={toLocalInput(meta.dataTo ?? "")}
                onChange={(e) => setStartAt(e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="speed">Speed</label>
              <select
                id="speed"
                value={speed}
                onChange={(e) => {
                  const v = Number(e.target.value);
                  setSpeed(v);
                  if (es.current) controlStream(sid.current, { speed: v });
                }}
              >
                {SPEEDS.map((s) => (
                  <option key={s.v} value={s.v}>
                    {s.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <div className="row">
                {state === "playing" ? (
                  <button
                    className="btn"
                    onClick={() => {
                      controlStream(sid.current, { paused: true });
                      setState("paused");
                    }}
                  >
                    Pause
                  </button>
                ) : state === "paused" ? (
                  <button
                    className="btn primary"
                    onClick={() => {
                      controlStream(sid.current, { paused: false });
                      setState("playing");
                    }}
                  >
                    Resume
                  </button>
                ) : (
                  <button className="btn primary" onClick={() => start(fromLocalInput(startAt))}>
                    {state === "ended" ? "Replay again" : "Start replay"}
                  </button>
                )}
                {(state === "playing" || state === "paused") && (
                  <button
                    className="btn"
                    onClick={() => {
                      stop();
                      setState("idle");
                    }}
                  >
                    Stop
                  </button>
                )}
              </div>
            </div>
          </>
        )}
        <div className={"status " + status.tone} role="status">
          <span className="state">{status.label}</span>
          {mode === "replay" ? (
            <span className="clock">{tick ? when(tick.ts) + " UTC" : "—"}</span>
          ) : (
            tick && (
              <span className="num">
                {hhmm(tick.ts)} UTC{" "}
                <span className="age muted">
                  {ageSec < 90 ? "" : `· ${duration(ageSec)} old`}
                </span>
              </span>
            )
          )}
          {mode === "live" && feed && (
            <span className="sub">
              {feed.pairsOk}/{total} pairs
              {feed.pairsDown.length > 0 && (
                <span title={`No forward quotes: ${feed.pairsDown.map(pairLabel).join(", ")}`}>
                  {" "}
                  (no fwd: {feed.pairsDown.slice(0, 3).map(pairLabel).join(", ")}
                  {feed.pairsDown.length > 3 ? ` +${feed.pairsDown.length - 3}` : ""})
                </span>
              )}{" "}
              · indicative, about {Math.round(feed.lagSec / 60)} min delayed · {feed.source}
            </span>
          )}
          {mode === "live" &&
            (state === "playing" && !error ? (
              <button
                className="btn quiet"
                onClick={() => {
                  stop();
                  setState("idle");
                }}
              >
                Disconnect
              </button>
            ) : (
              <button className="btn primary" onClick={() => start(null)}>
                {error ? "Reconnect now" : "Connect"}
              </button>
            ))}
        </div>
        <div className="stats">
          <div className="stat">
            <b>{liveSignals}</b>
            <span title="Pair and tenor cells whose net edge is over the min edge right now, confirmed or not.">Over min edge</span>
          </div>
          <div className="stat">
            <b>{shownFlags.length}</b>
            <span title="Gaps that stayed over the min edge for the confirmation time. One flag per gap.">Flags</span>
          </div>
          <div className="stat">
            <b>{stillThere.length}</b>
            <span title={`Flags still over the min edge when re-checked ${execDelaySec}s later.`}>Held</span>
          </div>
          <div className="stat">
            <b>{shownFlags.filter((e) => e.status === "missed").length}</b>
            <span title={`Flags that had closed when re-checked ${execDelaySec}s later.`}>Gone</span>
          </div>
          <div className="stat">
            <b>{usdCompact(stillThere.reduce((s, e) => s + e.pnlUSD, 0))}</b>
            <span title={`Each held flag dealt once at ${size(cfg.notionalUSD)}, at the prices of the re-check. Indicative: nothing is traded.`}>
              Potential P&amp;L
            </span>
          </div>
        </div>
      </div>

      {error && (
        <div className="banner err" role="alert" style={{ margin: "0 0 10px" }}>
          <b>{mode === "live" ? "Grid frozen" : "Replay stopped"}</b>
          <span>{error}</span>
        </div>
      )}
      {cfg.reference === "normal" &&
        tick &&
        tick.quotes.some((q) => q.ok) &&
        !tick.quotes.some((q) => q.ok && q.normalReady) && (
          <div className="banner info" style={{ margin: "0 0 10px" }}>
            <b>Learning normal levels</b>
            <span>
              No flags until about {Math.round(cfg.normalWarmupMin / 60)}h of quotes have been seen. The grid shows the gap to the level
              learned so far.
            </span>
          </div>
        )}

      <div className="scan-grid">
        <section className="panel">
          <header>
            <div className="inline">
              <h2>
                {view === "edge"
                  ? `Net edge${cfg.reference === "normal" ? " vs normal level" : ""}, bp p.a.`
                  : "Gross gap to parity, bp p.a."}
              </h2>
              <div className="sub">
                {view === "edge"
                  ? "After bid/offer on every leg and funding. Negative: that far short of break-even."
                  : "Market forward vs parity at taker prices, before funding. Zero: parity sits inside the bid/offer."}
              </div>
            </div>
            <div className="seg" role="group" aria-label="Grid shows">
              <button aria-pressed={view === "edge"} onClick={() => setView("edge")}>
                Net edge
              </button>
              <button aria-pressed={view === "dev"} onClick={() => setView("dev")}>
                Gross gap
              </button>
            </div>
          </header>
          <div className="scroll-x">
            <table className="matrix" ref={gridRef} onKeyDown={onGridKey}>
              <thead>
                <tr>
                  <th />
                  {tenors.map((t) => (
                    <th key={t.code} scope="col" className={selTenor === t.code ? "on" : undefined}>
                      {t.code}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => {
                  const rows = shownPairs.filter((p) => p.group === g);
                  return [
                    props.group === "All" && rows.length > 0 && (
                      <tr key={"g-" + g} className="grp">
                        <th colSpan={tenors.length + 1} scope="colgroup">
                          {g}
                        </th>
                      </tr>
                    ),
                    ...rows.map((p) => {
                      const dead = !!tick && !tenors.some((t) => quotes.get(p.symbol + "|" + t.code)?.ok);
                      return (
                        <tr key={p.symbol}>
                          <th
                            scope="row"
                            className={(selSym === p.symbol ? "on" : "") + (dead ? " dead" : "")}
                            title={dead ? "No usable quotes for this pair right now" : undefined}
                          >
                            {p.base}/{p.quote}
                          </th>
                          {tenors.map((t) => {
                            const key = p.symbol + "|" + t.code;
                            return (
                              <td key={t.code}>
                                <Cell
                                  k={key}
                                  q={quotes.get(key)}
                                  view={view}
                                  selected={selected === key}
                                  tabbable={tabKey === key}
                                  onClick={() => setSelected(key)}
                                />
                              </td>
                            );
                          })}
                        </tr>
                      );
                    }),
                  ];
                })}
              </tbody>
            </table>
          </div>
          {view === "edge" ? (
            <div className="legend">
              <span>
                <span className="ramp">
                  <i style={{ background: "var(--cheap-1)" }} />
                  <i style={{ background: "var(--cheap-2)" }} />
                  <i style={{ background: "var(--cheap-3)" }} />
                </span>
                Fwd cheap: buy fwd
              </span>
              <span>
                <span className="ramp">
                  <i style={{ background: "var(--rich-1)" }} />
                  <i style={{ background: "var(--rich-2)" }} />
                  <i style={{ background: "var(--rich-3)" }} />
                </span>
                Fwd rich: sell fwd
              </span>
              <span>Filled: over min edge (under 20, 20 to 50, over 50 bp)</span>
              <span className="note">Leads to check on a dealing screen, not prices to deal on.</span>
            </div>
          ) : (
            <div className="legend">
              <span>
                <i style={{ background: divergingFill(-60) }} />
                Fwd cheap to parity
              </span>
              <span>
                <i style={{ background: "var(--div-mid)" }} />
                Inside bid/offer
              </span>
              <span>
                <i style={{ background: divergingFill(60) }} />
                Fwd rich to parity
              </span>
              <span>
                <i style={{ boxShadow: "inset 0 0 0 1.5px var(--ink)" }} />
                Outlined: over min edge after funding
              </span>
              <span className="note">Leads to check on a dealing screen, not prices to deal on.</span>
            </div>
          )}
        </section>

        <aside className="rail">
          {selected && selPair && (
            <Ticket q={sel} pair={selPair} tenor={selTenor} cfg={cfg} onClose={() => setSelected(null)} />
          )}

          <section className="panel flags">
            <header>
              <div>
                <h2>Flags{shownFlags.length > 0 && ` · ${openFlags} open of ${shownFlags.length}`}</h2>
                <div className="sub">
                  Newest first. Re-checked {execDelaySec}s after flagging.
                </div>
              </div>
              <button
                className="btn quiet"
                aria-pressed={sound}
                title="Play a short tone when a new flag is raised"
                onClick={() => {
                  setSound(!sound);
                  if (!sound) chime(true);
                }}
              >
                Sound {sound ? "on" : "off"}
              </button>
            </header>
            <div className="feed">
              {shownFlags.length === 0 && (
                <div className="empty">
                  {state === "idle" ? (
                    <>
                      <b>Not scanning</b>
                      {mode === "live" ? "Connect to scan the live market." : "Start the replay to scan recorded quotes."}
                    </>
                  ) : (
                    <>
                      <b>No flags yet</b>
                      A gap flags once it has held over {cfg.minEdgeBps} bp for{" "}
                      {cfg.confirmSec ? duration(cfg.confirmSec) : "one reading"}.
                      {!selected && " Click any cell for its levels and legs."}
                    </>
                  )}
                </div>
              )}
              {shownFlags.map((e) => {
                const key = e.pair + "|" + e.tenor;
                const open = isOpen(e);
                return (
                  <button
                    key={e.id}
                    className="alert"
                    aria-current={selected === key}
                    onClick={() => setSelected(key)}
                  >
                    <span className={"head" + (open ? "" : " closed")}>
                      {pairLabel(e.pair)} {e.tenor}{" "}
                      <span className={side(e.direction)}>{sideLabel(e.direction)}</span>
                    </span>
                    <span className={"num head" + (open ? "" : " closed")}>
                      {bps(e.edgeAtDetectBps)} bp
                    </span>
                    <span className="sub" title={tradeLabel(e.direction, e.pair)}>
                      {mode === "live" ? hhmm(e.detectedAt) : when(e.detectedAt)} · {pts(e.detect.edgePips)} pips
                    </span>
                    <span style={{ textAlign: "right" }}>
                      {open && e.status !== "pending" && <span className="chip open">Open</span>}{" "}
                      <span className={"chip " + e.status}>
                        {e.status === "filled"
                          ? `Held · ${usdCompact(e.pnlUSD)}`
                          : e.status === "missed"
                            ? "Gone"
                            : "New"}
                      </span>
                    </span>
                  </button>
                );
              })}
            </div>
          </section>
        </aside>
      </div>
    </>
  );
}

function Cell({
  k,
  q,
  view,
  selected,
  tabbable,
  onClick,
}: {
  k: string;
  q?: Quote;
  view: "edge" | "dev";
  selected: boolean;
  tabbable: boolean;
  onClick: () => void;
}) {
  const common = { "data-k": k, tabIndex: tabbable ? 0 : -1, onClick };
  const name = pairLabel(k.split("|")[0]) + " " + k.split("|")[1];
  if (!q || !q.ok) {
    return (
      <button
        {...common}
        className={"cell na" + (selected ? " sel" : "")}
        title={`${name}: ${q?.reason ?? "no quote yet"}`}
        aria-label={`${name}, no quote`}
      >
        ·
      </button>
    );
  }
  const title = `${name} · net ${bps(q.edgeBps)} bp · gross ${bps(q.deviationBps)} bp${q.signal ? ` · ${sideLabel(q.direction).toLowerCase()}, over min edge` : ""}`;
  const dir = q.signal ? " f " + side(q.direction) : "";
  if (view === "dev") {
    return (
      <button
        {...common}
        className={"cell dev" + dir + (selected ? " sel" : "")}
        style={{ background: divergingFill(q.deviationBps) }}
        title={title}
      >
        {bpsCell(q.deviationBps)}
      </button>
    );
  }
  const tone = !q.signal
    ? q.edgeBps > 0
      ? " near"
      : ""
    : dir + (q.edgeBps < 20 ? " s1" : q.edgeBps < 50 ? " s2" : " s3");
  return (
    <button {...common} className={"cell" + tone + (selected ? " sel" : "")} title={title}>
      {bpsCell(q.edgeBps)}
    </button>
  );
}

function Ticket({
  q,
  pair,
  tenor,
  cfg,
  onClose,
}: {
  q?: Quote;
  pair: Pair;
  tenor: string;
  cfg: Config;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const close = (
    <button className="btn quiet" onClick={onClose} aria-label="Close ticket" title="Close (Esc)">
      Close
    </button>
  );
  if (!q || !q.ok) {
    return (
      <section className="panel ticket">
        <header>
          <h2>
            {pair.base}/{pair.quote} {tenor}
          </h2>
          {close}
        </header>
        <div className="sub">No quote: {q?.reason ?? "nothing read for this tenor yet"}.</div>
      </section>
    );
  }
  // The band in forward points, each end against the spot side it is dealt with.
  const bandLowPts = (q.bandLow - q.spotAsk) * pair.pipFactor;
  const bandHighPts = (q.bandHigh - q.spotBid) * pair.pipFactor;
  const src = (s: string) => (s === "POLICY" ? "policy" : s);
  const rich = isRich(q.direction);
  const sd = side(q.direction);
  // Which side of each quote the trade deals on.
  const deal = (hit: boolean) => (hit ? "deal" : undefined);
  const copy = () =>
    navigator.clipboard
      ?.writeText(ticketText(q, pair))
      .then(() => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => {});
  return (
    <section className="panel ticket" aria-label={`${pair.base}/${pair.quote} ${q.tenor} ticket`}>
      <header>
        <div>
          <h2>
            {pair.base}/{pair.quote} {q.tenor} <span className="muted">{q.days}d</span>
          </h2>
          <div className="sub">Read {when(q.ts)} UTC, indicative</div>
        </div>
        {close}
      </header>

      <div className="verdict">
        <span className="edge">{bps(q.edgeBps)} bp p.a.</span>
        {q.signal ? (
          <span className={"tag " + sd}>Over min edge</span>
        ) : q.edgeBps > 0 ? (
          <span className="tag">Under {cfg.minEdgeBps} bp min edge</span>
        ) : (
          <span className="tag">No edge after costs</span>
        )}
        <span className="sub num">
          {pts(q.edgePips)} pips net
          {q.edgeBps > 0 && ` · ${usd(q.profitUSD)} potential on ${size(cfg.notionalUSD)}`}
        </span>
        {q.grossPips > 0 ? (
          <span className={"side " + sd}>
            {sideLabel(q.direction)} by {pts(q.grossPips)} pips gross: {sideAction(q.direction)}
          </span>
        ) : (
          <span className="side muted">Inside bid/offer: {pts(-q.grossPips)} pips short before funding</span>
        )}
      </div>

      <ParityGauge
        bandLow={bandLowPts}
        bandHigh={bandHighPts}
        fair={q.fairPts}
        bid={q.ptsBid}
        ask={q.ptsAsk}
        side={sd}
        fmt={pts}
      />

      <table className="lv">
        <thead>
          <tr>
            <th>Market</th>
            <th>Bid</th>
            <th>Offer</th>
            <th>Parity</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <th scope="row">Spot</th>
            <td className={deal(!rich)}>{px(q.spotBid, pair)}</td>
            <td className={deal(rich)}>{px(q.spotAsk, pair)}</td>
            <td />
          </tr>
          <tr>
            <th scope="row">Fwd pts</th>
            <td className={deal(rich)}>{pts(q.ptsBid)}</td>
            <td className={deal(!rich)}>{pts(q.ptsAsk)}</td>
            <td className="span">{pts(q.fairPts)}</td>
          </tr>
          <tr>
            <th scope="row">Outright</th>
            <td className={deal(rich)}>{px(q.fwdBid, pair)}</td>
            <td className={deal(!rich)}>{px(q.fwdAsk, pair)}</td>
            <td className="span">{px(q.fairFwd, pair)}</td>
          </tr>
        </tbody>
      </table>
      <div className="sub">Bold: the side this trade deals on.</div>

      <div className="kv">
        <div>
          <span>
            {pair.base} {src(q.rateSrcBase)}
          </span>
          <b>{pct(q.rateBase)}</b>
        </div>
        <div>
          <span>
            {pair.quote} {src(q.rateSrcQuote)}
          </span>
          <b>{pct(q.rateQuote)}</b>
        </div>
        <div>
          <span>Funding on {q.borrowCcy}</span>
          <b>+{q.borrowBps.toFixed(0)} bp</b>
        </div>
        <div>
          <span title="Market forward vs parity at taker prices, before funding.">Gross gap</span>
          <b>{bps(q.deviationBps)} bp</b>
        </div>
        <div>
          <span title="This pair and tenor's usual gap to parity: a one-day average of past readings.">
            Normal level{q.normalReady ? "" : " (learning)"}
          </span>
          <b>{bps(q.normalBps)} bp</b>
        </div>
        <div>
          <span>Net edge{q.reference === "normal" ? " vs normal" : ""}</span>
          <b>{bps(q.edgeBps)} bp</b>
        </div>
      </div>

      <h3>
        {q.edgeBps > 0 ? "Legs" : "Closest trade (loses after costs)"}
        <button className="btn quiet" onClick={copy} title="Copy the levels and legs as text">
          {copied ? "Copied" : "Copy ticket"}
        </button>
      </h3>
      <ol className="legs">
        {legs(q, pair).map(([what, level, note], i) => (
          <li key={i}>
            {what} @ <b>{level}</b> {note && <small>({note})</small>}
          </li>
        ))}
      </ol>
    </section>
  );
}

import { useEffect, useState } from "react";
import { getMeta, type Config, type Meta } from "./api";
import { Backtest } from "./Backtest";
import { Rates } from "./Rates";
import { Scanner } from "./Scanner";

type Tab = "scanner" | "backtest" | "rates";
type Theme = "dark" | "light";

const NOTIONALS = [1, 5, 10, 25, 50, 100];

function savedTheme(): Theme {
  try {
    return localStorage.getItem("fxarb-theme") === "light" ? "light" : "dark";
  } catch {
    return "dark";
  }
}

export function App() {
  const [rawMeta, setMeta] = useState<Meta | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [cfg, setCfg] = useState<Config | null>(null);
  // Each flag is re-checked this long after it is raised (fixed; it only labels flags "held" or "gone").
  const execDelaySec = 60;
  const [tab, setTab] = useState<Tab>("scanner");
  const [clock, setClock] = useState<string | null>(null);
  const [dataset, setDataset] = useState<string | null>(null);
  const [group, setGroup] = useState("All");
  const [theme, setTheme] = useState<Theme>(savedTheme);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("fxarb-theme", theme);
    } catch {
      /* private window: the choice just does not persist */
    }
  }, [theme]);

  useEffect(() => {
    getMeta()
      .then((m) => {
        setMeta(m);
        setCfg(m.defaults);
      })
      .catch((e) => setError(String(e.message)));
  }, []);

  if (error) {
    return (
      <div className="shell">
        <div className="banner err" role="alert">
          <b>Scanner not reachable</b>
          <span>
            {error}. Start it with <code>fxarb serve -live</code>, then reload.
          </span>
        </div>
      </div>
    );
  }
  if (!rawMeta || !cfg) return <div className="shell muted">Connecting to the scanner…</div>;

  // The replay and back-test run on whichever data set is selected.
  const ds = rawMeta.datasets.find((d) => d.name === dataset) ?? rawMeta.datasets[0];
  const meta: Meta & { dataFrom?: string; dataTo?: string } = { ...rawMeta, dataset: ds.name, dataFrom: ds.dataFrom, dataTo: ds.dataTo };

  const num = (key: keyof Config) => (e: React.ChangeEvent<HTMLInputElement>) => setCfg({ ...cfg, [key]: Number(e.target.value) || 0 });
  const count = (g: string) => (g === "All" ? meta.pairs.length : meta.pairs.filter((p) => p.group === g).length);

  return (
    <div className="shell">
      <header className="top">
        <h1>FX Parity Scanner</h1>
        <div className="tabs" role="tablist">
          {(
            [
              ["scanner", "Scanner"],
              ["backtest", "Back-test"],
              ["rates", "Rates"],
            ] as [Tab, string][]
          ).map(([id, label]) => (
            <button key={id} role="tab" aria-selected={tab === id} onClick={() => setTab(id)}>
              {label}
            </button>
          ))}
        </div>
        <div className="right">
          {meta.datasets.length > 1 && (
            <>
              <label htmlFor="dataset" className="sub">
                History
              </label>
              <select id="dataset" value={ds.name} onChange={(e) => setDataset(e.target.value)}>
                {meta.datasets.map((d) => (
                  <option key={d.name} value={d.name}>
                    {d.label}
                  </option>
                ))}
              </select>
            </>
          )}
          <button
            className="btn quiet"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            title="Switch between the dark and light theme"
          >
            {theme === "dark" ? "Light theme" : "Dark theme"}
          </button>
        </div>
      </header>

      {ds.error && (
        <div className="banner err" role="alert">
          <b>No market data</b>
          <span>{ds.error}</span>
        </div>
      )}

      <section className="toolbar" aria-label="Scan settings">
        <div className="field">
          <span className="lbl">Pairs</span>
          <div className="seg" role="group" aria-label="Pairs shown">
            {["All", ...meta.groups].map((g) => (
              <button key={g} aria-pressed={group === g} onClick={() => setGroup(g)} title={`${count(g)} pairs`}>
                {g}
              </button>
            ))}
          </div>
        </div>
        <div className="field">
          <span className="lbl" title="Added to the reference rate of the currency borrowed. Per-currency spreads are on the Rates tab.">
            Funding spread
          </span>
          <div className="row">
            <button
              className="switch"
              role="switch"
              aria-checked={cfg.applyBorrowSpread}
              aria-label="Apply funding spread"
              onClick={() => setCfg({ ...cfg, applyBorrowSpread: !cfg.applyBorrowSpread })}
            />
            <input
              type="number"
              min={0}
              step={5}
              aria-label="Funding spread in basis points"
              value={cfg.borrowSpreadBps}
              disabled={!cfg.applyBorrowSpread}
              onChange={num("borrowSpreadBps")}
            />
            <span className="unit">bp</span>
          </div>
        </div>
        <div className="field">
          <label htmlFor="minEdge" title="A gap flags when the net edge after dealing and funding costs is at least this much.">
            Min edge
          </label>
          <div className="row">
            <input id="minEdge" type="number" min={0} step={1} value={cfg.minEdgeBps} onChange={num("minEdgeBps")} />
            <span className="unit">bp p.a.</span>
          </div>
        </div>
        <div className="field">
          <label htmlFor="confirm" title="A gap must stay over the min edge this long before it is raised as a flag.">
            Confirm for
          </label>
          <select id="confirm" value={cfg.confirmSec} onChange={(e) => setCfg({ ...cfg, confirmSec: Number(e.target.value) })}>
            <option value={0}>No wait</option>
            <option value={60}>1 min</option>
            <option value={120}>2 min</option>
            <option value={300}>5 min</option>
            <option value={600}>10 min</option>
          </select>
        </div>
        <div className="field">
          <span
            className="lbl"
            title="Parity: the forward implied by spot and the two interest rates. Normal: the pair's own usual gap to parity (one-day average of past readings), so a standing gap stops flagging and only a move away from it does."
          >
            Measure vs
          </span>
          <div className="seg" role="group" aria-label="Measure the gap against">
            <button aria-pressed={cfg.reference === "parity"} onClick={() => setCfg({ ...cfg, reference: "parity" })}>
              Parity
            </button>
            <button aria-pressed={cfg.reference === "normal"} onClick={() => setCfg({ ...cfg, reference: "normal" })}>
              Normal level
            </button>
          </div>
        </div>
        <div className="field">
          <label htmlFor="notional" title="Notional used for potential P&L.">
            Size
          </label>
          <select id="notional" value={cfg.notionalUSD} onChange={(e) => setCfg({ ...cfg, notionalUSD: Number(e.target.value) })}>
            {NOTIONALS.map((n) => (
              <option key={n} value={n * 1e6}>
                ${n}M
              </option>
            ))}
          </select>
        </div>
      </section>

      <div hidden={tab !== "scanner"}>
        <Scanner key={ds.name} meta={meta} cfg={cfg} execDelaySec={execDelaySec} group={group} onClock={setClock} />
      </div>
      <div hidden={tab !== "backtest"}>
        <Backtest key={ds.name} meta={meta} cfg={cfg} execDelaySec={execDelaySec} group={group} />
      </div>
      {tab === "rates" && <Rates meta={meta} cfg={cfg} setCfg={setCfg} asof={clock} />}
    </div>
  );
}

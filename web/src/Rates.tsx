import { useEffect, useState } from "react";
import { getRates, setRate, type Config, type Meta, type RateEntry } from "./api";
import { pct, when } from "./format";

export function Rates(props: { meta: Meta; cfg: Config; setCfg: (c: Config) => void; asof: string | null }) {
  const { cfg, setCfg, asof } = props;
  const [rows, setRows] = useState<RateEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
    const at = asof ?? props.meta.dataTo ?? undefined;
  const [version, setVersion] = useState(0);
  const [draft, setDraft] = useState<Record<string, { rate: string; since: string }>>({});
  const today = new Date().toISOString().slice(0, 10);

  async function saveRate(ccy: string) {
    const d = draft[ccy];
    if (!d || d.rate === "") return;
    setError(null);
    try {
      await setRate({ ccy, ratePct: Number(d.rate), since: d.since || today });
      setDraft((all) => ({ ...all, [ccy]: { rate: "", since: "" } }));
      setVersion((v) => v + 1);
    } catch (e) {
      setError(String((e as Error).message));
    }
  }


  useEffect(() => {
        // A hand-entered rate takes effect now, so ask for the latest table after saving one.
    getRates(version > 0 ? undefined : at, props.meta.dataset)
      .then(setRows)
      .catch((e) => setError(String(e.message)));
  }, [at, props.meta.dataset, version]);

  const overrides = cfg.borrowSpreadByCcy ?? {};
  const setOverride = (ccy: string, raw: string) => {
    const next = { ...overrides };
    if (raw === "") delete next[ccy];
    else next[ccy] = Number(raw);
    setCfg({ ...cfg, borrowSpreadByCcy: next });
  };

  return (
    <section className="panel" style={{ marginTop: 10 }}>
      <header>
        <div>
          <h2>Rates used for parity</h2>
          <div className="sub">
            BIS policy rates as known at {at ? when(at) + " UTC" : "the latest date"}. AED, QAR, BHD and OMR have no BIS series: enter them here,
            or their pairs are recorded but not priced.
          </div>
        </div>
      </header>
      {error && (
        <div className="banner err" role="alert" style={{ margin: "0 0 10px" }}>
          <b>Rate not saved</b>
          <span>{error}</span>
        </div>
      )}
      {!rows && !error && <div className="empty">Loading rates…</div>}
      <div className="tablewrap" style={{ maxHeight: "none" }}>
        <table className="data">
          <thead>
            <tr>
              <th>Ccy</th>
              <th>Source</th>
              <th className="num">Policy rate</th>
              <th>Since</th>
              <th title="Market rates loaded for specific tenors. With none, the policy rate prices every tenor.">Tenor rates</th>
              <th className="num">Day count</th>
              <th className="num">Funding spread, bp</th>
              <th className="num">Borrow at</th>
            </tr>
          </thead>
          <tbody>
            {rows?.map((r) => {
              const spread = !cfg.applyBorrowSpread ? 0 : overrides[r.code] ?? cfg.borrowSpreadBps;
              const curve = Object.keys(r.curve);
              return (
                <tr key={r.code}>
                  <td>
                    <b>{r.code}</b> <span className="muted">{r.name}</span>
                  </td>
                  <td>
                    {r.bank}
                    {r.manualRate && <span className="muted"> · manual</span>}
                  </td>
                                    <td className="num">
                    {r.policy === null ? <span className="chip pending">Not priced</span> : pct(r.policy)}
                    {r.manualRate && (
                      <div className="row" style={{ justifyContent: "flex-end", marginTop: 4 }}>
                        <input
                          type="number"
                          step="0.05"
                          aria-label={`Policy rate for ${r.code}, percent`}
                          placeholder="rate %"
                          value={draft[r.code]?.rate ?? ""}
                          onChange={(e) => setDraft((all) => ({ ...all, [r.code]: { since: all[r.code]?.since ?? "", rate: e.target.value } }))}
                        />
                      </div>
                    )}
                  </td>
                  <td>
                    {r.policySince || "—"}
                    {r.manualRate && (
                      <div className="row" style={{ marginTop: 4 }}>
                        <input
                          type="date"
                          aria-label={`Date the ${r.code} rate took effect`}
                          max={today}
                          value={draft[r.code]?.since ?? ""}
                          onChange={(e) => setDraft((all) => ({ ...all, [r.code]: { rate: all[r.code]?.rate ?? "", since: e.target.value } }))}
                        />
                        <button className="btn" disabled={!draft[r.code]?.rate} onClick={() => saveRate(r.code)}>
                          Save rate
                        </button>
                      </div>
                    )}
                  </td>
                  <td>{curve.length ? curve.join(", ") : <span className="muted">none</span>}</td>
                  <td className="num">ACT/{r.dayBasis}</td>
                  <td className="num">
                    <input
                      type="number"
                      aria-label={`Funding spread for ${r.code} in basis points`}
                      placeholder={String(cfg.borrowSpreadBps)}
                      value={overrides[r.code] ?? ""}
                      disabled={!cfg.applyBorrowSpread}
                      onChange={(e) => setOverride(r.code, e.target.value)}
                    />
                  </td>
                  <td className="num">{r.policy === null ? "—" : pct(r.policy + spread / 100)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="sub" style={{ marginBottom: 0 }}>
        Blank funding spread: the global spread above applies. Tenor rates override the policy rate when loaded: add rows such as{" "}
        <code>2026-11-09,USD,3M,3.92</code> to <code>data-live/rates.csv</code>.
      </p>
    </section>
  );
}

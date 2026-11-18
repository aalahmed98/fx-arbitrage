# FX Parity Scanner

A flagging tool for FX traders. It reads live forward points for 32 currency pairs across
15 tenors (1W, 2W, 3W and every month from 1M to 1Y), compares each market forward with the forward that
interest rate parity implies, and flags the gaps that survive dealing costs and funding costs.
Nothing is ever traded.

Go back end (standard library only), React dashboard.

## What data it uses

Only real quotes, recorded as they are read. There is no simulated or derived market data.

| Data | Source | Notes |
|---|---|---|
| Spot bid/offer | investing.com forward-rates pages | read once a minute |
| Forward points bid/offer, per tenor | investing.com forward-rates pages | stamped 30 minutes behind the clock for majors, about 60 for other pairs; indicative |
| Interest rates | BIS central-bank policy rates | refreshed at start and twice a day |
| Interest rates for AED, QAR, BHD, OMR | entered by you on the Rates tab | the BIS has no series for these; until a rate is entered the pair is recorded but not priced |

Nobody publishes historical forward points per tenor for free, so the history is whatever the
collector has recorded since it was first switched on (17 Nov 2026). Replay and back-test work
on that recording and get more useful every day it runs.

What to know about the source:

- 31 of the 32 pairs have forward data (not AUD/JPY; CAD/JPY has none either and is left out).
- Gulf quotes are thinner: bid/offer is wide, USD/OMR is often hours old, and the USD/QAR
  spot quote is far too wide to be dealable (3.51 / 3.78 when checked).
- It is delayed and indicative. A flag is a lead to check on a dealing screen, not a price.
- The site sits behind Cloudflare's bot checks. A plain, honestly labelled reader is served
  today; if that changes the collector logs the failure and records nothing, and it is not to
  be made to evade the check. Investing.com's terms also restrict automated access and
  commercial use, so clear this with compliance before the desk relies on it.

## Pairs and the filter

| Group | Pairs |
|---|---|
| G7 | EUR/USD, GBP/USD, USD/JPY, USD/CAD, EUR/GBP, EUR/JPY, GBP/JPY, EUR/CAD, GBP/CAD |
| GCC | USD/SAR, USD/AED, USD/QAR, USD/KWD, USD/BHD, USD/OMR |
| Other | USD/CHF, AUD/USD, NZD/USD, USD/SEK, USD/NOK, USD/DKK, EUR/CHF, AUD/JPY, EUR/SEK, EUR/NOK, USD/PLN, USD/CZK, USD/HUF, USD/MXN, USD/ZAR, USD/TRY, USD/HKD |

The "Show" control at the top of the dashboard narrows the grid, the flag list, the counts and
the back-test to one group. Pairs and groups are defined in `internal/market/market.go`.

Two conventions for the Gulf pairs are assumptions to confirm with the desk: forward points
for USD/BHD, USD/KWD and USD/OMR are read as pips of 0.00001, and KWD interest is counted on
ACT/365 (the others on ACT/360). Gulf value dates also ignore the Friday/Saturday weekend.

## Run it

```bash
go build -o fxarb.exe ./cmd/fxarb
```

```bash
cd web && npm install && npm run build && cd ..
```

```bash
./fxarb.exe serve -live
```

Then open http://127.0.0.1:8080. `serve -live` is the dashboard and the collector in one
process. `go test ./...` runs the tests.

Other commands:

| Command | What it does |
|---|---|
| `fxarb record` | collector only, no dashboard, for a machine that just gathers data |
| `fxarb backtest` | replay the recorded quotes and print the flags |
| `fxarb probe` | one live reading per pair next to parity, to check a pair's units |
| `fxarb fetch-rates` | download policy rates from the BIS |

Back-test options: `-borrow=false`, `-borrow-bps 40`, `-min-edge 10`,
`-confirm 300`, `-reference parity`, `-delay 120`, `-notional 25000000`,
`-pairs EURUSD,USDJPY`, `-from`, `-to`, `-out report.json`.

## Collecting every day

The collector appends everything it reads to `data-live/`:

| File | Columns |
|---|---|
| `rates.csv` | `date,ccy,tenor,rate_pct` where tenor is `POLICY` or a tenor code |
| `spot/EURUSD.csv` | `ts,bid,ask` |
| `forwards/EURUSD.csv` | `ts,tenor,bid_pts,ask_pts,quote_ts` (forward points in pips; `ts` is when we read the quote, `quote_ts` is the time the source put on it) |

- Each finished month is compressed to `EURUSD.2026-11.csv.gz` and a new file is started; the
  back-test reads archives and the current file as one history. Expect about 10 MB a day
  before compression.
- Every tenor from one week to one year that the source shows is saved. Not saved: the
  pre-spot tenors (ON, TN, SN) and anything beyond one year, which the parity formula used
  here does not cover, and quotes the source has stopped updating.
- Weekends and holidays record nothing: stale quotes are dropped.
- The calculated values (formula forward, gap, edge) are not stored; they are recomputed
  from the saved quotes and rates whenever the history is replayed.
- Only one collector may write to a folder; a second one refuses to record.
- It only collects while it is running, so it belongs on a machine that stays on.

### Auto-start on this PC

A per-user scheduled task, "FX Parity Scanner collector", starts
`fxarb-service.exe serve -live` at logon. Its output goes to `data-live/collector.log`. It
stops when the PC sleeps or shuts down and resumes at the next logon.

`fxarb-service.exe` is a copy of `fxarb.exe`, so rebuilding the project does not disturb it.
To put a new build into service, stop the task, copy the new `fxarb.exe` over
`fxarb-service.exe`, and start the task again. Dashboard-only changes (`npm run build`) are
picked up on the next page reload.

```bash
powershell -Command "Stop-ScheduledTask -TaskName 'FX Parity Scanner collector'"
```

```bash
powershell -Command "Start-ScheduledTask -TaskName 'FX Parity Scanner collector'"
```

```bash
powershell -Command "Unregister-ScheduledTask -TaskName 'FX Parity Scanner collector' -Confirm:$false"
```

## The calculation

For BASE/QUOTE with spot `S`, rates `r_b`, `r_q` and year fractions `tau = days / 360 or 365`:

    parity forward  F* = S x (1 + r_q x tau_q) / (1 + r_b x tau_b)

| Market forward is | Trade a flag points to | Profitable when |
|---|---|---|
| too high | borrow QUOTE, buy BASE spot, deposit BASE, sell BASE forward | forward bid > band high |
| too low | borrow BASE, sell BASE spot, deposit QUOTE, buy BASE forward | forward offer < band low |

### Which prices are used

Every leg is priced as a market taker, paying the spread each time. No mid price is used
anywhere in the calculation.

- Spot: buy at the offer, sell at the bid.
- Forward: sell at the forward bid, buy at the forward offer. Points are quoted over spot, so
  the outright bid is spot bid plus bid points and the outright offer is spot offer plus offer points.
- Money: borrow at the reference rate plus the funding spread; deposit at the reference rate.

The funding spread (default 25bp) is added to the rate of whichever currency is borrowed. It
can be switched off, changed, or set per currency on the Rates tab. The edge is shown in basis
points a year and in pips of the forward price.

### What gets flagged

- **Measured against parity** by default (`-reference parity`): the market forward against the formula forward. The alternative, `-reference normal`, measures against the pair's normal level. Real forwards
  sit away from policy-rate parity all the time, because money does not trade at the policy
  rate and because of the cross-currency basis. So each pair and tenor is compared with its own
  usual gap to parity (a one-day average of past readings only), and a flag means the gap has
  moved away from that. `-reference parity` measures against parity itself. The first four
  hours of any recording raise no flags while the normal levels are learned.
- **Confirmation.** A gap must clear the threshold for two minutes running (`-confirm 120`).
- **One flag per gap.** It stays one flag until the edge is gone.
- **Reaction check.** Each flag is checked again after a trader reaction time (default one
  minute): "still there" or "gone", with the potential P&L of dealing the chosen size once at
  that moment (`edge over the period x size`, received at maturity, not discounted).

### Reading the grid

"Net edge" shows what is left after costs, in basis points a year. A filled cell is over the
minimum edge: blue when the forward is cheap (sell spot, buy forward), orange when it is rich
(buy spot, sell forward), and deeper the bigger the edge. An unfilled cell with a negative
number is that far short of break-even.
"Gross gap" compares the market forward with parity, as a taker and before funding costs:
positive (orange) means rich, negative (blue) cheap, zero means parity sits inside the
bid/offer. The funding spread does not move it.

Click a cell, or move with the arrow keys, to open its ticket: the levels, the side each leg
deals on, and the four legs, which "Copy ticket" puts on the clipboard. The flag list marks
each flag "Held" or "Gone" at the reaction check, the browser tab shows the number of open
flags, and "Sound on" plays a tone when a new flag is raised. The dashboard is dark by
default; "Light theme" in the top right switches it and the choice is remembered.

## No look-ahead

- The scanner receives one event at a time and holds no handle on the data source. It rejects
  any event older than its clock.
- Every quote is stamped with the moment it was read, never an earlier time.
- A rate applies only from the day it took effect; a rate change picked up while running is
  stamped with the moment it was learned.
- The normal level a reading is compared with never includes that reading.
- `TestNoLookAhead` re-runs a back-test on data cut off at several points and requires every
  decision before the cut to match the full run exactly.

The automated tests need data where the right answer is known in advance, so
`internal/testgen` builds small made-up data sets in temporary folders while the tests run.
That is the only non-real data anywhere in the project; none of it is kept, and no command
or dashboard screen can reach it.

## Known limits

- **Policy rates are not tenor rates.** Measuring against the normal level works around this.
  To use real tenor rates instead, add rows such as `2026-11-09,USD,3M,3.92` to
  `data-live/rates.csv`; the scanner prefers a tenor rate whenever one is loaded.
- Value dates skip weekends but not currency holidays.
- The only live source is the delayed investing.com reader. A dealable feed only has to emit
  the same `engine.Event` stream into `feed.Hub`.
- Potential P&L ignores credit lines, balance-sheet cost and settlement risk.

// Command fxarb flags covered-interest-parity gaps in FX forwards for traders.
//
//	fxarb serve -live     dashboard + collector: read live forward points every minute, flag gaps, record everything
//	fxarb record          collector only, no dashboard
//	fxarb backtest        replay the recorded quotes and report the flags
//	fxarb probe           one live reading per pair next to parity, to check a pair's units
//	fxarb fetch-rates     download central-bank policy rates (BIS)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"fxarb/internal/api"
	"fxarb/internal/backtest"
	"fxarb/internal/collect"
	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/feed"
	"fxarb/internal/fetch"
	"fxarb/internal/market"
)

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseDate(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	t, err := data.ParseTS(s)
	die(err)
	return t
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fxarb <record|serve|backtest|probe|fetch-rates> [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("data", "data-live", "folder of recorded quotes")
	from := fs.String("from", "", "start (YYYY-MM-DD or RFC 3339, UTC)")
	to := fs.String("to", "", "end, exclusive")

	switch cmd {
	case "fetch-rates":
		since := fs.String("since", "2024-01-01", "earliest date to download")
		fs.Parse(args)
		rows, err := fetch.PolicyRates(parseDate(*since, time.Time{}))
		die(err)
		existing, _ := data.LoadRates(*dir)
		die(data.SaveRates(*dir, fetch.MergeRates(existing, rows)))
		latest := map[string]data.RateRow{}
		for _, r := range rows {
			latest[r.Ccy] = r
		}
		fmt.Printf("wrote %d policy-rate changes to %s/rates.csv\n", len(rows), *dir)
		for _, c := range market.Currencies {
			if r, ok := latest[c.Code]; ok {
				fmt.Printf("  %s %7.3f%%  since %s  (%s)\n", c.Code, r.Rate, r.Date.Format("2006-01-02"), c.Bank)
			} else {
				fmt.Printf("  %s  no data from BIS\n", c.Code)
			}
		}

	case "backtest":
		cfg := engine.DefaultConfig()
		fs.BoolVar(&cfg.ApplyBorrowSpread, "borrow", cfg.ApplyBorrowSpread, "apply the borrowing spread")
		fs.Float64Var(&cfg.BorrowSpreadBps, "borrow-bps", cfg.BorrowSpreadBps, "spread over the reference rate when borrowing, bps")
		fs.Float64Var(&cfg.MinEdgeBps, "min-edge", cfg.MinEdgeBps, "annualised net edge needed to alert, bps")
		fs.Float64Var(&cfg.NotionalUSD, "notional", cfg.NotionalUSD, "trade size, USD")

		fs.IntVar(&cfg.ConfirmSec, "confirm", cfg.ConfirmSec, "seconds a gap must hold before it is flagged")
		fs.StringVar(&cfg.Reference, "reference", cfg.Reference, "parity, or normal (the pair's own usual gap)")
		delay := fs.Int("delay", 60, "seconds between alert and execution")
		pairs := fs.String("pairs", "", "comma-separated symbols (default: all)")
		out := fs.String("out", "", "write the full report as JSON to this file")
		fs.Parse(args)
		rep, err := backtest.Run(backtest.Params{DataDir: *dir, From: parseDate(*from, time.Time{}),
			To: parseDate(*to, time.Time{}), Cfg: cfg, ExecDelaySec: *delay, Pairs: splitList(*pairs)})
		die(err)
		printReport(rep)
		if *out != "" {
			b, _ := json.MarshalIndent(rep, "", " ")
			die(os.WriteFile(*out, b, 0o644))
			fmt.Println("full report:", *out)
		}

	case "probe":
		// One live reading per pair, compared with parity. Use it to check a pair's page exists
		// and that its forward points are in the units the scanner expects.
		pairs := fs.String("pairs", "", "comma-separated symbols (default: all)")
		fs.Parse(args)
		rows, err := data.LoadRates(*dir)
		die(err)
		cfg := engine.DefaultConfig()
		cfg.ApplyBorrowSpread, cfg.MaxStaleSec = false, 0
		sel := backtest.SelectPairs(splitList(*pairs))
		sc := engine.NewScanner(cfg, sel)
		now := time.Now().UTC()
		for _, r := range rows {
			if !r.Date.After(now) {
				die(sc.Apply(engine.Event{TS: r.Date, Kind: engine.EvRate, Ccy: r.Ccy, Tenor: r.Tenor, Rate: r.Rate}))
			}
		}
		src := feed.NewInvesting()
		fmt.Printf("%-7s %-22s %5s  %s\n", "pair", "spot bid/ask", "rows", "tenor: market pts bid/offer | formula pts | taker gap bp (0 = inside the spread)")
		for i, p := range sel {
			if i > 0 {
				time.Sleep(1500 * time.Millisecond)
			}
			snap, err := src.Fetch(context.Background(), p)
			if err != nil {
				fmt.Printf("%-7s ERROR %v\n", p.Symbol, err)
				continue
			}
			evs, lag := feed.Events(snap)
			for _, ev := range evs {
				die(sc.Apply(ev))
			}
			line := fmt.Sprintf("%-7s %-22s %5d  lag %-7s", p.Symbol, fmt.Sprintf("%.5g / %.5g", snap.SpotBid, snap.SpotAsk),
				len(evs)-1, lag.Round(time.Second))
			for _, tn := range []string{"1W", "1M", "3M", "1Y"} {
				q := sc.Evaluate(sc.PairIndex(p.Symbol), market.TenorIndex(tn))
				if !q.OK {
					line += fmt.Sprintf(" %s: -", tn)
					continue
				}
				line += fmt.Sprintf(" %s: %.2f/%.2f|%.2f|%+.0f", tn, q.PtsBid, q.PtsAsk, q.FairPts, q.DeviationBps)
			}
			fmt.Println(line)
		}

	case "record":
		// The unattended collector: no dashboard, just read the live source every minute and
		// append to disk until stopped. Leave it running on a machine that stays on.
		every := fs.Duration("every", time.Minute, "how often each pair is re-read")
		fs.Parse(args)
		hub, err := collect.Start(context.Background(), *dir, *every, logLine)
		die(err)
		logLine("collector: recording %d pairs every %s into %s (Ctrl+C to stop)", len(market.Pairs), *every, *dir)
		for range time.Tick(30 * time.Minute) {
			sum := hub.Summary()
			logLine("collector: %d pairs reporting, forward quotes about %d min behind, last reading %s",
				sum.PairsOK, sum.LagSec/60, data.FormatTS(sum.Last))
		}

	case "serve":
		addr := fs.String("addr", "127.0.0.1:8080", "listen address")
		web := fs.String("web", "web/dist", "built dashboard directory")
		live := fs.Bool("live", false, "read live forward points from investing.com, scan them and record them")
		every := fs.Duration("every", time.Minute, "how often each pair is re-read")
		fs.Parse(args)
		// The only market data is what the collector has recorded: real quotes, nothing derived.
		srv := &api.Server{DataDir: *dir, WebDir: *web,
			Sets: []api.Dataset{{Name: "recorded", Label: "Recorded live quotes", Dir: *dir}}}
		if *live {
			hub, err := collect.Start(context.Background(), *dir, *every, logLine)
			die(err)
			srv.Live, srv.LiveDir = hub, *dir
			fmt.Printf("live feed on: reading %d pairs every %s, recording to %s\n", len(market.Pairs), *every, *dir)
		}
		fmt.Printf("fxarb dashboard on http://%s (data: %s)\n", *addr, *dir)
		die(http.ListenAndServe(*addr, srv.Handler()))

	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		os.Exit(2)
	}
}

// logLine prints a time-stamped line; the collector's output is meant to be kept as a log.
func logLine(format string, a ...any) {
	fmt.Printf(time.Now().UTC().Format("2006-01-02 15:04:05Z ")+format+"\n", a...)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printReport(r *backtest.Report) {
	c := r.Params.Cfg
	fmt.Printf("\nBACK-TEST  %s -> %s", data.FormatTS(r.DataFrom), data.FormatTS(r.DataTo))
	fmt.Printf("\n  assumptions   borrow spread %s, min edge %.1fbp, taker prices on every leg, checked %ds after the flag, $%.0f size\n",
		map[bool]string{true: fmt.Sprintf("%.0fbp", c.BorrowSpreadBps), false: "OFF"}[c.ApplyBorrowSpread],
		c.MinEdgeBps, r.Params.ExecDelaySec, c.NotionalUSD)
	fmt.Printf("  processed     %d events, %d evaluations in %.0f ms (%.1fM events/s)\n",
		r.Events, r.Evaluations, r.WallMs, r.EventsPerSec/1e6)
	fmt.Printf("  flags         %d   still there when checked %d   already gone %d\n", r.Alerts, r.Trades, r.Missed)
	fmt.Printf("  potential P&L $%.0f total   $%.0f avg/flag   avg edge %.1fbp   best $%.0f\n",
		r.TotalPnLUSD, r.AvgPnLUSD, r.AvgEdgeBps, r.BestTradePnLUSD)
	fmt.Println("  by tenor")
	for _, b := range r.ByTenor {
		fmt.Printf("    %-3s  %4d flags  %4d still there  $%12.0f\n", b.Key, b.Alerts, b.Trades, b.PnLUSD)
	}
	fmt.Println("  top pairs")
	for i, b := range r.ByPair {
		if i == 8 {
			break
		}
		fmt.Printf("    %-7s %4d flags  %4d still there  $%12.0f\n", b.Key, b.Alerts, b.Trades, b.PnLUSD)
	}
}

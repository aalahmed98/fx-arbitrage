// Package collect runs the live collector: it reads forward points from the live source every
// minute, records them to disk and keeps the interest-rate table current. The dashboard
// (fxarb serve -live) and the unattended collector (fxarb record) both start it from here.
package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fxarb/internal/backtest"
	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/feed"
	"fxarb/internal/fetch"
	"fxarb/internal/market"
)

// Start begins collecting into dir and returns the hub that live dashboards attach to.
// It returns once the collector is running; the work continues until ctx is cancelled.
func Start(ctx context.Context, dir string, every time.Duration, logf func(string, ...any)) (*feed.Hub, error) {
	// The recorded history carries its own rates table so it can be back-tested by itself.
	known := refreshRates(dir, nil, logf)
	if known == nil {
		return nil, fmt.Errorf("no interest rates available: %s has no rates.csv and the BIS could not be reached", dir)
	}

	hub := feed.NewHub("investing.com")

	// Learn each pair's normal gap to parity from what has been recorded so far; the hub
	// keeps learning from the live feed.
	master := engine.NewScanner(engine.DefaultConfig(), nil)
	learned := 0
	if st, err := data.OpenStream(dir, backtest.Symbols(market.Pairs), time.Time{}, time.Time{}); err == nil {
		for {
			batch, err := st.NextBatch()
			if err != nil || batch == nil || master.Learn(batch) != nil {
				break
			}
			hub.Prime(batch)
			learned += len(batch)
		}
		st.Close()
	} else if rows, err := data.LoadRates(dir); err == nil { // nothing recorded yet: just load the rates
		now := time.Now().UTC()
		for _, r := range rows {
			if !r.Date.After(now) {
				master.Apply(engine.Event{TS: r.Date, Kind: engine.EvRate, Ccy: r.Ccy, Tenor: r.Tenor, Rate: r.Rate})
			}
		}
	}
	hub.SetMaster(master)
	logf("collector: learned normal levels from %d recorded quotes", learned)

	go func() {
		// Two collectors writing the same files would interleave their rows, so make sure no
		// other one is running here before recording.
		if err := waitUntilQuiet(ctx, dir, every); err != nil {
			logf("collector: NOT recording: %v", err)
			return
		}
		feed.Record(ctx, hub, feed.NewInvesting(), market.Pairs, every, dir, logf)
	}()
	go func() { // policy rates change a few times a year; check twice a day
		t := time.NewTicker(12 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				latest := refreshRates(dir, hub, logf)
				for ccy, rate := range latest {
					if old, ok := known[ccy]; ok && old != rate {
						logf("collector: %s policy rate changed from %.3f%% to %.3f%%", ccy, old, rate)
					}
				}
				if latest != nil {
					known = latest
				}
			}
		}
	}()
	return hub, nil
}

// refreshRates downloads central-bank policy rates into dir (keeping any tenor rates already
// there) and returns the latest policy rate per currency. If the download fails it falls back
// to the file on disk; nil means there are no rates at all. When hub is set, a rate that has
// changed is published as an event stamped now, so running scanners pick it up from the
// moment we knew it.
func refreshRates(dir string, hub *feed.Hub, logf func(string, ...any)) map[string]float64 {
	existing, _ := data.LoadRates(dir)
	before := latestPolicy(existing)
	rows, err := fetch.PolicyRates(time.Now().UTC().AddDate(-2, 0, 0))
	if err != nil {
		logf("collector: could not refresh policy rates (%v); using the saved table", err)
		if len(before) == 0 {
			return nil
		}
		return before
	}
	merged := fetch.MergeRates(existing, rows)
	if err := data.SaveRates(dir, merged); err != nil {
		logf("collector: could not save rates: %v", err)
	}
	after := latestPolicy(merged)
	if hub != nil {
		now := time.Now().UTC().Truncate(time.Second)
		var batch []engine.Event
		for ccy, rate := range after {
			if old, ok := before[ccy]; !ok || old != rate {
				batch = append(batch, engine.Event{TS: now, Kind: engine.EvRate, Ccy: ccy, Tenor: engine.PolicyTenor, Rate: rate})
			}
		}
		hub.Publish(batch)
	}
	return after
}

// lastWrite is the most recent modification time among a directory's current spot files.
func lastWrite(dir string) time.Time {
	var newest time.Time
	files, _ := filepath.Glob(filepath.Join(dir, "spot", "*.csv"))
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(newest) {
			newest = st.ModTime()
		}
	}
	return newest
}

// waitUntilQuiet returns once nothing has written to dir for more than a full cycle. If
// files keep changing while it waits, another collector is running and it gives up.
func waitUntilQuiet(ctx context.Context, dir string, every time.Duration) error {
	quiet := every + 20*time.Second
	seen := lastWrite(dir)
	if time.Since(seen) > quiet {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(seen.Add(quiet))):
	}
	if lastWrite(dir).After(seen) {
		return fmt.Errorf("another collector is already writing to %s; stop it first, or run the dashboard without -live", dir)
	}
	return nil
}

func latestPolicy(rows []data.RateRow) map[string]float64 {
	out := map[string]float64{}
	now := time.Now().UTC()
	for _, r := range rows { // rows are sorted by date
		if r.Tenor == engine.PolicyTenor && !r.Date.After(now) {
			out[r.Ccy] = r.Rate
		}
	}
	return out
}

package backtest

import (
	"fmt"
	"math"
	"testing"
	"time"

	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/market"
	"fxarb/internal/testgen"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// buildSet writes a small made-up data set (two trading days) into a temp dir and returns
// the gaps that were planted in it.
func buildSet(t *testing.T) (dir string, from, to time.Time, planted []testgen.Planted) {
	t.Helper()
	dir = t.TempDir()
	from = time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	to = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rows []data.RateRow
	for ccy, r := range map[string]float64{"USD": 3.625, "EUR": 2.15, "GBP": 3.75, "JPY": 0.75, "CHF": 0,
		"CAD": 2.25, "AUD": 3.6, "NZD": 2.5, "SEK": 1.75, "NOK": 4, "DKK": 1.6, "PLN": 4.25, "CZK": 3.5,
		"HUF": 6.5, "MXN": 7.25, "ZAR": 6.75, "TRY": 38, "HKD": 4, "SAR": 4.25} {
		rows = append(rows, data.RateRow{Date: early, Ccy: ccy, Tenor: engine.PolicyTenor, Rate: r})
	}
	// A rate change in the middle of the window: must only take effect from that day.
	rows = append(rows, data.RateRow{Date: from.AddDate(0, 0, 1), Ccy: "GBP", Tenor: engine.PolicyTenor, Rate: 3.5})
	must(t, data.SaveRates(dir, rows))
	planted, err := testgen.Generate(testgen.Params{Dir: dir, From: from, To: to, Seed: 3, EventsPerWeek: 4})
	must(t, err)
	return dir, from, to, planted
}

func signature(e *engine.Episode) string {
	return fmt.Sprintf("%s %s %s %s edge=%.6f status=%s fill=%s pnl=%.4f",
		e.Pair, e.Tenor, e.Direction, data.FormatTS(e.DetectedAt), e.EdgeAtDetectBps, e.Status,
		data.FormatTS(e.FillAt), e.PnLUSD)
}

// TestNoLookAhead is the core guarantee: what the scanner reports up to time T must not
// depend on anything that happens after T. We run the full data set, then re-run on data
// cut off at several points, and require every decision made before the cut to be identical.
func TestNoLookAhead(t *testing.T) {
	dir, from, to, _ := buildSet(t)
	pairs := []string{"EURUSD", "GBPUSD", "USDJPY", "USDMXN"}
	for _, reference := range []string{"parity", "normal"} {
		cfg := engine.DefaultConfig()
		cfg.BorrowSpreadBps = 10
		cfg.Reference = reference // "normal" also exercises the learned levels: they may use the past only
		full, err := Run(Params{DataDir: dir, From: from, To: to, Cfg: cfg, ExecDelaySec: 60, Pairs: pairs})
		must(t, err)
		if full.Alerts < 5 {
			t.Fatalf("%s: test set too quiet to be meaningful: %d flags", reference, full.Alerts)
		}
		for _, hours := range []int{5, 13, 22, 31, 40} {
			cut := from.Add(time.Duration(hours) * time.Hour)
			part, err := Run(Params{DataDir: dir, From: from, To: cut, Cfg: cfg, ExecDelaySec: 60, Pairs: pairs})
			must(t, err)
			var want []*engine.Episode
			for _, e := range full.Episodes {
				if e.DetectedAt.Before(cut) {
					want = append(want, e)
				}
			}
			if len(part.Episodes) != len(want) {
				t.Fatalf("%s, cut at +%dh: %d flags, but the full run had %d before the cut", reference, hours, len(part.Episodes), len(want))
			}
			for i, e := range part.Episodes {
				w := want[i]
				if e.Pair != w.Pair || e.Tenor != w.Tenor || e.Direction != w.Direction ||
					!e.DetectedAt.Equal(w.DetectedAt) || e.EdgeAtDetectBps != w.EdgeAtDetectBps {
					t.Fatalf("%s, cut at +%dh, flag %d differs:\n  cut : %s\n  full: %s", reference, hours, i, signature(e), signature(w))
				}
				// Reaction checks that had already happened before the cut must also be identical.
				if w.Status == "filled" && w.FillAt.Before(cut) && signature(e) != signature(w) {
					t.Fatalf("%s, cut at +%dh, check %d differs:\n  cut : %s\n  full: %s", reference, hours, i, signature(e), signature(w))
				}
			}
		}
	}
}

func TestScannerRejectsOutOfOrderEvents(t *testing.T) {
	sc := engine.NewScanner(engine.DefaultConfig(), nil)
	t1 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	must(t, sc.Apply(engine.Event{TS: t1, Kind: engine.EvSpot, Pair: "EURUSD", Bid: 1.1, Ask: 1.1002}))
	if err := sc.Apply(engine.Event{TS: t1.Add(-time.Minute), Kind: engine.EvSpot, Pair: "EURUSD", Bid: 1.1, Ask: 1.1002}); err == nil {
		t.Fatal("an event from the past must be rejected")
	}
}

// scored is how a run did against the gaps planted in the test data.
type scored struct {
	worthFlagging int      // planted gaps big enough to clear every cost and the threshold
	caught        int      // of those, how many were flagged
	missed        []string // the ones that were not, with their headroom
	worstMiss     float64  // largest headroom (bp) among the missed ones
}

// score checks, after a run has finished, which planted gaps were flagged. It never feeds
// anything back into the scanner.
func score(planted []testgen.Planted, rep *Report, p Params) scored {
	var s scored
	for _, g := range planted {
		if g.End.Before(rep.DataFrom) || g.Start.After(rep.DataTo) {
			continue
		}
		// Against the "normal" level nothing can be flagged until that level has been learned.
		if p.Cfg.Reference == "normal" && g.Start.Before(rep.DataFrom.Add(time.Duration(p.Cfg.NormalWarmupMin+10)*time.Minute)) {
			continue
		}
		pr, _ := market.PairBySymbol(g.Pair)
		borrow := pr.Quote // forward rich -> borrow quote
		if g.PeakBps < 0 {
			borrow = pr.Base
		}
		headroom := math.Abs(g.PeakBps) - g.CostBps - p.Cfg.BorrowBps(borrow) - p.Cfg.MinEdgeBps
		headroom -= g.SpotCostBps // a taker also pays the spot spread
		if headroom < 0 {
			continue
		}
		s.worthFlagging++
		hit := false
		for _, e := range rep.Episodes {
			end := e.EndedAt
			if e.Open() {
				end = rep.DataTo
			}
			if e.Pair == g.Pair && e.Tenor == g.Tenor && !e.FirstSeenAt.After(g.End) && !end.Before(g.Start) {
				hit = true
			}
		}
		if hit {
			s.caught++
		} else {
			s.missed = append(s.missed, fmt.Sprintf("%s %s at %s (headroom %.1fbp)", g.Pair, g.Tenor, data.FormatTS(g.Start), headroom))
			s.worstMiss = math.Max(s.worstMiss, headroom)
		}
	}
	return s
}

func TestFundingSpreadReducesFlagsAndPlantedGapsAreCaught(t *testing.T) {
	dir, from, to, planted := buildSet(t)
	run := func(apply bool) (*Report, Params) {
		cfg := engine.DefaultConfig()
		cfg.ApplyBorrowSpread = apply
		cfg.Reference = "parity" // the planted gaps are relative to parity
		p := Params{DataDir: dir, From: from, To: to, Cfg: cfg, ExecDelaySec: 60}
		r, err := Run(p)
		must(t, err)
		return r, p
	}
	off, _ := run(false)
	on, p := run(true)
	if on.Alerts >= off.Alerts || on.TotalPnLUSD >= off.TotalPnLUSD {
		t.Fatalf("a 25bp funding spread should cut flags and potential P&L: off %d/$%.0f, on %d/$%.0f",
			off.Alerts, off.TotalPnLUSD, on.Alerts, on.TotalPnLUSD)
	}
	for _, e := range on.Episodes {
		if e.Status == "filled" && e.PnLUSD <= 0 {
			t.Fatalf("a flag still there when checked must show a positive potential P&L: %s", signature(e))
		}
	}
	s := score(planted, on, p)
	if s.worthFlagging == 0 {
		t.Fatal("expected planted gaps to score against")
	}
	// A planted gap may be missed only when it was marginal: the background noise (up to ~5bp
	// sd) can cancel a peak that clears the threshold by a few bp, and it must hold for the
	// confirmation time. Anything with real headroom must be caught.
	if s.worstMiss > 12 {
		t.Errorf("missed a clear gap: %v", s.missed)
	}
	if float64(s.caught) < 0.85*float64(s.worthFlagging) {
		t.Errorf("caught only %d of %d planted gaps worth flagging; missed %v", s.caught, s.worthFlagging, s.missed)
	}
}

func TestStreamIsTimeOrdered(t *testing.T) {
	dir, from, to, _ := buildSet(t)
	st, err := data.OpenStream(dir, []string{"EURUSD", "USDJPY"}, from, to)
	must(t, err)
	defer st.Close()
	var prev time.Time
	n := 0
	for {
		b, err := st.NextBatch()
		must(t, err)
		if b == nil {
			break
		}
		for _, ev := range b {
			if !ev.TS.Equal(b[0].TS) {
				t.Fatal("a batch mixed timestamps")
			}
		}
		if !b[0].TS.After(prev) {
			t.Fatalf("batches out of order: %s then %s", prev, b[0].TS)
		}
		prev = b[0].TS
		n += len(b)
	}
	if n == 0 {
		t.Fatal("empty stream")
	}
}

// Package backtest replays historical data through the scanner, one timestamp at a time,
// and reports what it would have alerted on and the P&L of trading those alerts.
package backtest

import (
	"math"
	"sort"
	"time"

	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/market"
)

// Params configures one run.
type Params struct {
	DataDir      string        `json:"-"`
	From         time.Time     `json:"from"`
	To           time.Time     `json:"to"`
	Cfg          engine.Config `json:"config"`
	ExecDelaySec int           `json:"execDelaySec"` // time between the alert and the trade
	Pairs        []string      `json:"pairs"`        // empty = whole universe
}

// Bucket is P&L grouped by pair or tenor.
type Bucket struct {
	Key    string  `json:"key"`
	Alerts int     `json:"alerts"`
	Trades int     `json:"trades"`
	PnLUSD float64 `json:"pnlUSD"`
}

// EquityPoint is cumulative P&L after one trade.
type EquityPoint struct {
	TS     time.Time `json:"ts"`
	PnLUSD float64   `json:"pnlUSD"`
}

// Report is the result of a run.
type Report struct {
	Params   Params    `json:"params"`
	DataFrom time.Time `json:"dataFrom"`
	DataTo   time.Time `json:"dataTo"`

	Events       int64   `json:"events"`
	Evaluations  int64   `json:"evaluations"`
	WallMs       float64 `json:"wallMs"`
	EventsPerSec float64 `json:"eventsPerSec"`

	Alerts          int     `json:"alerts"`
	Trades          int     `json:"trades"`
	Missed          int     `json:"missed"`
	TotalPnLUSD     float64 `json:"totalPnLUSD"`
	AvgPnLUSD       float64 `json:"avgPnLUSD"`
	AvgEdgeBps      float64 `json:"avgEdgeBps"` // at fill, annualised
	MedianDurSec    float64 `json:"medianDurSec"`
	BestTradePnLUSD float64 `json:"bestTradePnLUSD"`

	ByPair   []Bucket          `json:"byPair"`
	ByTenor  []Bucket          `json:"byTenor"`
	Equity   []EquityPoint     `json:"equity"`
	Episodes []*engine.Episode `json:"episodes"`
}

// SelectPairs resolves symbols to universe pairs (empty = all).
func SelectPairs(symbols []string) []market.Pair {
	if len(symbols) == 0 {
		return market.Pairs
	}
	var out []market.Pair
	for _, s := range symbols {
		if p, ok := market.PairBySymbol(s); ok {
			out = append(out, p)
		}
	}
	return out
}

// Symbols lists the symbols of a set of pairs.
func Symbols(pairs []market.Pair) []string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.Symbol
	}
	return out
}

// Run replays the data set through a fresh scanner and builds the report.
func Run(p Params) (*Report, error) {
	pairs := SelectPairs(p.Pairs)
	st, err := data.OpenStream(p.DataDir, Symbols(pairs), p.From, p.To)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	tr := engine.NewTracker(engine.NewScanner(p.Cfg, pairs), time.Duration(p.ExecDelaySec)*time.Second)
	rep := &Report{Params: p}
	start := time.Now()
	for {
		batch, err := st.NextBatch()
		if err != nil {
			return nil, err
		}
		if batch == nil {
			break
		}
		if _, err := tr.Step(batch); err != nil {
			return nil, err
		}
		if engine.HasQuotes(batch) { // rate history replayed before the first quote is warm-up
			if rep.DataFrom.IsZero() {
				rep.DataFrom = batch[0].TS
			}
			rep.DataTo = batch[0].TS
		}
	}
	tr.Finish()
	wall := time.Since(start)

	rep.Events, rep.Evaluations = tr.Events, tr.Evaluations
	rep.WallMs = float64(wall.Microseconds()) / 1000
	if wall > 0 {
		rep.EventsPerSec = float64(tr.Events) / wall.Seconds()
	}
	Summarize(rep, tr.Episodes, tr.SeenTenor)

	return rep, nil
}

// Summarize fills the headline numbers, groupings and equity curve from a list of episodes.
// seenTenor marks the tenors the data set actually quoted; only those get a by-tenor row.
func Summarize(rep *Report, eps []*engine.Episode, seenTenor []bool) {
	rep.Episodes = eps
	if rep.Episodes == nil {
		rep.Episodes = []*engine.Episode{} // an empty list, never null, in the JSON report
	}
	rep.Alerts = len(eps)
	byPair, byTenor := map[string]*Bucket{}, map[string]*Bucket{}
	bucket := func(m map[string]*Bucket, k string) *Bucket {
		if m[k] == nil {
			m[k] = &Bucket{Key: k}
		}
		return m[k]
	}
	var durs []float64
	var edgeSum float64
	fills := make([]*engine.Episode, 0, len(eps))
	for _, e := range eps {
		bp, bt := bucket(byPair, e.Pair), bucket(byTenor, e.Tenor)
		bp.Alerts++
		bt.Alerts++
		if !e.Open() {
			durs = append(durs, float64(e.DurationSec))
		}
		switch e.Status {
		case "filled":
			rep.Trades++
			rep.TotalPnLUSD += e.PnLUSD
			edgeSum += e.EdgeAtFillBps
			rep.BestTradePnLUSD = math.Max(rep.BestTradePnLUSD, e.PnLUSD)
			bp.Trades++
			bt.Trades++
			bp.PnLUSD += e.PnLUSD
			bt.PnLUSD += e.PnLUSD
			fills = append(fills, e)
		case "missed":
			rep.Missed++
		}
	}
	if rep.Trades > 0 {
		rep.AvgPnLUSD = rep.TotalPnLUSD / float64(rep.Trades)
		rep.AvgEdgeBps = edgeSum / float64(rep.Trades)
	}
	rep.MedianDurSec = median(durs)

	sort.SliceStable(fills, func(a, b int) bool { return fills[a].FillAt.Before(fills[b].FillAt) })
	cum := 0.0
	rep.Equity = make([]EquityPoint, 0, len(fills))
	for _, e := range fills {
		cum += e.PnLUSD
		rep.Equity = append(rep.Equity, EquityPoint{e.FillAt, cum})
	}

	rep.ByPair = []Bucket{}
	for _, b := range byPair {
		rep.ByPair = append(rep.ByPair, *b)
	}
	sort.Slice(rep.ByPair, func(a, b int) bool {
		if rep.ByPair[a].PnLUSD != rep.ByPair[b].PnLUSD {
			return rep.ByPair[a].PnLUSD > rep.ByPair[b].PnLUSD
		}
		return rep.ByPair[a].Key < rep.ByPair[b].Key
	})
	rep.ByTenor = []Bucket{}
	for j, t := range market.Tenors {
		if b := byTenor[t.Code]; b != nil {
			rep.ByTenor = append(rep.ByTenor, *b)
		} else if j < len(seenTenor) && seenTenor[j] {
			rep.ByTenor = append(rep.ByTenor, Bucket{Key: t.Code})
		}
	}
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

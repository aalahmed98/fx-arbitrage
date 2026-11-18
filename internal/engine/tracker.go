package engine

import (
	"time"

	"fxarb/internal/market"
)

// Episode is one flag: a gap from the moment it is confirmed until it closes.
type Episode struct {
	ID        int    `json:"id"`
	Pair      string `json:"pair"`
	Tenor     string `json:"tenor"`
	Direction string `json:"direction"`
	BorrowCcy string `json:"borrowCcy"`
	Days      int    `json:"days"`

	FirstSeenAt time.Time `json:"firstSeenAt"` // when the gap first cleared the threshold
	DetectedAt  time.Time `json:"detectedAt"`  // when it was flagged (after the confirmation wait)
	EndedAt     time.Time `json:"endedAt"`     // zero while still open
	DurationSec int       `json:"durationSec"`

	EdgeAtDetectBps float64 `json:"edgeAtDetectBps"` // net annualised edge on the flag
	PeakEdgeBps     float64 `json:"peakEdgeBps"`
	DeviationBps    float64 `json:"deviationBps"` // gross deviation from parity on the flag

	// Status says whether a trader who reacted ExecDelay after the flag would still have
	// found the gap: "filled" = still there, "missed" = already gone, "pending" = not yet due.
	Status        string    `json:"status"`
	FillAt        time.Time `json:"fillAt"`
	EdgeAtFillBps float64   `json:"edgeAtFillBps"`
	PnLUSD        float64   `json:"pnlUSD"` // potential P&L of dealing the notional once at that moment

	Detect   Quote `json:"detect"` // full market picture on the flag, for audit
	Injected *bool `json:"injected,omitempty"`

	fillDue time.Time
	open    bool
}

// Open reports whether the gap is still live.
func (e *Episode) Open() bool { return e.open }

// StepResult lists what changed during one Tracker.Step.
type StepResult struct {
	Opened []*Episode // new flags
	Filled []*Episode // flags whose reaction-time check came due this step
	Closed []*Episode // gaps that closed this step
}

// Tracker turns the scanner's minute-by-minute signals into flags for traders.
//
// A flag is raised once a gap has cleared the threshold continuously for Config.ConfirmSec.
// It then stays open, as ONE flag, until the edge is gone altogether, so a gap that hovers
// around the threshold is not flagged over and over.
//
// For the back-test each flag is also checked ExecDelay later: if the gap is still there it
// is counted as actionable and its potential P&L is recorded at the prices of that moment.
// Nothing is ever traded.
type Tracker struct {
	SC        *Scanner
	ExecDelay time.Duration
	Episodes  []*Episode

	Events      int64
	Evaluations int64
	SeenTenor   []bool // tenors that have had at least one usable quote

	active  map[int]*Episode // key = pair*nTenors + tenor
	pending map[int]*Episode
	cand    []candidate // a gap being watched before it is confirmed
	dirty   []bool
	nT      int
}

type candidate struct {
	since time.Time
	dir   string
}

// NewTracker wraps a scanner.
func NewTracker(sc *Scanner, execDelay time.Duration) *Tracker {
	nT := len(market.Tenors)
	n := len(sc.Pairs()) * nT
	return &Tracker{SC: sc, ExecDelay: execDelay, nT: nT,
		active: map[int]*Episode{}, pending: map[int]*Episode{}, SeenTenor: make([]bool, nT),
		cand: make([]candidate, n), dirty: make([]bool, n)}
}

// Step applies a batch of events that share one timestamp, then re-evaluates what changed.
func (t *Tracker) Step(batch []Event) (StepResult, error) {
	var res StepResult
	sc := t.SC
	fresh := map[int]bool{} // keys that received a new forward or spot quote in this batch
	for _, ev := range batch {
		if err := sc.Apply(ev); err != nil {
			return res, err
		}
		t.Events++
		switch ev.Kind {
		case EvSpot:
			if i := sc.PairIndex(ev.Pair); i >= 0 {
				for j := 0; j < t.nT; j++ {
					t.dirty[i*t.nT+j] = true
				}
			}
		case EvForward:
			if i := sc.PairIndex(ev.Pair); i >= 0 {
				if j := market.TenorIndex(ev.Tenor); j >= 0 {
					t.dirty[i*t.nT+j] = true
					fresh[i*t.nT+j] = true
				}
			}
		case EvRate:
			for i, p := range sc.Pairs() {
				if p.Base == ev.Ccy || p.Quote == ev.Ccy {
					for j := 0; j < t.nT; j++ {
						t.dirty[i*t.nT+j] = true
					}
				}
			}
		}
	}
	// Live flags, flags awaiting their reaction-time check and gaps awaiting confirmation
	// are re-checked every step even without a new tick, so a quote going stale closes them.
	for k := range t.active {
		t.dirty[k] = true
	}
	for k := range t.pending {
		t.dirty[k] = true
	}
	for k := range t.cand {
		if !t.cand[k].since.IsZero() {
			t.dirty[k] = true
		}
	}

	now := sc.Now()
	confirm := time.Duration(sc.Config().ConfirmSec) * time.Second
	for k, d := range t.dirty {
		if !d {
			continue
		}
		t.dirty[k] = false
		i, j := k/t.nT, k%t.nT
		q := sc.Evaluate(i, j)
		t.Evaluations++
		if q.OK {
			t.SeenTenor[j] = true
		}

		ep := t.active[k]
		// An open flag survives while any positive edge remains in the same direction.
		if ep != nil && !(q.OK && q.EdgeBps > 0 && q.Direction == ep.Direction) {
			ep.open = false
			ep.EndedAt = now
			ep.DurationSec = int(now.Sub(ep.DetectedAt).Seconds())
			delete(t.active, k)
			res.Closed = append(res.Closed, ep)
			if ep.Status == "pending" { // gone before anyone could have acted on it
				ep.Status = "missed"
				delete(t.pending, k)
				res.Filled = append(res.Filled, ep)
			}
			ep = nil
		}
		if ep == nil {
			c := &t.cand[k]
			switch {
			case !q.Signal:
				*c = candidate{}
			case c.since.IsZero() || c.dir != q.Direction:
				*c = candidate{since: now, dir: q.Direction}
			}
			if q.Signal && now.Sub(c.since) >= confirm {
				ep = &Episode{ID: len(t.Episodes) + 1, Pair: q.Pair, Tenor: q.Tenor, Direction: q.Direction,
					BorrowCcy: q.BorrowCcy, Days: q.Days, FirstSeenAt: c.since, DetectedAt: now,
					EdgeAtDetectBps: q.EdgeBps, PeakEdgeBps: q.EdgeBps, DeviationBps: q.DeviationBps,
					Status: "pending", Detect: q, fillDue: now.Add(t.ExecDelay), open: true}
				*c = candidate{}
				t.Episodes = append(t.Episodes, ep)
				t.active[k] = ep
				t.pending[k] = ep
				res.Opened = append(res.Opened, ep)
			}
		}
		if ep != nil {
			if q.EdgeBps > ep.PeakEdgeBps {
				ep.PeakEdgeBps = q.EdgeBps
			}
			if ep.Status == "pending" && !now.Before(ep.fillDue) {
				ep.Status = "filled"
				ep.FillAt = now
				ep.EdgeAtFillBps = q.EdgeBps
				ep.PnLUSD = q.ProfitUSD
				delete(t.pending, k)
				res.Filled = append(res.Filled, ep)
			}
		}
		// Only now does this reading join the pair's "normal" level, and only when it is a
		// genuinely new forward quote rather than a re-check of an old one.
		if q.OK && fresh[k] {
			sc.Observe(i, j, q.GrossSellBps, q.GrossBuyBps)
		}
	}
	return res, nil
}

// HasQuotes reports whether a batch carries any price (as opposed to only rate changes).
func HasQuotes(batch []Event) bool {
	for _, ev := range batch {
		if ev.Kind != EvRate {
			return true
		}
	}
	return false
}

// Finish stamps flags still open at the end of the data.
func (t *Tracker) Finish() {
	now := t.SC.Now()
	for _, ep := range t.active {
		ep.DurationSec = int(now.Sub(ep.DetectedAt).Seconds())
	}
}

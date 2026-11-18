// Package engine is the live scanner. It is a pure state machine: events go in one at a
// time in timestamp order, and every evaluation uses only what has already arrived.
// The scanner holds no reference to any data source, so it cannot see the future.
package engine

import (
	"fmt"
	"math"
	"time"

	"fxarb/internal/cip"
	"fxarb/internal/market"
)

// EventKind tags a market-data event.
type EventKind uint8

const (
	EvSpot    EventKind = iota // Pair, Bid, Ask (outright prices)
	EvForward                  // Pair, Tenor, Bid, Ask (forward points)
	EvRate                     // Ccy, Tenor ("POLICY" or a tenor code), Rate (percent)
)

// PolicyTenor is the rates-table key for a central-bank policy rate.
const PolicyTenor = "POLICY"

// Event is one market-data update.
type Event struct {
	TS    time.Time
	Kind  EventKind
	Pair  string
	Tenor string
	Ccy   string
	Bid   float64
	Ask   float64
	Rate  float64
}

// Config holds the trader's assumptions. All of it can be changed from the UI.
type Config struct {
	ApplyBorrowSpread bool               `json:"applyBorrowSpread"` // the funding-cost toggle
	BorrowSpreadBps   float64            `json:"borrowSpreadBps"`   // added to the rate of the currency we borrow
	BorrowSpreadByCcy map[string]float64 `json:"borrowSpreadByCcy"` // optional per-currency override, in bps
	MinEdgeBps        float64            `json:"minEdgeBps"`        // annualised net edge needed to raise a flag
	MaxStaleSec       int                `json:"maxStaleSec"`       // ignore quotes older than this
	NotionalUSD       float64            `json:"notionalUSD"`       // trade size used for the potential P&L

	// ConfirmSec: a gap must hold this long before it is flagged, so a one-minute blip from a
	// stale or thin quote does not reach a trader.
	ConfirmSec int `json:"confirmSec"`
	// Reference chooses what a gap is measured against.
	//   "parity": the forward implied by spot and the interest rates (the textbook test).
	//   "normal": the pair's own recent average gap to parity, so a standing gap (policy rates
	//             that differ from market rates, or the cross-currency basis) stops flagging
	//             and only a move away from the usual level does.
	Reference         string  `json:"reference"`
	NormalHalfLifeMin float64 `json:"normalHalfLifeMin"` // memory of the "normal" level, in minutes of market time
	NormalWarmupMin   float64 `json:"normalWarmupMin"`   // history needed before flagging against normal
}

// DefaultConfig is the starting set of assumptions.
func DefaultConfig() Config {
	return Config{ApplyBorrowSpread: true, BorrowSpreadBps: 25, MinEdgeBps: 5,
		MaxStaleSec: 300, NotionalUSD: 10_000_000,
		ConfirmSec: 120, Reference: "parity", NormalHalfLifeMin: 1440, NormalWarmupMin: 240}
}

// BorrowBps returns the funding spread applied when borrowing ccy.
func (c Config) BorrowBps(ccy string) float64 {
	if !c.ApplyBorrowSpread {
		return 0
	}
	if v, ok := c.BorrowSpreadByCcy[ccy]; ok {
		return v
	}
	return c.BorrowSpreadBps
}

// Quote is the full analysis of one pair/tenor at one moment.
type Quote struct {
	OK     bool      `json:"ok"` // false if a price or rate is missing or stale
	TS     time.Time `json:"ts"`
	Pair   string    `json:"pair"`
	Tenor  string    `json:"tenor"`
	Days   int       `json:"days"`
	Reason string    `json:"reason,omitempty"` // why OK is false

	SpotBid float64 `json:"spotBid"`
	SpotAsk float64 `json:"spotAsk"`
	PtsBid  float64 `json:"ptsBid"`
	PtsAsk  float64 `json:"ptsAsk"`
	FwdBid  float64 `json:"fwdBid"`
	FwdAsk  float64 `json:"fwdAsk"`

	RateBase     float64 `json:"rateBase"` // percent
	RateQuote    float64 `json:"rateQuote"`
	RateSrcBase  string  `json:"rateSrcBase"` // "POLICY" or the tenor code of a market curve point
	RateSrcQuote string  `json:"rateSrcQuote"`

	// Everything below is priced as a market taker: buy at the offer, sell at the bid, on
	// both the spot and the forward. No mid price is used.
	MktFwd       float64 `json:"mktFwd"`       // the forward price we would deal at for Direction
	FairFwd      float64 `json:"fairFwd"`      // the formula forward built on the spot side we would deal at
	FairPts      float64 `json:"fairPts"`      // FairFwd as points over that spot side
	BandLow      float64 `json:"bandLow"`      // forward offer below this = profit after funding
	BandHigh     float64 `json:"bandHigh"`     // forward bid above this = profit after funding
	GrossPips    float64 `json:"grossPips"`    // pips in our favour for Direction before funding costs (negative = against us)
	DeviationBps float64 `json:"deviationBps"` // taker gap before funding costs, a year: + overpriced, - underpriced, 0 inside the spread
	GrossSellBps float64 `json:"-"`            // sell-the-forward trade before funding costs, a year
	GrossBuyBps  float64 `json:"-"`            // buy-the-forward trade before funding costs, a year

	// NormalBps is what the Direction trade usually shows before funding costs (a slow average
	// over past readings only); NormalReady is false until enough history has built up.
	NormalBps   float64 `json:"normalBps"`
	NormalReady bool    `json:"normalReady"`
	Reference   string  `json:"reference"` // what EdgeBps is measured against: "parity" or "normal"

	Direction     string  `json:"direction"`
	BorrowCcy     string  `json:"borrowCcy"`
	BorrowBps     float64 `json:"borrowBps"`     // spread applied to BorrowCcy
	EdgeBps       float64 `json:"edgeBps"`       // net, annualised
	EdgePeriodBps float64 `json:"edgePeriodBps"` // net, over the life of the trade
	EdgePips      float64 `json:"edgePips"`      // net, as pips of the forward price
	ProfitUSD     float64 `json:"profitUSD"`     // EdgePeriod x notional, received at maturity
	Signal        bool    `json:"signal"`        // EdgeBps >= MinEdgeBps
}

// normal is the running "usual level" of one pair and tenor's gap to parity.
type normal struct {
	sell  float64 // usual gross edge of selling the forward, bp a year
	buy   float64 // usual gross edge of buying the forward, bp a year
	first time.Time
	last  time.Time
	n     int
}

type px struct {
	bid, ask float64
	ts       time.Time
	set      bool
}

// Scanner tracks the latest prices and rates and evaluates covered interest parity on demand.
type Scanner struct {
	cfg     Config
	now     time.Time
	pairs   []market.Pair
	pairIdx map[string]int
	base    []market.Currency
	quote   []market.Currency
	spot    []px
	fwd     [][]px                        // [pair][tenor]
	rates   map[string]map[string]float64 // ccy -> tenor -> percent
	daysKey []int                         // per pair: yyyymmdd the cached day counts belong to
	days    [][]int                       // [pair][tenor]
	norm    [][]normal                    // [pair][tenor]
}

// NewScanner builds a scanner for the given pairs (nil = whole universe).
func NewScanner(cfg Config, pairs []market.Pair) *Scanner {
	if pairs == nil {
		pairs = market.Pairs
	}
	s := &Scanner{cfg: cfg, pairs: pairs, pairIdx: map[string]int{}, rates: map[string]map[string]float64{}}
	nT := len(market.Tenors)
	for i, p := range pairs {
		s.pairIdx[p.Symbol] = i
		b, _ := market.CurrencyByCode(p.Base)
		q, _ := market.CurrencyByCode(p.Quote)
		s.base, s.quote = append(s.base, b), append(s.quote, q)
		s.fwd = append(s.fwd, make([]px, nT))
		s.days = append(s.days, make([]int, nT))
		s.norm = append(s.norm, make([]normal, nT))
	}
	s.spot = make([]px, len(pairs))
	s.daysKey = make([]int, len(pairs))
	return s
}

// Pairs returns the scanned pairs in index order.
func (s *Scanner) Pairs() []market.Pair { return s.pairs }

// Now is the timestamp of the latest event applied.
func (s *Scanner) Now() time.Time { return s.now }

// Config returns the current assumptions.
func (s *Scanner) Config() Config { return s.cfg }

// PairIndex returns the index of a pair symbol, or -1.
func (s *Scanner) PairIndex(sym string) int {
	if i, ok := s.pairIdx[sym]; ok {
		return i
	}
	return -1
}

// Apply feeds one event into the scanner. Events must arrive in non-decreasing time order;
// an older event is rejected, which is the guard against look-ahead in back-tests.
func (s *Scanner) Apply(ev Event) error {
	if ev.TS.Before(s.now) {
		return fmt.Errorf("look-ahead guard: event at %s arrived after the clock reached %s",
			ev.TS.Format(time.RFC3339), s.now.Format(time.RFC3339))
	}
	s.now = ev.TS
	switch ev.Kind {
	case EvSpot:
		if i, ok := s.pairIdx[ev.Pair]; ok {
			s.spot[i] = px{bid: ev.Bid, ask: ev.Ask, ts: ev.TS, set: true}
		}
	case EvForward:
		if i, ok := s.pairIdx[ev.Pair]; ok {
			if j := market.TenorIndex(ev.Tenor); j >= 0 {
				s.fwd[i][j] = px{bid: ev.Bid, ask: ev.Ask, ts: ev.TS, set: true}
			}
		}
	case EvRate:
		m := s.rates[ev.Ccy]
		if m == nil {
			m = map[string]float64{}
			s.rates[ev.Ccy] = m
		}
		m[ev.Tenor] = ev.Rate
	}
	return nil
}

// Rate returns the reference rate (percent) for a currency and tenor: the tenor-matched
// market rate if one has been loaded, otherwise the central-bank policy rate.
func (s *Scanner) Rate(ccy, tenor string) (rate float64, src string, ok bool) {
	m := s.rates[ccy]
	if m == nil {
		return 0, "", false
	}
	if v, found := m[tenor]; found {
		return v, tenor, true
	}
	if v, found := m[PolicyTenor]; found {
		return v, PolicyTenor, true
	}
	return 0, "", false
}

// Rates returns a copy of the latest rates table (ccy -> tenor -> percent).
func (s *Scanner) Rates() map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(s.rates))
	for c, m := range s.rates {
		cp := make(map[string]float64, len(m))
		for k, v := range m {
			cp[k] = v
		}
		out[c] = cp
	}
	return out
}

func (s *Scanner) tenorDays(i, j int) int {
	y, m, d := s.now.UTC().Date()
	key := y*10000 + int(m)*100 + d
	if s.daysKey[i] != key {
		for k, t := range market.Tenors {
			s.days[i][k] = market.TenorDays(s.now, s.pairs[i].SpotLag, t)
		}
		s.daysKey[i] = key
	}
	return s.days[i][j]
}

// Observe folds one reading of the gap to parity into the pair and tenor's "normal" level.
// The tracker calls it after it has acted on that reading, so a reading never influences
// the level it is itself compared with. The average forgets with a half-life measured in
// market time: a weekend or a quiet spell does not wipe the memory.
func (s *Scanner) Observe(i, j int, grossSellBps, grossBuyBps float64) {
	n := &s.norm[i][j]
	if n.n == 0 {
		n.sell, n.buy, n.first = grossSellBps, grossBuyBps, s.now
	} else if hl := s.cfg.NormalHalfLifeMin; hl > 0 {
		dt := math.Min(s.now.Sub(n.last).Minutes(), 5)
		if dt > 0 {
			w := 1 - math.Exp(-math.Ln2*dt/hl)
			n.sell += w * (grossSellBps - n.sell)
			n.buy += w * (grossBuyBps - n.buy)
		}
	}
	n.last = s.now
	n.n++
}

// Learn applies a batch and folds every new forward quote in it into the "normal" levels.
// It is what a scanner does when it only watches the market to learn the usual gaps, without
// raising flags (the live hub keeps one such scanner running all the time).
func (s *Scanner) Learn(batch []Event) error {
	for _, ev := range batch {
		if err := s.Apply(ev); err != nil {
			return err
		}
	}
	for _, ev := range batch {
		if ev.Kind != EvForward {
			continue
		}
		i, j := s.PairIndex(ev.Pair), market.TenorIndex(ev.Tenor)
		if i < 0 || j < 0 {
			continue
		}
		if q := s.Evaluate(i, j); q.OK {
			s.Observe(i, j, q.GrossSellBps, q.GrossBuyBps)
		}
	}
	return nil
}

// CopyNormalFrom takes over another scanner's learned "normal" levels. Both scanners must
// cover the same pairs. The levels are measured before funding costs, so they are valid
// under any funding spread.
func (s *Scanner) CopyNormalFrom(o *Scanner) {
	for i := range s.norm {
		if i < len(o.norm) {
			copy(s.norm[i], o.norm[i])
		}
	}
}

// Evaluate analyses pair i, tenor j using only the state accumulated so far.
func (s *Scanner) Evaluate(i, j int) Quote {
	p := s.pairs[i]
	q := Quote{TS: s.now, Pair: p.Symbol, Tenor: market.Tenors[j].Code}
	sp, fw := s.spot[i], s.fwd[i][j]
	if !sp.set || !fw.set {
		q.Reason = "no quote yet"
		return q
	}
	maxAge := time.Duration(s.cfg.MaxStaleSec) * time.Second
	if maxAge > 0 && (s.now.Sub(sp.ts) > maxAge || s.now.Sub(fw.ts) > maxAge) {
		q.Reason = "stale quote"
		return q
	}
	rb, srcB, okB := s.Rate(p.Base, q.Tenor)
	rq, srcQ, okQ := s.Rate(p.Quote, q.Tenor)
	if !okB || !okQ {
		q.Reason = "no interest rate"
		return q
	}

	days := s.tenorDays(i, j)
	// Every leg on the side a taker deals on. Forward points are quoted over spot, so the
	// outright bid is spot bid + bid points and the outright offer is spot offer + offer points.
	in := cip.Inputs{
		SpotBid: sp.bid, SpotAsk: sp.ask,
		FwdBid: sp.bid + fw.bid/p.PipFactor, FwdAsk: sp.ask + fw.ask/p.PipFactor,
		Days:     days,
		RateBase: rb / 100, RateQuote: rq / 100,
		BasisBase: s.base[i].DayBasis, BasisQuote: s.quote[i].DayBasis,
		BorrowBase: s.cfg.BorrowBps(p.Base) / 1e4, BorrowQuote: s.cfg.BorrowBps(p.Quote) / 1e4,
	}
	r := cip.Analyze(in)

	q.OK = true
	q.Days = in.Days
	q.SpotBid, q.SpotAsk = sp.bid, sp.ask
	q.PtsBid, q.PtsAsk = fw.bid, fw.ask
	q.FwdBid, q.FwdAsk = in.FwdBid, in.FwdAsk
	q.RateBase, q.RateQuote, q.RateSrcBase, q.RateSrcQuote = rb, rq, srcB, srcQ
	q.BandLow, q.BandHigh = r.BandLow, r.BandHigh
	q.GrossSellBps = r.GrossBorrowQuote / r.TauQuote * 1e4
	q.GrossBuyBps = r.GrossBorrowBase / r.TauBase * 1e4
	// Market forward against the formula, as a taker and before funding costs: positive when
	// the forward bid is above the formula (overpriced, we sell it), negative when the forward
	// offer is below it (underpriced, we buy it), zero when the formula sits inside the spread.
	switch {
	case q.GrossSellBps > 0:
		q.DeviationBps = q.GrossSellBps
	case q.GrossBuyBps > 0:
		q.DeviationBps = -q.GrossBuyBps
	}

	best, edgeBps, edgePeriod := r.Best, r.EdgeAnnualBps, r.EdgePeriod
	nm := s.norm[i][j]
	q.NormalReady = nm.n >= 30 && s.now.Sub(nm.first).Minutes() >= s.cfg.NormalWarmupMin
	q.Reference = "parity"
	if s.cfg.Reference == "normal" {
		// Measure each trade against what it usually shows instead of against zero: a forward
		// that always looks 40bp rich is only interesting when it is richer than that.
		q.Reference = "normal"
		adjSell := r.EdgeBorrowQuote/r.TauQuote*1e4 - nm.sell
		adjBuy := r.EdgeBorrowBase/r.TauBase*1e4 - nm.buy
		best, edgeBps, edgePeriod = cip.BorrowQuote, adjSell, adjSell/1e4*r.TauQuote
		if adjBuy > adjSell {
			best, edgeBps, edgePeriod = cip.BorrowBase, adjBuy, adjBuy/1e4*r.TauBase
		}
	}
	q.Direction = string(best)
	if best == cip.BorrowBase { // sell base spot at the bid, buy it back at the forward offer
		q.BorrowCcy, q.NormalBps = p.Base, nm.buy
		q.MktFwd, q.FairFwd = in.FwdAsk, r.FairOnBid
		q.FairPts = (r.FairOnBid - sp.bid) * p.PipFactor
		q.GrossPips = (r.FairOnBid - in.FwdAsk) * p.PipFactor
	} else { // buy base spot at the offer, sell it at the forward bid
		q.BorrowCcy, q.NormalBps = p.Quote, nm.sell
		q.MktFwd, q.FairFwd = in.FwdBid, r.FairOnAsk
		q.FairPts = (r.FairOnAsk - sp.ask) * p.PipFactor
		q.GrossPips = (in.FwdBid - r.FairOnAsk) * p.PipFactor
	}
	q.BorrowBps = s.cfg.BorrowBps(q.BorrowCcy)
	q.EdgeBps = edgeBps
	q.EdgePeriodBps = edgePeriod * 1e4
	q.EdgePips = edgePeriod * sp.ask * p.PipFactor // the net edge as pips of the forward price
	q.Signal = edgeBps >= s.cfg.MinEdgeBps && edgePeriod > 0 && (q.Reference == "parity" || q.NormalReady)
	if edgePeriod > 0 {
		q.ProfitUSD = edgePeriod * s.cfg.NotionalUSD
	}
	return q
}

// Snapshot evaluates every pair and tenor.
func (s *Scanner) Snapshot() []Quote {
	out := make([]Quote, 0, len(s.pairs)*len(market.Tenors))
	for i := range s.pairs {
		for j := range market.Tenors {
			out = append(out, s.Evaluate(i, j))
		}
	}
	return out
}

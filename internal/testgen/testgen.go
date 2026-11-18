// Package testgen builds small made-up market data sets FOR THE AUTOMATED TESTS ONLY.
//
// The tests need data in which the right answer is known in advance, so this package writes
// forward points that sit at parity plus a little noise and plants gaps of known size at known
// times. Nothing here is real market data, nothing in the program or the dashboard uses it,
// and it only ever writes to the temporary folders the tests create.
package testgen

import (
	"encoding/csv"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/market"
)

// Params configures the generator.
type Params struct {
	Dir           string
	From, To      time.Time
	Seed          int64
	EventsPerWeek float64 // expected planted gaps per pair and tenor per trading week
}

// Planted is one gap written into the data on purpose.
type Planted struct {
	Pair, Tenor string
	Start, End  time.Time
	PeakBps     float64 // signed, annualised; positive = forward rich
	CostBps     float64 // half bid/offer of the points, annualised
	SpotCostBps float64 // spot bid/offer, annualised over the tenor
}

// usdValue is a rough dollar value of one unit of each currency, to start the random walks.
var usdValue = map[string]float64{
	"USD": 1, "EUR": 1.12, "GBP": 1.30, "AUD": 0.65, "NZD": 0.59, "JPY": 1 / 150.0, "CHF": 1 / 0.83,
	"CAD": 1 / 1.38, "SEK": 1 / 9.8, "NOK": 1 / 10.4, "DKK": 1 / 6.65, "PLN": 1 / 3.8, "CZK": 1 / 22.0,
	"HUF": 1 / 350.0, "MXN": 1 / 18.5, "ZAR": 1 / 17.8, "TRY": 1 / 43.0, "HKD": 1 / 7.8, "SAR": 1 / 3.75,
	"AED": 1 / 3.6725, "QAR": 1 / 3.64, "KWD": 1 / 0.306, "BHD": 1 / 0.377, "OMR": 1 / 0.385,
}

// character gives each kind of pair its noise, bid/offer width and gap size (annualised bp),
// plus a spot half-spread in pips.
func character(p market.Pair) (noiseBps, halfSpreadBps, scale, spotHalfPips float64) {
	major := map[string]bool{"EUR": true, "GBP": true, "JPY": true, "CHF": true, "CAD": true, "AUD": true, "NZD": true, "USD": true}
	switch {
	case p.Group == "GCC" || (p.Base == "USD" && p.Quote == "HKD"):
		return 3.0, 2.5, 1.2, 1
	case !major[p.Base] || !major[p.Quote]:
		return 5.0, 4.0, 1.6, 8
	case p.Base != "USD" && p.Quote != "USD":
		return 2.5, 1.5, 1.1, 0.6
	}
	return 1.5, 1.0, 1.0, 0.3
}

func marketOpen(t time.Time) bool { // FX trades from Sunday 22:00 UTC to Friday 22:00 UTC
	switch t.Weekday() {
	case time.Saturday:
		return false
	case time.Friday:
		return t.Hour() < 22
	case time.Sunday:
		return t.Hour() >= 22
	}
	return true
}

type gap struct {
	start, riseEnd, holdEnd, end int
	peak, halfLife               float64
}

func (g gap) at(i int) float64 {
	switch {
	case i < g.start || i > g.end:
		return 0
	case i < g.riseEnd:
		return g.peak * float64(i-g.start+1) / float64(g.riseEnd-g.start+1)
	case i <= g.holdEnd:
		return g.peak
	}
	return g.peak * math.Exp(-math.Ln2*float64(i-g.holdEnd)/g.halfLife)
}

func poisson(rng *rand.Rand, lambda float64) int {
	l, k, p := math.Exp(-lambda), 0, 1.0
	for {
		p *= rng.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

func seedFor(seed int64, name string) int64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return seed ^ int64(h.Sum64())
}

// Generate writes spot files, forward-point files and planted.csv into p.Dir, which must
// already hold a rates.csv with a policy rate for every currency.
func Generate(p Params) ([]Planted, error) {
	rows, err := data.LoadRates(p.Dir)
	if err != nil {
		return nil, err
	}
	type series struct {
		dates []time.Time
		vals  []float64
	}
	policy := map[string]*series{}
	for _, r := range rows {
		if r.Tenor != engine.PolicyTenor {
			continue
		}
		s := policy[r.Ccy]
		if s == nil {
			s = &series{}
			policy[r.Ccy] = s
		}
		s.dates, s.vals = append(s.dates, r.Date), append(s.vals, r.Rate)
	}
	rateAt := func(ccy string, t time.Time) (float64, bool) {
		s := policy[ccy]
		if s == nil {
			return 0, false
		}
		i := sort.Search(len(s.dates), func(i int) bool { return s.dates[i].After(t) })
		if i == 0 {
			return 0, false
		}
		return s.vals[i-1], true
	}

	var grid []time.Time
	for t := p.From.UTC().Truncate(time.Minute); t.Before(p.To); t = t.Add(time.Minute) {
		if marketOpen(t) {
			grid = append(grid, t)
		}
	}
	if len(grid) == 0 {
		return nil, fmt.Errorf("no trading minutes between %s and %s", p.From, p.To)
	}

	// One random walk per currency, so every cross is consistent with its legs.
	walk := map[string][]float64{}
	for _, c := range market.Currencies {
		v := make([]float64, len(grid))
		rng := rand.New(rand.NewSource(seedFor(p.Seed, "ccy:"+c.Code)))
		sd := 0.09 / math.Sqrt(260*1440)
		x := usdValue[c.Code]
		for i := range v {
			if c.Code != "USD" {
				x *= math.Exp(sd * rng.NormFloat64())
			}
			v[i] = x
		}
		walk[c.Code] = v
	}

	var planted []Planted
	for _, pr := range market.Pairs {
		if policy[pr.Base] == nil || policy[pr.Quote] == nil {
			continue
		}
		base, _ := market.CurrencyByCode(pr.Base)
		quote, _ := market.CurrencyByCode(pr.Quote)
		noiseSD, hsBase, scale, spotHalfPips := character(pr)
		hs := spotHalfPips / pr.PipFactor
		n := len(grid)

		sw, err := data.Create(filepath.Join(p.Dir, "spot", pr.Symbol+".csv.gz"))
		if err != nil {
			return nil, err
		}
		sw.WriteString("ts,bid,ask\n")
		mids := make([]float64, n)
		for i, t := range grid {
			mids[i] = walk[pr.Base][i] / walk[pr.Quote][i]
			fmt.Fprintf(sw, "%s,%s,%s\n", data.FormatTS(t), strconv.FormatFloat(mids[i]-hs, 'f', 6, 64), strconv.FormatFloat(mids[i]+hs, 'f', 6, 64))
		}
		if err := sw.Close(); err != nil {
			return nil, err
		}

		rng := rand.New(rand.NewSource(seedFor(p.Seed, pr.Symbol)))
		kappa := math.Ln2 / 120.0 // noise reverts with a two-hour half-life
		step := noiseSD * math.Sqrt(2*kappa)
		nT := len(market.Tenors)
		noise := make([]float64, nT)
		plans := make([][]gap, nT)
		for j := range market.Tenors {
			noise[j] = noiseSD * rng.NormFloat64()
			for k := poisson(rng, p.EventsPerWeek*float64(n)/7200); k > 0; k-- {
				g := gap{start: rng.Intn(n)}
				g.riseEnd = g.start + rng.Intn(3)
				g.holdEnd = g.riseEnd + 2 + rng.Intn(40)
				g.halfLife = 3 + 27*rng.Float64()
				g.peak = math.Min(12+rng.ExpFloat64()*35, 180) * scale
				if rng.Intn(2) == 0 {
					g.peak = -g.peak
				}
				g.end = g.holdEnd + int(math.Ceil(g.halfLife*math.Log2(math.Abs(g.peak)/2)))
				plans[j] = append(plans[j], g)
			}
		}

		fw, err := data.Create(filepath.Join(p.Dir, "forwards", pr.Symbol+".csv.gz"))
		if err != nil {
			return nil, err
		}
		fw.WriteString("ts,tenor,bid_pts,ask_pts\n")
		for i, t := range grid {
			rb, ok1 := rateAt(pr.Base, t)
			rq, ok2 := rateAt(pr.Quote, t)
			if !ok1 || !ok2 {
				continue
			}
			stamp := data.FormatTS(t)
			for j, tn := range market.Tenors {
				noise[j] += -kappa*noise[j] + step*rng.NormFloat64()
				dev := noise[j]
				for _, g := range plans[j] {
					dev += g.at(i)
				}
				d := float64(market.TenorDays(t, pr.SpotLag, tn))
				tauB, tauQ := d/base.DayBasis, d/quote.DayBasis
				fwd := mids[i] * (1 + (rq/100+dev/1e4)*tauQ) / (1 + rb/100*tauB)
				pts := (fwd - mids[i]) * pr.PipFactor
				hsPts := mids[i] * hsBase * (1 + 0.25/math.Sqrt(d/360)) / 1e4 * tauQ * pr.PipFactor
				fmt.Fprintf(fw, "%s,%s,%s,%s\n", stamp, tn.Code,
					strconv.FormatFloat(pts-hsPts, 'f', 3, 64), strconv.FormatFloat(pts+hsPts, 'f', 3, 64))
			}
		}
		if err := fw.Close(); err != nil {
			return nil, err
		}

		for j, tn := range market.Tenors {
			d := float64(market.TenorDays(grid[0], pr.SpotLag, tn))
			for _, g := range plans[j] {
				end := g.end
				if end >= n {
					end = n - 1
				}
				planted = append(planted, Planted{Pair: pr.Symbol, Tenor: tn.Code, Start: grid[g.start], End: grid[end],
					PeakBps: g.peak, CostBps: hsBase * (1 + 0.25/math.Sqrt(d/360)),
					SpotCostBps: 2 * hs / mids[g.start] / (d / 360) * 1e4})
			}
		}
	}
	return planted, save(p.Dir, planted)
}

func save(dir string, planted []Planted) error {
	f, err := os.Create(filepath.Join(dir, "planted.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"pair", "tenor", "start", "end", "peak_bps", "cost_bps", "spot_cost_bps"})
	for _, g := range planted {
		w.Write([]string{g.Pair, g.Tenor, data.FormatTS(g.Start), data.FormatTS(g.End),
			strconv.FormatFloat(g.PeakBps, 'f', 2, 64), strconv.FormatFloat(g.CostBps, 'f', 2, 64), strconv.FormatFloat(g.SpotCostBps, 'f', 2, 64)})
	}
	w.Flush()
	return w.Error()
}

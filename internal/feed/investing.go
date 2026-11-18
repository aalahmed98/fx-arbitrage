// Package feed brings live market data into the scanner.
//
// The Investing source reads the public forward-rates page that investing.com publishes for
// each pair (https://www.investing.com/currencies/eur-usd-forward-rates). Each page carries
// the spot bid/ask and a table of forward points (bid/ask) per tenor, each row stamped with
// the time of its last quote. There is no history on those pages, so the recorder in this
// package also appends everything it sees to disk, building a real history from the day it
// is switched on.
//
// The reader identifies itself honestly, requests each page about once a minute and does
// nothing to get around access controls. Investing.com's terms restrict automated access and
// commercial use, and the quotes are indicative: clear this with compliance before relying
// on it at the desk.
package feed

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fxarb/internal/market"
)

// Point is one tenor's forward points as quoted by the source.
type Point struct {
	Tenor     string
	Bid, Ask  float64 // forward points in pips
	QuoteTime time.Time
}

// Snapshot is one reading of a pair's page.
type Snapshot struct {
	Pair             string
	At               time.Time // when we observed it; the only timestamp the scanner is given
	SpotBid, SpotAsk float64
	Points           []Point
}

// Investing reads investing.com forward-rates pages.
type Investing struct {
	Client    *http.Client
	BaseURL   string
	UserAgent string
}

// NewInvesting returns a reader with sensible defaults.
func NewInvesting() *Investing {
	return &Investing{
		Client:    &http.Client{Timeout: 20 * time.Second},
		BaseURL:   "https://www.investing.com",
		UserAgent: "fxarb/0.1 (internal FX parity scanner)",
	}
}

// URL is the page read for a pair.
func (f *Investing) URL(p market.Pair) string {
	return fmt.Sprintf("%s/currencies/%s-%s-forward-rates", f.BaseURL, strings.ToLower(p.Base), strings.ToLower(p.Quote))
}

// Fetch reads one pair's page.
func (f *Investing) Fetch(ctx context.Context, p market.Pair) (*Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", f.URL(p), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", p.Symbol, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	snap, err := ParseInvesting(string(body), p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Symbol, err)
	}
	snap.At = time.Now().UTC().Truncate(time.Second)
	return snap, nil
}

var (
	reName = regexp.MustCompile(`id="pair_name_\d+"[^>]*>([^<]+)<`)
	reBid  = regexp.MustCompile(`id="bid_\d+"[^>]*>([^<]*)<`)
	reAsk  = regexp.MustCompile(`id="ask_\d+"[^>]*>([^<]*)<`)
	reTime = regexp.MustCompile(`id="time_\d+"[^>]*data-value="(\d+)"`)
	reSpot = regexp.MustCompile(`pid-(\d+)-bid"[^>]*>([^<]+)<`)
)

// tenorAlias maps the source's tenor labels onto ours.
var tenorAlias = map[string]string{"SW": "1W", "1W": "1W", "12M": "1Y"}

// pointScale converts the source's forward points into the scanner's pips for pairs the
// source quotes differently. Investing.com shows USD/MXN points in pesos (0.0394) rather than
// pips (394). Run `fxarb probe` after adding a pair: market and parity points should be the
// same order of magnitude.
var pointScale = map[string]float64{"USDMXN": 10000}

func number(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(html.UnescapeString(s)), ",", "")
	return strconv.ParseFloat(s, 64)
}

// ParseInvesting extracts the spot quote and forward-points table from a forward-rates page.
func ParseInvesting(page string, p market.Pair) (*Snapshot, error) {
	snap := &Snapshot{Pair: p.Symbol}

	if m := reSpot.FindStringSubmatch(page); m != nil {
		bid, e1 := number(m[2])
		reA := regexp.MustCompile(`pid-` + m[1] + `-ask"[^>]*>([^<]+)<`)
		if a := reA.FindStringSubmatch(page); a != nil && e1 == nil {
			if ask, e2 := number(a[1]); e2 == nil && bid > 0 && ask >= bid {
				snap.SpotBid, snap.SpotAsk = bid, ask
			}
		}
	}
	if snap.SpotBid == 0 {
		return nil, fmt.Errorf("no spot bid/ask on the page")
	}

	i := strings.Index(page, `id="curr_table"`)
	if i < 0 {
		return nil, fmt.Errorf("no forward-rates table on the page")
	}
	table := page[i:]
	if j := strings.Index(table, "</table>"); j > 0 {
		table = table[:j]
	}
	scale := 1.0
	if s, ok := pointScale[p.Symbol]; ok {
		scale = s
	}
	seen := map[string]bool{}
	for _, row := range strings.Split(table, `<tr id="pair_`)[1:] {
		name := reName.FindStringSubmatch(row)
		bid, ask, ts := reBid.FindStringSubmatch(row), reAsk.FindStringSubmatch(row), reTime.FindStringSubmatch(row)
		if name == nil || bid == nil || ask == nil || ts == nil {
			continue
		}
		// "EURUSD&nbsp;SW&nbsp;FWD" -> ["EURUSD", "SW", "FWD"]
		parts := strings.Fields(strings.ReplaceAll(html.UnescapeString(name[1]), " ", " "))
		if len(parts) < 3 || !strings.EqualFold(parts[0], p.Symbol) {
			continue
		}
		tenor := strings.ToUpper(parts[1])
		if a, ok := tenorAlias[tenor]; ok {
			tenor = a
		}
		if market.TenorIndex(tenor) < 0 || seen[tenor] {
			continue // ON, TN, SN, odd months, anything past a year, and repeated rows are skipped
		}
		b, e1 := number(bid[1])
		a, e2 := number(ask[1])
		sec, e3 := strconv.ParseInt(ts[1], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		if b > a { // some rows arrive crossed; a crossed quote is not tradable
			continue
		}
		seen[tenor] = true
		snap.Points = append(snap.Points, Point{Tenor: tenor, Bid: b * scale, Ask: a * scale, QuoteTime: time.Unix(sec, 0).UTC()})
	}
	if len(snap.Points) == 0 {
		return nil, fmt.Errorf("forward-rates table had no usable rows")
	}
	return snap, nil
}

// Package fetch downloads central-bank policy rates from the BIS.
package fetch

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/market"
)

var client = &http.Client{Timeout: 60 * time.Second}

// PolicyRates downloads the daily central-bank policy-rate series for every tracked currency
// from the BIS statistics API (public, no key) and returns one row per rate change.
// BIS dates are the days a rate was in effect, so a row never pre-dates its announcement.
func PolicyRates(from time.Time) ([]data.RateRow, error) {
	areaToCcy := map[string]string{}
	var areas []string
	for _, c := range market.Currencies {
		if c.BISArea == "" {
			continue // the BIS has no series for this currency; its rate is entered by hand
		}
		areaToCcy[c.BISArea] = c.Code
		areas = append(areas, c.BISArea)
	}
	u := fmt.Sprintf("https://stats.bis.org/api/v2/data/dataflow/BIS/WS_CBPOL/1.0/D.%s?startPeriod=%s&format=csv",
		strings.Join(areas, "+"), from.Format("2006-01-02"))
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("BIS returned %s: %s", resp.Status, b)
	}
	rd := csv.NewReader(resp.Body)
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	head, err := rd.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	iA, okA := col["REF_AREA"]
	iT, okT := col["TIME_PERIOD"]
	iV, okV := col["OBS_VALUE"]
	if !okA || !okT || !okV {
		return nil, fmt.Errorf("unexpected BIS columns: %v", head)
	}
	type obs struct {
		d time.Time
		v float64
	}
	series := map[string][]obs{}
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) <= iV {
			continue
		}
		ccy := areaToCcy[rec[iA]]
		v, errV := strconv.ParseFloat(strings.TrimSpace(rec[iV]), 64)
		d, errD := time.Parse("2006-01-02", strings.TrimSpace(rec[iT]))
		if ccy == "" || errV != nil || errD != nil || v != v { // v != v: BIS writes NaN on non-business days
			continue
		}
		series[ccy] = append(series[ccy], obs{d, v})
	}
	var out []data.RateRow
	for ccy, s := range series {
		sort.Slice(s, func(a, b int) bool { return s[a].d.Before(s[b].d) })
		for i, o := range s {
			if i == 0 || o.v != s[i-1].v {
				out = append(out, data.RateRow{Date: o.d, Ccy: ccy, Tenor: engine.PolicyTenor, Rate: o.v})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].Date.Equal(out[b].Date) {
			return out[a].Date.Before(out[b].Date)
		}
		return out[a].Ccy < out[b].Ccy
	})
	return out, nil
}

// MergeRates replaces the POLICY rows of an existing table with fresh ones from the BIS. It
// keeps any tenor-specific market rates the user has loaded, and the policy rates of
// currencies the BIS does not cover (those are entered by hand).
func MergeRates(existing, policy []data.RateRow) []data.RateRow {
	out := append([]data.RateRow(nil), policy...)
	fromBIS := map[string]bool{}
	for _, r := range policy {
		fromBIS[r.Ccy] = true
	}
	for _, r := range existing {
		if r.Tenor != engine.PolicyTenor || !fromBIS[r.Ccy] {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Date.Before(out[b].Date) })
	return out
}

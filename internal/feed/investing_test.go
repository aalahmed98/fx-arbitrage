package feed

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"fxarb/internal/engine"
	"fxarb/internal/market"
)

func row(id int, name, bid, ask string, unix int64) string {
	return fmt.Sprintf(`<tr id="pair_%d"><td class="icon"></td><td class="bold left" id="pair_name_%d" nowrap="nowrap">%s</td>`+
		`<td id="bid_%d">%s</td><td id="ask_%d">%s</td><td id="high_%d">1</td><td id="low_%d">1</td>`+
		`<td class="bold" id="time_%d" data-value="%d">10:42:08</td></tr>`, id, id, name, id, bid, id, ask, id, id, id, unix)
}

func page(spotBid, spotAsk string, rows ...string) string {
	return `<span class="inlineblock pid-1-bid">` + spotBid + `</span> / <span class="inlineblock pid-1-ask">` + spotAsk + `</span>` +
		`<table id="curr_table"><tbody>` + strings.Join(rows, "") + `</tbody></table><table><tr id="pair_9"></tr></table>`
}

func TestParseInvesting(t *testing.T) {
	const now = 1794912128
	p, _ := market.PairBySymbol("EURUSD")
	snap, err := ParseInvesting(page("1.0989", "1.0991",
		row(1, "EURUSD&nbsp;ON&nbsp;FWD", "0.39", "0.49", now),         // overnight: not scanned
		row(2, "EURUSD&nbsp;SW&nbsp;FWD", "3.4500", "3.5500", now),     // SW means one week
		row(3, "EURUSD&nbsp;1W&nbsp;FWD", "9", "9", now),               // repeat of the same tenor: ignored
		row(4, "EURUSD&nbsp;1M&nbsp;FWD", "14.7600", "15.0200", now-5), //
		row(5, "EURUSD&nbsp;3M&nbsp;FWD", "44.90", "44.40", now),       // crossed: dropped
		row(6, "EURUSD&nbsp;1Y&nbsp;FWD", "1,180.78", "1,185.86", now), // thousands separator
		row(7, "EURUSD&nbsp;2Y&nbsp;FWD", "356", "368", now),           // beyond a year: not scanned
	), p)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SpotBid != 1.0989 || snap.SpotAsk != 1.0991 {
		t.Fatalf("spot = %v / %v", snap.SpotBid, snap.SpotAsk)
	}
	want := []Point{{"1W", 3.45, 3.55, time.Unix(now, 0).UTC()}, {"1M", 14.76, 15.02, time.Unix(now-5, 0).UTC()},
		{"1Y", 1180.78, 1185.86, time.Unix(now, 0).UTC()}}
	if len(snap.Points) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(snap.Points), len(want), snap.Points)
	}
	for i, w := range want {
		if snap.Points[i] != w {
			t.Errorf("point %d = %+v, want %+v", i, snap.Points[i], w)
		}
	}
}

func TestParseInvestingScalesPesoPoints(t *testing.T) {
	p, _ := market.PairBySymbol("USDMXN")
	snap, err := ParseInvesting(page("18.469", "18.487", row(1, "USDMXN&nbsp;1M&nbsp;FWD", "0.0394", "0.0424", 1794912128)), p)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Points[0]; got.Bid < 393.9 || got.Bid > 394.1 || got.Ask < 423.9 || got.Ask > 424.1 {
		t.Fatalf("USDMXN points not converted to pips: %+v", got)
	}
}

func TestParseInvestingRejectsOtherPages(t *testing.T) {
	p, _ := market.PairBySymbol("EURUSD")
	if _, err := ParseInvesting("<html>Just a moment...</html>", p); err == nil {
		t.Fatal("a page without quotes must be an error, not an empty snapshot")
	}
}

func TestEventsUseObservationTimeAndDropStaleRows(t *testing.T) {
	at := time.Date(2026, 11, 17, 11, 15, 0, 0, time.UTC)
	snap := &Snapshot{Pair: "EURUSD", At: at, SpotBid: 1.1, SpotAsk: 1.1002, Points: []Point{
		{"1M", 14, 15, at.Add(-30 * time.Minute)}, // the feed's normal half-hour delay
		{"3M", 44, 45, at.Add(-31 * time.Minute)},
		{"1Y", 180, 185, at.Add(-3 * time.Hour)}, // not re-quoted for hours
	}}
	evs, lag := Events(snap)
	if lag != 30*time.Minute {
		t.Fatalf("lag = %s, want 30m", lag)
	}
	if len(evs) != 3 {
		t.Fatalf("want spot + 2 fresh tenors, got %d events", len(evs))
	}
	for _, ev := range evs {
		if !ev.TS.Equal(at) {
			t.Fatalf("event stamped %s, want the observation time %s", ev.TS, at)
		}
		if ev.Kind == engine.EvForward && ev.Tenor == "1Y" {
			t.Fatal("a stale tenor leaked through")
		}
	}
	// Whole page stale (weekend): spot only.
	snap.At = at.Add(48 * time.Hour)
	if evs, _ := Events(snap); len(evs) != 1 {
		t.Fatalf("closed market should yield only the spot event, got %d", len(evs))
	}
}

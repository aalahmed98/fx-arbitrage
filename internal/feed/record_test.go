package feed

import (
	"path/filepath"
	"testing"
	"time"

	"fxarb/internal/data"
	"fxarb/internal/engine"
)

// A new month must start a new file, archive the old one, and the back-test reader must
// still see one continuous history in time order.
func TestMonthlyArchiveKeepsOneHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forwards", "EURUSD.csv")
	header := "ts,tenor,bid_pts,ask_pts\n"
	oct := time.Date(2026, 10, 30, 21, 58, 0, 0, time.UTC)
	nov := time.Date(2026, 11, 2, 0, 5, 0, 0, time.UTC)

	if err := appendRow(path, header, "2026-10-30T21:58:00Z,1M,14.1,14.3\n", oct); err != nil {
		t.Fatal(err)
	}
	if err := appendRow(path, header, "2026-10-30T21:59:00Z,1M,14.2,14.4\n", oct.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := appendRow(path, header, "2026-11-02T00:05:00Z,1M,15.0,15.2\n", nov); err != nil {
		t.Fatal(err)
	}

	parts := data.Parts(dir, "forwards", "EURUSD")
	if len(parts) != 2 || filepath.Base(parts[0]) != "EURUSD.2026-10.csv.gz" || filepath.Base(parts[1]) != "EURUSD.csv" {
		t.Fatalf("parts = %v, want the October archive then the current file", parts)
	}

	if err := data.SaveRates(dir, []data.RateRow{{Date: oct.AddDate(0, -1, 0), Ccy: "USD", Tenor: engine.PolicyTenor, Rate: 3.875}}); err != nil {
		t.Fatal(err)
	}
	st, err := data.OpenStream(dir, []string{"EURUSD"}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var got []float64
	var prev time.Time
	for {
		b, err := st.NextBatch()
		if err != nil {
			t.Fatal(err)
		}
		if b == nil {
			break
		}
		for _, ev := range b {
			if ev.Kind != engine.EvForward {
				continue
			}
			if ev.TS.Before(prev) {
				t.Fatalf("history went backwards at %s", ev.TS)
			}
			prev = ev.TS
			got = append(got, ev.Bid)
		}
	}
	if len(got) != 3 || got[0] != 14.1 || got[1] != 14.2 || got[2] != 15.0 {
		t.Fatalf("read %v, want the two October rows then the November row", got)
	}
}

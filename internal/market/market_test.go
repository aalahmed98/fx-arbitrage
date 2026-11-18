package market

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestSpotDate(t *testing.T) {
	cases := []struct {
		trade string
		lag   int
		want  string
	}{
		{"2026-11-09", 2, "2026-11-11"}, // Mon -> Wed
		{"2026-11-12", 2, "2026-11-16"}, // Thu -> Mon (skips the weekend)
		{"2026-11-13", 1, "2026-11-16"}, // Fri T+1 -> Mon
		{"2026-11-15", 2, "2026-11-18"}, // Sunday evening trades as Monday
	}
	for _, c := range cases {
		if got := SpotDate(d(c.trade), c.lag); !got.Equal(d(c.want)) {
			t.Errorf("SpotDate(%s, %d) = %s, want %s", c.trade, c.lag, got.Format("2006-01-02"), c.want)
		}
	}
}

func TestMaturityDate(t *testing.T) {
	cases := []struct{ spot, tenor, want string }{
		{"2026-11-11", "1W", "2026-11-18"},
		{"2026-11-11", "1M", "2026-12-11"},
		{"2026-11-11", "3M", "2027-02-11"},
		{"2026-01-30", "1M", "2026-02-27"}, // clamps to month end; the 28th is a Saturday, so roll back
		{"2026-10-30", "1M", "2026-11-30"},
		{"2026-11-13", "1Y", "2027-11-15"}, // Sat 13 Nov 2027 rolls forward to Monday
	}
	for _, c := range cases {
		got := MaturityDate(d(c.spot), Tenors[TenorIndex(c.tenor)])
		if !got.Equal(d(c.want)) {
			t.Errorf("MaturityDate(%s, %s) = %s, want %s", c.spot, c.tenor, got.Format("2006-01-02"), c.want)
		}
	}
}

func TestTenorDays(t *testing.T) {
	// Trade Mon 9 Nov 2026, spot Wed 11 Nov, 3M maturity Thu 11 Feb 2027 = 92 days.
	if got := TenorDays(d("2026-11-09"), 2, Tenors[TenorIndex("3M")]); got != 92 {
		t.Fatalf("3M days = %d, want 92", got)
	}
}

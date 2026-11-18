package cip

import (
	"math"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// quote builds EUR/USD-style inputs: 90 days, EUR 3%, USD 5%, both ACT/360, with the given
// spot and forward bid/offer.
func quote(spotBid, spotAsk, fwdBid, fwdAsk float64) Inputs {
	return Inputs{SpotBid: spotBid, SpotAsk: spotAsk, FwdBid: fwdBid, FwdAsk: fwdAsk,
		Days: 90, RateBase: 0.03, RateQuote: 0.05, BasisBase: 360, BasisQuote: 360}
}

const carry = 1.0125 / 1.0075 // (1 + 5% x 0.25) / (1 + 3% x 0.25)

func TestFormulaForwardOnEachSide(t *testing.T) {
	r := Analyze(quote(1.0999, 1.1001, 1.1050, 1.1060))
	if !near(r.FairOnAsk, 1.1001*carry, 1e-12) || !near(r.FairOnBid, 1.0999*carry, 1e-12) {
		t.Fatalf("formula forwards = %.8f / %.8f", r.FairOnBid, r.FairOnAsk)
	}
}

func TestNoSpreadNoGapMeansZero(t *testing.T) {
	f := 1.10 * carry
	r := Analyze(quote(1.10, 1.10, f, f))
	if !near(r.EdgeBorrowQuote, 0, 1e-12) || !near(r.EdgeBorrowBase, 0, 1e-12) {
		t.Fatalf("edges at parity with no spread should be zero, got %g / %g", r.EdgeBorrowQuote, r.EdgeBorrowBase)
	}
}

// A taker pays the spread on both legs: with the forward exactly at parity on mid, both
// directions must lose, by the spot spread plus the forward spread.
func TestTakerPaysBothSpreads(t *testing.T) {
	f := 1.10 * carry
	r := Analyze(quote(1.0999, 1.1001, f-0.0002, f+0.0002))
	if r.EdgeBorrowQuote >= 0 || r.EdgeBorrowBase >= 0 {
		t.Fatalf("a market at parity must cost a taker money both ways, got %g / %g", r.EdgeBorrowQuote, r.EdgeBorrowBase)
	}
	// Selling forward: we get the bid (2 pips under mid) after buying spot 1 pip over mid.
	wantLoss := -(0.0002 + 0.0001*carry) / 1.1001 * 1.0075
	if !near(r.EdgeBorrowQuote, wantLoss, 1e-7) {
		t.Fatalf("borrow-quote edge = %.8f, want %.8f", r.EdgeBorrowQuote, wantLoss)
	}
}

func TestRichForwardIsSoldAtTheBid(t *testing.T) {
	// Forward bid 50bp a year above the formula forward built on the spot OFFER.
	fwdBid := 1.1001 * carry * (1 + 0.0050*0.25)
	r := Analyze(quote(1.0999, 1.1001, fwdBid, fwdBid+0.0004))
	if r.Best != BorrowQuote || !near(r.EdgeAnnualBps, 50, 1.0) {
		t.Fatalf("want borrow-quote at about 50bp, got %s %.2fbp", r.Best, r.EdgeAnnualBps)
	}
	if r.EdgeBorrowBase >= 0 {
		t.Fatalf("the opposite direction must lose, got %g", r.EdgeBorrowBase)
	}
}

func TestCheapForwardIsBoughtAtTheOffer(t *testing.T) {
	// Forward offer 50bp a year below the formula forward built on the spot BID.
	fwdAsk := 1.0999 * carry * (1 - 0.0050*0.25)
	r := Analyze(quote(1.0999, 1.1001, fwdAsk-0.0004, fwdAsk))
	if r.Best != BorrowBase || !near(r.EdgeAnnualBps, 50, 1.0) {
		t.Fatalf("want borrow-base at about 50bp, got %s %.2fbp", r.Best, r.EdgeAnnualBps)
	}
}

func TestFundingSpreadEatsTheEdge(t *testing.T) {
	fwdBid := 1.1001 * carry * (1 + 0.0020*0.25) // 20bp rich for a taker
	in := quote(1.0999, 1.1001, fwdBid, fwdBid+0.0004)
	if r := Analyze(in); r.EdgePeriod <= 0 {
		t.Fatalf("expected a positive edge before funding costs, got %g", r.EdgePeriod)
	}
	in.BorrowBase, in.BorrowQuote = 0.0025, 0.0025
	r := Analyze(in)
	if !near(r.EdgeAnnualBps, -5, 1.0) {
		t.Fatalf("net edge = %.2fbp, want about -5bp (20bp gap less 25bp funding)", r.EdgeAnnualBps)
	}
	// The gross figure must not move with the funding spread.
	if !near(r.GrossBorrowQuote/r.TauQuote*1e4, 20, 1.0) {
		t.Fatalf("gross edge = %.2fbp, want about 20bp", r.GrossBorrowQuote/r.TauQuote*1e4)
	}
}

func TestBandMatchesEdges(t *testing.T) {
	in := quote(1.0999, 1.1001, 1.1058, 1.1062)
	in.BorrowBase, in.BorrowQuote = 0.0025, 0.0025
	r := Analyze(in)
	if r.BandLow >= r.BandHigh {
		t.Fatalf("band is inverted: %g >= %g", r.BandLow, r.BandHigh)
	}
	if (in.FwdBid > r.BandHigh) != (r.EdgeBorrowQuote > 0) || (in.FwdAsk < r.BandLow) != (r.EdgeBorrowBase > 0) {
		t.Fatal("the band disagrees with the edges")
	}
	if r.EdgeBorrowQuote > 0 && r.EdgeBorrowBase > 0 {
		t.Fatal("both directions profitable")
	}
}

func TestDayCountBasis(t *testing.T) {
	in := quote(1.30, 1.30, 1.30, 1.30)
	in.BasisBase = 365 // GBP is ACT/365, USD ACT/360
	want := 1.30 * (1 + 0.05*90.0/360) / (1 + 0.03*90.0/365)
	if r := Analyze(in); !near(r.FairOnAsk, want, 1e-12) {
		t.Fatalf("formula forward = %.8f, want %.8f", r.FairOnAsk, want)
	}
}

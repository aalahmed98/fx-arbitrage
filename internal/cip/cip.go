// Package cip implements covered interest parity (CIP) pricing for a market taker.
//
// Every leg is priced on the side a taker deals on; no mid price is used anywhere.
// For a pair BASE/QUOTE (spot is units of quote per one base), simple money-market rates
// r_b and r_q and year fractions tau_b = days/basis_b, tau_q = days/basis_q:
//
//	formula forward from a spot price S:  F*(S) = S * (1 + r_q*tau_q) / (1 + r_b*tau_b)
//
// The two trades, each paying the spread on both the spot and the forward:
//
//	borrow QUOTE, BUY base at the spot offer, deposit base, SELL base at the forward bid
//	borrow BASE, SELL base at the spot bid, deposit quote, BUY base at the forward offer
package cip

// Direction names the profitable side of the trade.
type Direction string

const (
	None        Direction = ""
	BorrowQuote Direction = "BORROW_QUOTE" // borrow quote ccy, buy base spot, sell base forward
	BorrowBase  Direction = "BORROW_BASE"  // borrow base ccy, sell base spot, buy base forward
)

// Inputs are the dealable prices and funding assumptions for one pair and tenor.
// All rates and spreads are decimals (0.045 = 4.5%).
type Inputs struct {
	SpotBid, SpotAsk float64 // we sell base at the bid and buy it at the offer
	FwdBid, FwdAsk   float64 // outright forward: we sell base at the bid and buy it at the offer
	Days             int

	RateBase, RateQuote   float64 // reference rates
	BasisBase, BasisQuote float64 // 360 or 365

	BorrowBase, BorrowQuote float64 // spread added on top of the reference rate when we borrow
}

// Result is the parity analysis for one pair and tenor.
type Result struct {
	TauBase, TauQuote float64

	// The formula forward on each side of spot: FairOnAsk is what the forward bid must beat
	// when we buy spot at the offer; FairOnBid is what the forward offer must be under when
	// we sell spot at the bid. Neither includes funding costs.
	FairOnAsk, FairOnBid float64
	// The same two levels after our own funding costs: the no-arbitrage band.
	BandLow, BandHigh float64

	// Period profit as a fraction of notional, paying the spread on every leg.
	// Gross* ignore the funding spread; Edge* include it.
	GrossBorrowQuote, GrossBorrowBase float64
	EdgeBorrowQuote, EdgeBorrowBase   float64

	Best          Direction // better of the two directions (even when negative)
	EdgePeriod    float64   // Edge of Best
	EdgeAnnualBps float64   // EdgePeriod annualised, in basis points
}

// Fair returns the covered-interest-parity forward for the given spot price and rates.
func Fair(spot, rateBase, rateQuote, tauBase, tauQuote float64) float64 {
	return spot * (1 + rateQuote*tauQuote) / (1 + rateBase*tauBase)
}

// Analyze prices both arbitrage directions for a taker.
func Analyze(in Inputs) Result {
	var r Result
	if in.Days <= 0 || in.SpotBid <= 0 || in.SpotAsk <= 0 {
		return r
	}
	d := float64(in.Days)
	r.TauBase, r.TauQuote = d/in.BasisBase, d/in.BasisQuote

	// Growth factors at the reference rates and at our own funding and deposit rates.
	refQ, refB := 1+in.RateQuote*r.TauQuote, 1+in.RateBase*r.TauBase
	borrowQ := 1 + (in.RateQuote+in.BorrowQuote)*r.TauQuote
	borrowB := 1 + (in.RateBase+in.BorrowBase)*r.TauBase
	lendQ, lendB := refQ, refB // deposits earn the reference rate

	r.FairOnAsk = in.SpotAsk * refQ / refB
	r.FairOnBid = in.SpotBid * refQ / refB
	r.BandHigh = in.SpotAsk * borrowQ / lendB
	r.BandLow = in.SpotBid * lendQ / borrowB

	// Borrow 1 quote -> buy 1/SpotAsk base -> deposit -> sell forward at FwdBid; repay the loan.
	r.GrossBorrowQuote = in.FwdBid/in.SpotAsk*refB - refQ
	r.EdgeBorrowQuote = in.FwdBid/in.SpotAsk*lendB - borrowQ
	// Borrow 1 base -> sell for SpotBid quote -> deposit -> buy the base back at FwdAsk.
	// The profit is in quote ccy per unit of base; a taker turns it back into base at the offer.
	r.GrossBorrowBase = (in.SpotBid*refQ - in.FwdAsk*refB) / in.SpotAsk
	r.EdgeBorrowBase = (in.SpotBid*lendQ - in.FwdAsk*borrowB) / in.SpotAsk

	if r.EdgeBorrowQuote >= r.EdgeBorrowBase {
		r.Best, r.EdgePeriod = BorrowQuote, r.EdgeBorrowQuote
		r.EdgeAnnualBps = r.EdgePeriod / r.TauQuote * 1e4
	} else {
		r.Best, r.EdgePeriod = BorrowBase, r.EdgeBorrowBase
		r.EdgeAnnualBps = r.EdgePeriod / r.TauBase * 1e4
	}
	return r
}

// Package market holds the static reference data the scanner needs:
// the currency universe, quoting conventions, tenors and value-date maths.
package market

import "time"

// Currency describes one currency's money-market conventions.
type Currency struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	DayBasis float64 `json:"dayBasis"` // 360 or 365 (ACT/360 vs ACT/365 money-market convention)
	BISArea  string  `json:"-"`        // BIS reference-area code for the policy rate; "" if the BIS has none
	Bank     string  `json:"bank"`     // rate-setting central bank
	// ManualRate marks a currency whose policy rate the BIS does not publish. Its rate is
	// entered by the user on the Rates tab; until then its pairs are recorded but not priced.
	ManualRate bool `json:"manualRate"`
}

func ccy(code, name string, basis float64, bis, bank string) Currency {
	return Currency{Code: code, Name: name, DayBasis: basis, BISArea: bis, Bank: bank, ManualRate: bis == ""}
}

// Currencies is the tracked universe. Add a row here to track another currency.
var Currencies = []Currency{
	ccy("USD", "US dollar", 360, "US", "Federal Reserve"),
	ccy("EUR", "Euro", 360, "XM", "European Central Bank"),
	ccy("GBP", "Pound sterling", 365, "GB", "Bank of England"),
	ccy("JPY", "Japanese yen", 360, "JP", "Bank of Japan"),
	ccy("CAD", "Canadian dollar", 365, "CA", "Bank of Canada"),
	ccy("SAR", "Saudi riyal", 360, "SA", "Saudi Central Bank"),
	ccy("AED", "UAE dirham", 360, "", "Central Bank of the UAE"),
	ccy("QAR", "Qatari riyal", 360, "", "Qatar Central Bank"),
	ccy("KWD", "Kuwaiti dinar", 365, "KW", "Central Bank of Kuwait"),
	ccy("BHD", "Bahraini dinar", 360, "", "Central Bank of Bahrain"),
	ccy("OMR", "Omani rial", 360, "", "Central Bank of Oman"),
	ccy("CHF", "Swiss franc", 360, "CH", "Swiss National Bank"),
	ccy("AUD", "Australian dollar", 365, "AU", "Reserve Bank of Australia"),
	ccy("NZD", "New Zealand dollar", 365, "NZ", "Reserve Bank of New Zealand"),
	ccy("SEK", "Swedish krona", 360, "SE", "Sveriges Riksbank"),
	ccy("NOK", "Norwegian krone", 360, "NO", "Norges Bank"),
	ccy("DKK", "Danish krone", 360, "DK", "Danmarks Nationalbank"),
	ccy("PLN", "Polish zloty", 365, "PL", "Narodowy Bank Polski"),
	ccy("CZK", "Czech koruna", 360, "CZ", "Czech National Bank"),
	ccy("HUF", "Hungarian forint", 360, "HU", "Magyar Nemzeti Bank"),
	ccy("MXN", "Mexican peso", 360, "MX", "Banco de Mexico"),
	ccy("ZAR", "South African rand", 365, "ZA", "South African Reserve Bank"),
	ccy("TRY", "Turkish lira", 360, "TR", "Central Bank of Turkiye"),
	ccy("HKD", "Hong Kong dollar", 365, "HK", "Hong Kong Monetary Authority"),
}

// Pair describes one currency pair's quoting conventions.
type Pair struct {
	Symbol    string  `json:"symbol"`
	Base      string  `json:"base"`
	Quote     string  `json:"quote"`
	PipFactor float64 `json:"pipFactor"` // forward points are quoted in 1/PipFactor of price
	SpotLag   int     `json:"spotLag"`   // business days from trade date to spot value date
	Group     string  `json:"group"`     // "G7", "GCC" or "Other": what the dashboard filters by
}

// Groups lists the filter groups in display order.
var Groups = []string{"G7", "GCC", "Other"}

var (
	g7Currencies  = map[string]bool{"USD": true, "EUR": true, "JPY": true, "GBP": true, "CAD": true}
	gccCurrencies = map[string]bool{"SAR": true, "AED": true, "QAR": true, "KWD": true, "BHD": true, "OMR": true}
)

func pair(base, quote string) Pair {
	p := Pair{Symbol: base + quote, Base: base, Quote: quote, PipFactor: 10000, SpotLag: 2, Group: "Other"}
	switch quote {
	case "JPY", "HUF":
		p.PipFactor = 100
	case "BHD", "KWD", "OMR":
		// Dinars and the rial have 1000 sub-units and are quoted to five decimals, so a pip
		// is 0.00001. Check with `fxarb probe` once their rates are loaded.
		p.PipFactor = 100000
	}
	if p.Symbol == "USDCAD" || p.Symbol == "USDTRY" {
		p.SpotLag = 1
	}
	switch {
	case gccCurrencies[base] || gccCurrencies[quote]:
		p.Group = "GCC"
	case g7Currencies[base] && g7Currencies[quote]:
		p.Group = "G7"
	}
	return p
}

// Pairs is the scanned universe. Add a row here to scan another pair.
var Pairs = []Pair{
	// G7: pairs among USD, EUR, JPY, GBP and CAD (CAD/JPY has no forward quotes at the source)
	pair("EUR", "USD"), pair("GBP", "USD"), pair("USD", "JPY"), pair("USD", "CAD"), pair("EUR", "GBP"),
	pair("EUR", "JPY"), pair("GBP", "JPY"), pair("EUR", "CAD"), pair("GBP", "CAD"),
	// GCC: the dollar against the six Gulf currencies
	pair("USD", "SAR"), pair("USD", "AED"), pair("USD", "QAR"), pair("USD", "KWD"), pair("USD", "BHD"), pair("USD", "OMR"),
	// Other
	pair("USD", "CHF"), pair("AUD", "USD"), pair("NZD", "USD"), pair("USD", "SEK"), pair("USD", "NOK"),
	pair("USD", "DKK"), pair("EUR", "CHF"), pair("AUD", "JPY"), pair("EUR", "SEK"), pair("EUR", "NOK"),
	pair("USD", "PLN"), pair("USD", "CZK"), pair("USD", "HUF"), pair("USD", "MXN"), pair("USD", "ZAR"),
	pair("USD", "TRY"), pair("USD", "HKD"),
}

// CurrencyByCode looks a currency up; ok is false if it is not tracked.
func CurrencyByCode(code string) (Currency, bool) {
	for _, c := range Currencies {
		if c.Code == code {
			return c, true
		}
	}
	return Currency{}, false
}

// PairBySymbol looks a pair up; ok is false if it is not in the universe.
func PairBySymbol(sym string) (Pair, bool) {
	for _, p := range Pairs {
		if p.Symbol == sym {
			return p, true
		}
	}
	return Pair{}, false
}

// Tenor is a standard forward tenor measured from the spot value date.
type Tenor struct {
	Code   string `json:"code"`
	Weeks  int    `json:"-"`
	Months int    `json:"-"`
}

// Tenors are the scanned forward tenors, shortest first.
var Tenors = []Tenor{
	{Code: "1W", Weeks: 1}, {Code: "2W", Weeks: 2}, {Code: "3W", Weeks: 3},
	{Code: "1M", Months: 1}, {Code: "2M", Months: 2}, {Code: "3M", Months: 3}, {Code: "4M", Months: 4},
	{Code: "5M", Months: 5}, {Code: "6M", Months: 6}, {Code: "7M", Months: 7}, {Code: "8M", Months: 8},
	{Code: "9M", Months: 9}, {Code: "10M", Months: 10}, {Code: "11M", Months: 11}, {Code: "1Y", Months: 12},
}

// TenorIndex returns the index of a tenor code in Tenors, or -1.
func TenorIndex(code string) int {
	for i, t := range Tenors {
		if t.Code == code {
			return i
		}
	}
	return -1
}

func isWeekend(d time.Time) bool {
	wd := d.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// SpotDate is the spot value date: trade date plus lag business days.
// Weekends are skipped; currency holiday calendars are not modelled yet.
func SpotDate(trade time.Time, lag int) time.Time {
	d := dateOnly(trade)
	for isWeekend(d) { // a Sunday-evening tick belongs to Monday's trade date
		d = d.AddDate(0, 0, 1)
	}
	for n := 0; n < lag; {
		d = d.AddDate(0, 0, 1)
		if !isWeekend(d) {
			n++
		}
	}
	return d
}

// MaturityDate applies the tenor to the spot date using the modified-following convention.
func MaturityDate(spot time.Time, t Tenor) time.Time {
	var d time.Time
	if t.Weeks > 0 {
		d = spot.AddDate(0, 0, 7*t.Weeks)
	} else {
		y, m, day := spot.Date()
		first := time.Date(y, m+time.Month(t.Months), 1, 0, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1).Day()
		if day > last {
			day = last
		}
		d = time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
	}
	month := d.Month()
	rolled := d
	for isWeekend(rolled) {
		rolled = rolled.AddDate(0, 0, 1)
	}
	if t.Weeks == 0 && rolled.Month() != month { // modified following: do not cross month end
		rolled = d
		for isWeekend(rolled) {
			rolled = rolled.AddDate(0, 0, -1)
		}
	}
	return rolled
}

// TenorDays is the interest period in calendar days (spot date to maturity) for a trade done at trade.
func TenorDays(trade time.Time, lag int, t Tenor) int {
	spot := SpotDate(trade, lag)
	return int(MaturityDate(spot, t).Sub(spot).Hours()/24 + 0.5)
}

// Package api serves the dashboard: reference data, back-tests on demand, and a replay stream
// that pushes the scanner's view to the browser minute by minute.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"strconv"
	"sync"
	"time"

	"fxarb/internal/backtest"
	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/feed"
	"fxarb/internal/market"
)

// Server holds the paths the API works from.
type Server struct {
	DataDir string
	WebDir  string
	Live    *feed.Hub // nil when the live feed is off
	LiveDir string    // where the live feed records, and where its rates table is kept current

	Sets []Dataset // selectable data sets; empty means just DataDir

	mu       sync.Mutex
	sessions map[string]*session
}

// session is the live control state of one replay stream (pause and speed).
type session struct {
	mu     sync.Mutex
	paused bool
	speed  float64
}

func (c *session) get() (bool, float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused, c.speed
}

// control changes the pause state or speed of a running replay: POST /api/stream/control
// {"sid": "...", "paused": true, "speed": 300}.
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SID    string   `json:"sid"`
		Paused *bool    `json:"paused"`
		Speed  *float64 `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	s.mu.Lock()
	c := s.sessions[req.SID]
	s.mu.Unlock()
	if c == nil {
		fail(w, 404, fmt.Errorf("no such replay session"))
		return
	}
	c.mu.Lock()
	if req.Paused != nil {
		c.paused = *req.Paused
	}
	if req.Speed != nil {
		c.speed = *req.Speed
	}
	c.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/rates", s.rates)
	mux.HandleFunc("POST /api/rates", s.setRate)
	mux.HandleFunc("POST /api/backtest", s.backtest)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("POST /api/stream/control", s.control)
	mux.HandleFunc("GET /api/live", s.live)
	mux.HandleFunc("/", s.static)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// Dataset is one directory of market data the dashboard can replay and back-test.
type Dataset struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Dir   string `json:"-"`
}

// datasets lists the selectable data sets; the first is the default.
func (s *Server) datasets() []Dataset {
	if len(s.Sets) == 0 {
		return []Dataset{{Name: "default", Label: "Data", Dir: s.DataDir}}
	}
	return s.Sets
}

// dirFor resolves a data set name ("" = default) to its directory.
func (s *Server) dirFor(name string) (string, error) {
	sets := s.datasets()
	if name == "" {
		return sets[0].Dir, nil
	}
	for _, d := range sets {
		if d.Name == name {
			return d.Dir, nil
		}
	}
	return "", fmt.Errorf("unknown data set %q", name)
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"pairs": market.Pairs, "tenors": market.Tenors, "currencies": market.Currencies, "groups": market.Groups,
		"defaults": engine.DefaultConfig(), "execDelaySec": 60,
	}
	type setInfo struct {
		Dataset
		DataFrom *time.Time `json:"dataFrom,omitempty"`
		DataTo   *time.Time `json:"dataTo,omitempty"`

		Error string `json:"error,omitempty"`
	}
	var sets []setInfo
	for _, d := range s.datasets() {
		info := setInfo{Dataset: d}
		if first, last, err := data.Range(d.Dir, backtest.Symbols(market.Pairs)); err != nil {
			info.Error = err.Error()
		} else {
			info.DataFrom, info.DataTo = &first, &last
		}

		sets = append(sets, info)
	}
	out["datasets"] = sets
	if s.Live != nil {
		out["live"] = s.Live.Summary()
	}
	writeJSON(w, 200, out)
}

// rates returns each currency's reference rates as known at ?asof= (default: latest).
func (s *Server) rates(w http.ResponseWriter, r *http.Request) {
	dir, err := s.dirFor(r.URL.Query().Get("dataset"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	rows, err := data.LoadRates(dir)
	if err != nil {
		fail(w, 500, err)
		return
	}
	asof := time.Now().UTC()
	if v := r.URL.Query().Get("asof"); v != "" {
		if asof, err = data.ParseTS(v); err != nil {
			fail(w, 400, err)
			return
		}
	}
	type entry struct {
		market.Currency
		Policy      *float64           `json:"policy"`
		PolicySince string             `json:"policySince"`
		Curve       map[string]float64 `json:"curve"`
	}
	byCcy := map[string]*entry{}
	var out []*entry
	for _, c := range market.Currencies {
		e := &entry{Currency: c, Curve: map[string]float64{}}
		byCcy[c.Code] = e
		out = append(out, e)
	}
	for _, row := range rows { // rows are sorted by date; stop at asof so nothing later leaks in
		if row.Date.After(asof) {
			break
		}
		e := byCcy[row.Ccy]
		if e == nil {
			continue
		}
		if row.Tenor == engine.PolicyTenor {
			v := row.Rate
			e.Policy, e.PolicySince = &v, row.Date.Format("2006-01-02")
		} else {
			e.Curve[row.Tenor] = row.Rate
		}
	}
	writeJSON(w, 200, out)
}

// setRate stores a policy rate the user has entered for a currency the BIS does not cover:
// POST /api/rates {"ccy":"BHD","ratePct":4.5,"since":"2026-09-17"}. The rate is written to the
// rates table from its effective date and, when the live feed is running, takes effect in
// every open dashboard from this moment.
func (s *Server) setRate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ccy     string  `json:"ccy"`
		RatePct float64 `json:"ratePct"`
		Since   string  `json:"since"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	c, ok := market.CurrencyByCode(req.Ccy)
	if !ok || !c.ManualRate {
		fail(w, 400, fmt.Errorf("%s is not a currency whose rate is entered by hand", req.Ccy))
		return
	}
	if req.RatePct != req.RatePct || req.RatePct < -5 || req.RatePct > 100 {
		fail(w, 400, fmt.Errorf("rate must be a percentage between -5 and 100"))
		return
	}
	now := time.Now().UTC()
	since := now.Truncate(24 * time.Hour)
	if req.Since != "" {
		d, err := time.Parse("2006-01-02", req.Since)
		if err != nil || d.After(now) {
			fail(w, 400, fmt.Errorf("the effective date must be a past or present date, YYYY-MM-DD"))
			return
		}
		since = d
	}
	dir := s.LiveDir
	if dir == "" {
		dir = s.DataDir
	}
	s.mu.Lock()
	rows, _ := data.LoadRates(dir)
	kept := rows[:0]
	for _, row := range rows { // one hand-entered rate per currency and date
		if !(row.Ccy == c.Code && row.Tenor == engine.PolicyTenor && row.Date.Equal(since)) {
			kept = append(kept, row)
		}
	}
	kept = append(kept, data.RateRow{Date: since, Ccy: c.Code, Tenor: engine.PolicyTenor, Rate: req.RatePct})
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Date.Before(kept[b].Date) })
	err := data.SaveRates(dir, kept)
	s.mu.Unlock()
	if err != nil {
		fail(w, 500, err)
		return
	}
	if s.Live != nil {
		s.Live.Publish([]engine.Event{{TS: now.Truncate(time.Second), Kind: engine.EvRate, Ccy: c.Code, Tenor: engine.PolicyTenor, Rate: req.RatePct}})
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type runRequest struct {
	From         string        `json:"from"`
	To           string        `json:"to"`
	Config       engine.Config `json:"config"`
	ExecDelaySec int           `json:"execDelaySec"`
	Pairs        []string      `json:"pairs"`
	Dataset      string        `json:"dataset"`
}

func (s *Server) backtest(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	dir, err := s.dirFor(req.Dataset)
	if err != nil {
		fail(w, 400, err)
		return
	}
	p := backtest.Params{DataDir: dir, Cfg: req.Config, ExecDelaySec: req.ExecDelaySec, Pairs: req.Pairs}
	if req.From != "" {
		if p.From, err = data.ParseTS(req.From); err != nil {
			fail(w, 400, err)
			return
		}
	}
	if req.To != "" {
		if p.To, err = data.ParseTS(req.To); err != nil {
			fail(w, 400, err)
			return
		}
	}
	rep, err := backtest.Run(p)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, rep)
}

// stream replays history as server-sent events. The browser sees exactly what a trader would
// have seen at each simulated minute: the scanner is fed one timestamp at a time and the
// stream never reads ahead of the simulated clock.
//
// Query: from (RFC 3339), speed (simulated seconds per real second, 0 = flat out),
// execDelaySec, config (JSON-encoded engine.Config).
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cfg := engine.DefaultConfig()
	if v := q.Get("config"); v != "" {
		if err := json.Unmarshal([]byte(v), &cfg); err != nil {
			fail(w, 400, err)
			return
		}
	}
	var from time.Time
	if v := q.Get("from"); v != "" {
		var err error
		if from, err = data.ParseTS(v); err != nil {
			fail(w, 400, err)
			return
		}
	}
	speed, _ := strconv.ParseFloat(q.Get("speed"), 64)
	ctl := &session{speed: speed}
	if sid := q.Get("sid"); sid != "" {
		s.mu.Lock()
		if s.sessions == nil {
			s.sessions = map[string]*session{}
		}
		s.sessions[sid] = ctl
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.sessions, sid)
			s.mu.Unlock()
		}()
	}
	delay := 60
	if v, err := strconv.Atoi(q.Get("execDelaySec")); err == nil {
		delay = v
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	dir, err := s.dirFor(q.Get("dataset"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	st, err := data.OpenStream(dir, backtest.Symbols(market.Pairs), from, time.Time{})
	if err != nil {
		fail(w, 500, err)
		return
	}
	defer st.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	}

	tr := engine.NewTracker(engine.NewScanner(cfg, nil), time.Duration(delay)*time.Second)
	var pnl float64
	var trades, missed int
	var prevTS time.Time
	started := false
	lastTick := time.Time{}
	tick := func() {
		send("tick", map[string]any{"ts": tr.SC.Now(), "quotes": tr.SC.Snapshot(),
			"alerts": len(tr.Episodes), "trades": trades, "missed": missed, "pnlUSD": pnl})
		fl.Flush()
		lastTick = time.Now()
	}
	for {
		paused, speed := ctl.get()
		for paused { // hold the simulated clock; nothing is read from the data while paused
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			paused, speed = ctl.get()
		}
		batch, err := st.NextBatch()
		if err != nil {
			send("fault", map[string]string{"error": err.Error()})
			fl.Flush()
			return
		}
		if batch == nil {
			break
		}
		res, err := tr.Step(batch)
		if err != nil {
			send("fault", map[string]string{"error": err.Error()})
			fl.Flush()
			return
		}
		ts := batch[0].TS
		if !started && !engine.HasQuotes(batch) { // rate history replayed to warm the scanner up
			continue
		}
		started = true
		for _, e := range res.Opened {
			send("alert", e)
		}
		for _, e := range res.Filled {
			if e.Status == "filled" {
				trades++
				pnl += e.PnLUSD
			} else {
				missed++
			}
			send("fill", e)
		}
		for _, e := range res.Closed {
			send("close", e)
		}

		var wait time.Duration
		if speed > 0 && !prevTS.IsZero() {
			wait = time.Duration(float64(ts.Sub(prevTS)) / speed)
			if wait > 2*time.Second { // do not sit through weekends
				wait = 2 * time.Second
			}
		}
		prevTS = ts
		if wait > 0 || time.Since(lastTick) > 200*time.Millisecond {
			tick()
		} else if len(res.Opened)+len(res.Filled)+len(res.Closed) > 0 {
			fl.Flush()
		}
		if wait > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(wait):
			}
		} else if r.Context().Err() != nil {
			return
		}
	}
	tick()
	send("end", map[string]any{"ts": tr.SC.Now()})
	fl.Flush()
}

// live streams the scanner's view of the live feed. Each connection gets its own scanner with
// its own assumptions, warmed up with the latest quote per instrument and then fed every new
// reading as it arrives.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	if s.Live == nil {
		fail(w, 404, fmt.Errorf("the live feed is off; start the server with -live"))
		return
	}
	q := r.URL.Query()
	cfg := engine.DefaultConfig()
	if v := q.Get("config"); v != "" {
		if err := json.Unmarshal([]byte(v), &cfg); err != nil {
			fail(w, 400, err)
			return
		}
	}
	delay := 60
	if v, err := strconv.Atoi(q.Get("execDelaySec")); err == nil {
		delay = v
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	ratesDir := s.LiveDir
	if ratesDir == "" {
		ratesDir = s.DataDir
	}
	rates, err := data.LoadRates(ratesDir)
	if err != nil {
		fail(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	}

	tr := engine.NewTracker(engine.NewScanner(cfg, nil), time.Duration(delay)*time.Second)
	var pnl float64
	var trades, missed int
	var lastQuote time.Time // the scanner's clock also advances on rate changes; show quote time
	step := func(batch []engine.Event) bool {
		res, err := tr.Step(batch)
		if err != nil {
			send("fault", map[string]string{"error": err.Error()})
			fl.Flush()
			return false
		}
		if engine.HasQuotes(batch) {
			lastQuote = batch[0].TS
		}
		for _, e := range res.Opened {
			send("alert", e)
		}
		for _, e := range res.Filled {
			if e.Status == "filled" {
				trades++
				pnl += e.PnLUSD
			} else {
				missed++
			}
			send("fill", e)
		}
		for _, e := range res.Closed {
			send("close", e)
		}
		return true
	}
	tick := func() {
		send("tick", map[string]any{"ts": lastQuote, "quotes": tr.SC.Snapshot(), "alerts": len(tr.Episodes),
			"trades": trades, "missed": missed, "pnlUSD": pnl, "feed": s.Live.Summary()})
		fl.Flush()
	}

	s.Live.SeedNormal(tr.SC) // start with the normal levels the server has already learned
	snapshot, ch, cancel := s.Live.Subscribe()
	defer cancel()
	now := time.Now().UTC()
	for _, row := range rates { // only rates already in effect
		if row.Date.After(now) {
			break
		}
		if !step([]engine.Event{{TS: row.Date, Kind: engine.EvRate, Ccy: row.Ccy, Tenor: row.Tenor, Rate: row.Rate}}) {
			return
		}
	}
	for _, batch := range snapshot {
		if !step(batch) {
			return
		}
	}
	tick()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			tick()
		case batch, open := <-ch:
			if !open {
				send("fault", map[string]string{"error": "this view fell behind the feed; reconnect"})
				fl.Flush()
				return
			}
			if !step(batch) {
				return
			}
			tick()
		}
	}
}

// static serves the built dashboard (web/dist) with a single-page-app fallback.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if s.WebDir == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.WebDir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		path = filepath.Join(s.WebDir, "index.html")
	}
	if filepath.Base(path) == "index.html" {
		// The page names its scripts by content hash; never let a browser keep an old page,
		// or it would go on showing an out-of-date dashboard after an update.
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeFile(w, r, path)
}

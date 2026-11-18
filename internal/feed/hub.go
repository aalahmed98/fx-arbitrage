package feed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fxarb/internal/data"
	"fxarb/internal/engine"
	"fxarb/internal/market"
)

const (
	// MaxFeedLag: if even the newest forward row on a page is older than this, the market is
	// shut (or the feed is down) and no forward quotes are passed on.
	// Investing.com delays majors by about 30 minutes and other pairs by about 60.
	MaxFeedLag = 75 * time.Minute
	// MaxRowLag drops a tenor that was last quoted this much before the newest row on its
	// page, i.e. one nobody has re-quoted for a while.
	MaxRowLag = 10 * time.Minute
)

// Events turns a snapshot into scanner events, all stamped with the observation time, so the
// scanner never sees a quote earlier than the moment we actually had it. It also returns how
// far the source's forward quotes trail the clock (investing.com runs about 30 minutes behind).
func Events(s *Snapshot) (evs []engine.Event, lag time.Duration) {
	evs = []engine.Event{{TS: s.At, Kind: engine.EvSpot, Pair: s.Pair, Bid: s.SpotBid, Ask: s.SpotAsk}}
	var newest time.Time
	for _, p := range s.Points {
		if p.QuoteTime.After(newest) {
			newest = p.QuoteTime
		}
	}
	lag = s.At.Sub(newest)
	if lag > MaxFeedLag {
		return evs, lag
	}
	for _, p := range s.Points {
		if newest.Sub(p.QuoteTime) > MaxRowLag {
			continue
		}
		evs = append(evs, engine.Event{TS: s.At, Kind: engine.EvForward, Pair: s.Pair, Tenor: p.Tenor, Bid: p.Bid, Ask: p.Ask})
	}
	return evs, lag
}

// PairStatus is the health of one pair's feed.
type PairStatus struct {
	Pair    string    `json:"pair"`
	LastOK  time.Time `json:"lastOk"`
	Tenors  int       `json:"tenors"` // fresh tenors in the last reading
	LagSec  int       `json:"lagSec"` // how far the forward quotes trailed the clock
	Error   string    `json:"error,omitempty"`
	Fetches int       `json:"fetches"`
}

// Hub fans live events out to any number of scanners and remembers the latest quote per
// instrument so a scanner that connects later starts from the current market.
type Hub struct {
	Source string

	mu     sync.Mutex
	latest map[string]engine.Event
	subs   map[int]chan []engine.Event
	nextID int
	status map[string]*PairStatus
	last   time.Time
	// master watches every reading to learn each pair's normal gap to parity, so a dashboard
	// that connects (or changes an assumption) does not have to start learning from scratch.
	master *engine.Scanner
}

// SetMaster installs the scanner that learns normal levels from the feed. It may already
// have been warmed up on recorded history.
func (h *Hub) SetMaster(sc *engine.Scanner) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.master = sc
}

// Prime remembers quotes read back from the recorded history at start-up, without sending
// them to anyone, so a dashboard opened right after a restart shows the last known market
// instead of an empty grid. Quotes too old to trust are shown as stale by each scanner.
func (h *Hub) Prime(batch []engine.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range batch {
		if ev.Kind != engine.EvRate {
			h.latest[ev.Pair+"|"+ev.Tenor] = ev
		}
	}
}

// SeedNormal copies the learned normal levels into a subscriber's scanner.
func (h *Hub) SeedNormal(sc *engine.Scanner) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.master != nil {
		sc.CopyNormalFrom(h.master)
	}
}

// NewHub creates an empty hub.
func NewHub(source string) *Hub {
	return &Hub{Source: source, latest: map[string]engine.Event{}, subs: map[int]chan []engine.Event{}, status: map[string]*PairStatus{}}
}

// Publish records a batch (all events share one timestamp) and sends it to every subscriber.
func (h *Hub) Publish(batch []engine.Event) {
	if len(batch) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range batch {
		if ev.Kind != engine.EvRate { // rates come from the rates file, not the quote cache
			h.latest[ev.Pair+"|"+ev.Tenor] = ev
		}
	}
	if h.master != nil {
		h.master.Learn(batch) // an out-of-order batch is simply not learned from
	}
	h.last = batch[0].TS
	for id, ch := range h.subs {
		select {
		case ch <- batch:
		default: // a subscriber that cannot keep up is dropped rather than blocking the feed
			close(ch)
			delete(h.subs, id)
		}
	}
}

// Subscribe returns the current market as time-ordered batches, a channel of future batches,
// and a cancel function.
func (h *Hub) Subscribe() (snapshot [][]engine.Event, ch <-chan []engine.Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	all := make([]engine.Event, 0, len(h.latest))
	for _, ev := range h.latest {
		all = append(all, ev)
	}
	sort.Slice(all, func(a, b int) bool { return all[a].TS.Before(all[b].TS) })
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && all[j].TS.Equal(all[i].TS) {
			j++
		}
		snapshot = append(snapshot, all[i:j])
		i = j
	}
	c := make(chan []engine.Event, 512)
	id := h.nextID
	h.nextID++
	h.subs[id] = c
	return snapshot, c, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

// Status reports per-pair feed health and the time of the latest event.
func (h *Hub) Status() (pairs []PairStatus, last time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range market.Pairs {
		if s := h.status[p.Symbol]; s != nil {
			pairs = append(pairs, *s)
		}
	}
	return pairs, h.last
}

// Summary condenses feed health for the dashboard.
type Summary struct {
	Source    string    `json:"source"`
	Last      time.Time `json:"last"`
	PairsOK   int       `json:"pairsOk"`
	PairsDown []string  `json:"pairsDown"`
	LagSec    int       `json:"lagSec"` // median delay of the forward quotes
}

// Summary reports how many pairs are reporting and how delayed the quotes are.
func (h *Hub) Summary() Summary {
	pairs, last := h.Status()
	s := Summary{Source: h.Source, Last: last, PairsDown: []string{}}
	var lags []int
	for _, p := range pairs {
		if p.Error != "" || p.Tenors == 0 {
			s.PairsDown = append(s.PairsDown, p.Pair)
			continue
		}
		s.PairsOK++
		lags = append(lags, p.LagSec)
	}
	if len(lags) > 0 {
		sort.Ints(lags)
		s.LagSec = lags[len(lags)/2]
	}
	return s
}

// note records the outcome of one reading and reports whether the pair's error state changed
// (so a pair that fails every minute is logged once, not every minute).
func (h *Hub) note(pair string, snap *Snapshot, fresh int, lag time.Duration, err error) (changed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.status[pair]
	if s == nil {
		s = &PairStatus{Pair: pair}
		h.status[pair] = s
	}
	s.Fetches++
	if err != nil {
		changed = s.Error != err.Error()
		s.Error = err.Error()
		return changed
	}
	changed = s.Error != ""
	s.Error, s.LastOK, s.Tenors, s.LagSec = "", snap.At, fresh, int(lag.Seconds())
	return changed
}

// archive compresses a finished month's file to <name>.<YYYY-MM>.csv.gz and removes the
// original. The back-test reads archives and the current file as one history.
func archive(path string, month time.Time) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	target := strings.TrimSuffix(path, ".csv") + "." + month.Format("2006-01") + ".csv.gz"
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	w, err := data.Create(target)
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		w.Close()
		os.Remove(target)
		return err
	}
	if err := w.Close(); err != nil {
		os.Remove(target)
		return err
	}
	return os.Remove(path)
}

var (
	monthsMu sync.Mutex
	months   = map[string]time.Time{} // file -> time of our last write to it
)

func appendRow(path, header, row string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// The first row of a new month starts a new file; the finished month is compressed.
	// The month a file belongs to is remembered from our own last write to it; after a
	// restart it is taken from the file's modification time.
	if st, err := os.Stat(path); err == nil {
		monthsMu.Lock()
		m, known := months[path]
		monthsMu.Unlock()
		if !known {
			m = st.ModTime().UTC()
		}
		if m.Year() != now.Year() || m.Month() != now.Month() {
			if err := archive(path, m); err != nil {
				return err
			}
		}
	}
	monthsMu.Lock()
	months[path] = now
	monthsMu.Unlock()
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if statErr != nil {
		row = header + row
	}
	_, err = f.WriteString(row)
	return err
}

func ff(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// Record polls every pair once per cycle, publishes what it reads and appends it to dir in
// the same layout the back-test reads, so recorded days can be replayed later.
// It runs until ctx is cancelled.
func Record(ctx context.Context, hub *Hub, src *Investing, pairs []market.Pair, cycle time.Duration, dir string, logf func(string, ...any)) {
	gap := cycle / time.Duration(len(pairs))
	if gap < time.Second {
		gap = time.Second // never more than one request a second
	}
	var lastTS time.Time
	for {
		for _, p := range pairs {
			started := time.Now()
			snap, err := src.Fetch(ctx, p)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if hub.note(p.Symbol, nil, 0, 0, err) {
					logf("feed: %v", err)
				}
			} else {
				if !snap.At.After(lastTS) { // keep observation times strictly increasing
					snap.At = lastTS.Add(time.Second)
				}
				lastTS = snap.At
				evs, lag := Events(snap)
				if hub.note(p.Symbol, snap, len(evs)-1, lag, nil) {
					logf("feed: %s is reporting again", p.Symbol)
				}
				hub.Publish(evs)
				stamp := data.FormatTS(snap.At)
				// Each forward row keeps two times: when we read it (ts) and the time the source
				// itself put on the quote (quote_ts), so the record shows how old every quote was.
				quoted := map[string]time.Time{}
				for _, pt := range snap.Points {
					quoted[pt.Tenor] = pt.QuoteTime
				}
				rows := ""
				for _, ev := range evs[1:] {
					rows += fmt.Sprintf("%s,%s,%s,%s,%s\n", stamp, ev.Tenor, ff(ev.Bid), ff(ev.Ask), data.FormatTS(quoted[ev.Tenor]))
				}
				e1 := appendRow(filepath.Join(dir, "spot", p.Symbol+".csv"), "ts,bid,ask\n",
					fmt.Sprintf("%s,%s,%s\n", stamp, ff(snap.SpotBid), ff(snap.SpotAsk)), snap.At)
				var e2 error
				if rows != "" {
					e2 = appendRow(filepath.Join(dir, "forwards", p.Symbol+".csv"), "ts,tenor,bid_pts,ask_pts,quote_ts\n", rows, snap.At)
				}
				if e1 != nil || e2 != nil {
					logf("feed: cannot write %s history: %v %v", p.Symbol, e1, e2)
				}
			}
			// Pace by the clock so a full cycle takes the requested time whatever the
			// pages' response times, but never hurry: at least a quarter second between requests.
			wait := gap - time.Since(started)
			if wait < 250*time.Millisecond {
				wait = 250 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

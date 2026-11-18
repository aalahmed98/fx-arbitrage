// Package data reads and writes the on-disk market-data files and merges them into a single
// time-ordered event stream.
//
// Layout of a data directory (every file may be plain .csv or gzip-compressed .csv.gz):
//
//	rates.csv                date,ccy,tenor,rate_pct      tenor is POLICY or 1W/2W/1M/2M/3M/6M/9M/1Y
//	spot/EURUSD.csv          ts,bid,ask                   outright prices
//	forwards/EURUSD.csv      ts,tenor,bid_pts,ask_pts     forward points in pips; recorded rows also carry
//	                                                      quote_ts, the source's own time for the quote
//
// A pair's history may be split into archives plus a current file, read in this order:
// EURUSD.2026-11.csv.gz, EURUSD.2026-12.csv.gz, ..., EURUSD.csv. The live recorder archives
// each finished month this way.
//
// Timestamps are UTC, RFC 3339 (2026-11-09T14:31:00Z). Rows must be in time order.
package data

import (
	"bufio"
	"compress/gzip"
	"container/heap"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fxarb/internal/engine"
)

const tsLayout = "2006-01-02T15:04:05Z"

// FormatTS renders a timestamp the way the data files store it.
func FormatTS(t time.Time) string { return t.UTC().Format(tsLayout) }

// ParseTS accepts RFC 3339, "2006-01-02 15:04:05", a bare date, or unix seconds.
func ParseTS(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) == 20 && s[19] == 'Z' && s[10] == 'T' { // fast path for our own files
		n := func(a, b int) int {
			v := 0
			for _, c := range s[a:b] {
				v = v*10 + int(c-'0')
			}
			return v
		}
		return time.Date(n(0, 4), time.Month(n(5, 7)), n(8, 10), n(11, 13), n(14, 16), n(17, 19), 0, time.UTC), nil
	}
	for _, l := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

// Resolve finds base+".csv" or base+".csv.gz"; ok is false if neither exists.
func Resolve(base string) (string, bool) {
	for _, ext := range []string{".csv", ".csv.gz"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext, true
		}
	}
	return "", false
}

type readCloser struct {
	io.Reader
	closers []io.Closer
}

func (r readCloser) Close() error {
	for _, c := range r.closers {
		c.Close()
	}
	return nil
}

// Open opens a .csv or .csv.gz file for reading.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 1<<16)
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		return readCloser{gz, []io.Closer{gz, f}}, nil
	}
	return readCloser{br, []io.Closer{f}}, nil
}

// Writer writes a .csv or .csv.gz file.
type Writer struct {
	f  *os.File
	gz *gzip.Writer
	bw *bufio.Writer
}

// Create creates path (and its directory); a .gz suffix turns on compression.
func Create(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &Writer{f: f}
	if strings.HasSuffix(path, ".gz") {
		w.gz, _ = gzip.NewWriterLevel(f, gzip.BestSpeed)
		w.bw = bufio.NewWriterSize(w.gz, 1<<16)
	} else {
		w.bw = bufio.NewWriterSize(f, 1<<16)
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error)       { return w.bw.Write(p) }
func (w *Writer) WriteString(s string) (int, error) { return w.bw.WriteString(s) }

// Close flushes and closes the file.
func (w *Writer) Close() error {
	err := w.bw.Flush()
	if w.gz != nil {
		if e := w.gz.Close(); err == nil {
			err = e
		}
	}
	if e := w.f.Close(); err == nil {
		err = e
	}
	return err
}

// RateRow is one row of rates.csv.
type RateRow struct {
	Date  time.Time
	Ccy   string
	Tenor string
	Rate  float64 // percent
}

// LoadRates reads rates.csv from a data directory, sorted by date.
func LoadRates(dir string) ([]RateRow, error) {
	path, ok := Resolve(filepath.Join(dir, "rates"))
	if !ok {
		return nil, fmt.Errorf("no rates.csv in %s (run: fxarb fetch-rates)", dir)
	}
	rc, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	recs, err := csv.NewReader(rc).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var rows []RateRow
	for i, r := range recs {
		if i == 0 || len(r) < 4 {
			continue
		}
		d, err := ParseTS(r[0])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(r[3]), 64)
		if err != nil || v != v || v > 1000 || v < -100 {
			return nil, fmt.Errorf("%s line %d: bad rate %q", path, i+1, r[3])
		}
		rows = append(rows, RateRow{d, strings.ToUpper(strings.TrimSpace(r[1])), strings.ToUpper(strings.TrimSpace(r[2])), v})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].Date.Before(rows[b].Date) })
	return rows, nil
}

// SaveRates writes rates.csv.
func SaveRates(dir string, rows []RateRow) error {
	w, err := Create(filepath.Join(dir, "rates.csv"))
	if err != nil {
		return err
	}
	w.WriteString("date,ccy,tenor,rate_pct\n")
	for _, r := range rows {
		fmt.Fprintf(w, "%s,%s,%s,%s\n", r.Date.Format("2006-01-02"), r.Ccy, r.Tenor, strconv.FormatFloat(r.Rate, 'f', -1, 64))
	}
	return w.Close()
}

// Parts lists the files holding one pair's history of a kind ("spot" or "forwards"), oldest
// first: dated archives in name order, then the current file. Unfinished downloads
// (*.part.*) are ignored.
func Parts(dir, kind, sym string) []string {
	base := filepath.Join(dir, kind, sym)
	var archives []string
	for _, ext := range []string{".csv", ".csv.gz"} {
		m, _ := filepath.Glob(base + ".*" + ext)
		for _, p := range m {
			if !strings.Contains(filepath.Base(p), ".part.") {
				archives = append(archives, p)
			}
		}
	}
	sort.Strings(archives)
	if cur, ok := Resolve(base); ok {
		archives = append(archives, cur)
	}
	return archives
}

// source yields events from one pair's files, in time order.
type source struct {
	rd    *csv.Reader
	rc    io.Closer
	path  string
	rest  []string // files still to be read after the current one
	pair  string
	isFwd bool
	line  int
	cur   engine.Event
}

func openSource(path, pair string, isFwd bool) (*source, error) {
	return openParts([]string{path}, pair, isFwd)
}

func openParts(paths []string, pair string, isFwd bool) (*source, error) {
	s := &source{rest: paths, pair: pair, isFwd: isFwd}
	if err := s.advance(); err != nil {
		return nil, err
	}
	return s, nil
}

// advance closes the current file and opens the next one.
func (s *source) advance() error {
	if s.rc != nil {
		s.rc.Close()
		s.rc = nil
	}
	if len(s.rest) == 0 {
		return io.EOF
	}
	s.path, s.rest = s.rest[0], s.rest[1:]
	rc, err := Open(s.path)
	if err != nil {
		return err
	}
	rd := csv.NewReader(rc)
	rd.ReuseRecord = true
	rd.FieldsPerRecord = -1
	if _, err := rd.Read(); err != nil && !errors.Is(err, io.EOF) { // header
		rc.Close()
		return fmt.Errorf("%s: %w", s.path, err)
	}
	s.rd, s.rc, s.line = rd, rc, 1
	return nil
}

func (s *source) close() {
	if s.rc != nil {
		s.rc.Close()
		s.rc = nil
	}
}

// next loads the next row into s.cur; it returns false when every file is exhausted.
func (s *source) next() (bool, error) {
	rec, err := s.rd.Read()
	for errors.Is(err, io.EOF) {
		if e := s.advance(); e != nil {
			if errors.Is(e, io.EOF) {
				return false, nil
			}
			return false, e
		}
		rec, err = s.rd.Read()
	}
	s.line++
	if err != nil {
		return false, fmt.Errorf("%s line %d: %w", s.path, s.line, err)
	}
	want := 3
	if s.isFwd {
		want = 4
	}
	if len(rec) < want {
		return false, fmt.Errorf("%s line %d: expected %d columns", s.path, s.line, want)
	}
	ts, err := ParseTS(rec[0])
	if err != nil {
		return false, fmt.Errorf("%s line %d: %w", s.path, s.line, err)
	}
	ev := engine.Event{TS: ts, Pair: s.pair, Kind: engine.EvSpot}
	col := 1
	if s.isFwd {
		ev.Kind = engine.EvForward
		ev.Tenor = strings.ToUpper(rec[1])
		col = 2
	}
	if ev.Bid, err = strconv.ParseFloat(rec[col], 64); err == nil {
		ev.Ask, err = strconv.ParseFloat(rec[col+1], 64)
	}
	if err != nil {
		return false, fmt.Errorf("%s line %d: bad price", s.path, s.line)
	}

	s.cur = ev
	return true, nil
}

type srcHeap []*source

func (h srcHeap) Len() int           { return len(h) }
func (h srcHeap) Less(a, b int) bool { return h[a].cur.TS.Before(h[b].cur.TS) }
func (h srcHeap) Swap(a, b int)      { h[a], h[b] = h[b], h[a] }
func (h *srcHeap) Push(x any)        { *h = append(*h, x.(*source)) }
func (h *srcHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// Stream merges every spot and forward file plus the rates table into one time-ordered feed.
type Stream struct {
	h     srcHeap
	rates []RateRow
	ri    int
	from  time.Time
	to    time.Time
	batch []engine.Event
	all   []*source
}

// OpenStream opens the data directory for the given pair symbols. Quotes before from are
// skipped (rates before from are still delivered so the scanner starts with the right curve);
// the stream ends at to. Zero times mean no bound.
func OpenStream(dir string, pairs []string, from, to time.Time) (*Stream, error) {
	rates, err := LoadRates(dir)
	if err != nil {
		return nil, err
	}
	st := &Stream{rates: rates, from: from, to: to}
	for _, sym := range pairs {
		for _, kind := range []string{"spot", "forwards"} {
			parts := Parts(dir, kind, sym)
			if len(parts) == 0 {
				continue
			}
			src, err := openParts(parts, sym, kind == "forwards")
			if err != nil {
				st.Close()
				return nil, err
			}
			st.all = append(st.all, src)
			ok, err := src.next()
			for err == nil && ok && src.cur.TS.Before(from) {
				ok, err = src.next()
			}
			if err != nil {
				st.Close()
				return nil, err
			}
			if ok {
				st.h = append(st.h, src)
			}
		}
	}
	if len(st.all) == 0 {
		return nil, fmt.Errorf("no spot/ or forwards/ files in %s for the selected pairs", dir)
	}
	heap.Init(&st.h)
	return st, nil
}

// NextBatch returns every event sharing the next timestamp, or nil at the end.
// The returned slice is reused by the following call.
func (st *Stream) NextBatch() ([]engine.Event, error) {
	st.batch = st.batch[:0]
	if len(st.h) == 0 { // quotes ran out; later rate changes have nothing left to price
		return nil, nil
	}
	ts := st.h[0].cur.TS
	if st.ri < len(st.rates) && !st.rates[st.ri].Date.After(ts) {
		ts = st.rates[st.ri].Date
	}
	if !st.to.IsZero() && !ts.Before(st.to) {
		return nil, nil
	}
	for st.ri < len(st.rates) && st.rates[st.ri].Date.Equal(ts) {
		r := st.rates[st.ri]
		st.batch = append(st.batch, engine.Event{TS: ts, Kind: engine.EvRate, Ccy: r.Ccy, Tenor: r.Tenor, Rate: r.Rate})
		st.ri++
	}
	for len(st.h) > 0 && st.h[0].cur.TS.Equal(ts) {
		src := st.h[0]
		st.batch = append(st.batch, src.cur)
		ok, err := src.next()
		if err != nil {
			return nil, err
		}
		if ok {
			if src.cur.TS.Before(ts) {
				return nil, fmt.Errorf("%s line %d: timestamps go backwards", src.path, src.line)
			}
			heap.Fix(&st.h, 0)
		} else {
			heap.Pop(&st.h)
		}
	}
	return st.batch, nil
}

// Close releases every open file.
func (st *Stream) Close() {
	for _, s := range st.all {
		s.close()
	}
}

// Range scans one spot file and returns its first and last timestamps.
func Range(dir string, pairs []string) (first, last time.Time, err error) {
	for _, sym := range pairs {
		parts := Parts(dir, "spot", sym)
		if len(parts) == 0 {
			continue
		}
		// The first timestamp is the first row of the oldest file; the last is the final row
		// of the newest, so only that one file is read to the end.
		head, err := openSource(parts[0], sym, false)
		if err != nil {
			return first, last, err
		}
		ok, err := head.next()
		head.close()
		if err != nil {
			return first, last, err
		}
		if ok {
			first, last = head.cur.TS, head.cur.TS
		}
		tail, err := openSource(parts[len(parts)-1], sym, false)
		if err != nil {
			return first, last, err
		}
		defer tail.close()
		for {
			ok, err := tail.next()
			if err != nil {
				return first, last, err
			}
			if !ok {
				if first.IsZero() {
					break // an empty file: try the next pair
				}
				return first, last, nil
			}
			if first.IsZero() {
				first = tail.cur.TS
			}
			last = tail.cur.TS
		}
		continue
	}
	return first, last, fmt.Errorf("no spot files in %s", dir)
}

// SpotMids reads a pair's spot file into a map from unix seconds to mid price.
func SpotMids(dir, sym string) (map[int64]float64, error) {
	parts := Parts(dir, "spot", sym)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no spot file for %s in %s", sym, dir)
	}
	src, err := openParts(parts, sym, false)
	if err != nil {
		return nil, err
	}
	defer src.close()
	out := map[int64]float64{}
	for {
		ok, err := src.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out[src.cur.TS.Unix()] = (src.cur.Bid + src.cur.Ask) / 2
	}
}

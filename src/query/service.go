package query

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/droidpector/apkinspector/src/model"
)

// Source loads the persisted rows of a session (implemented by storage.Store).
type Source interface {
	Summaries(ctx context.Context, sessionID string) ([]model.Summary, error)
}

// Request is a page query from the UI.
type Request struct {
	SessionID string `json:"sessionId"`
	Filter    string `json:"filter"`
	Quick     string `json:"quick"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	Sort      string `json:"sort"` // seq (default), method, host, path, status, type, size, duration, time
	Desc      bool   `json:"desc"`
}

// Page is one window of matching rows.
type Page struct {
	Rows    []model.Summary `json:"rows"`
	Total   int             `json:"total"`   // rows matching the filter
	All     int             `json:"all"`     // rows in the session
	Version int64           `json:"version"` // increments whenever the session changes
}

// Stats are live session counters.
type Stats struct {
	Requests int64 `json:"requests"`
	Domains  int64 `json:"domains"`
	Bytes    int64 `json:"bytes"`
}

// Service is the Network Query Service. It keeps an in-memory index of the
// rows of recently viewed sessions so filtering/paging never scans SQLite, and
// is updated live by the capture recorder. All methods are goroutine-safe.
type Service struct {
	src       Source
	maxCached int

	mu    sync.Mutex
	index map[string]*sessionIndex
	lru   []string // most recently used last
}

type sessionIndex struct {
	rows    []model.Summary
	pos     map[string]int
	hosts   map[string]int
	bytes   int64
	version int64
}

// NewService creates a query service caching up to maxCached sessions.
func NewService(src Source, maxCached int) *Service {
	if maxCached <= 0 {
		maxCached = 3
	}
	return &Service{src: src, maxCached: maxCached, index: map[string]*sessionIndex{}}
}

func (s *Service) touch(id string) {
	s.lru = slices.DeleteFunc(s.lru, func(x string) bool { return x == id })
	s.lru = append(s.lru, id)
	for len(s.lru) > s.maxCached {
		delete(s.index, s.lru[0])
		s.lru = s.lru[1:]
	}
}

// load returns the index for a session, loading it from the source if needed.
// Must be called with s.mu held; it releases the lock while loading.
func (s *Service) load(ctx context.Context, id string) (*sessionIndex, error) {
	if ix, ok := s.index[id]; ok {
		s.touch(id)
		return ix, nil
	}
	s.mu.Unlock()
	rows, err := s.src.Summaries(ctx, id)
	s.mu.Lock()
	if err != nil {
		return nil, fmt.Errorf("loading session %s: %w", id, err)
	}
	if ix, ok := s.index[id]; ok { // loaded concurrently (and possibly updated live)
		s.touch(id)
		return ix, nil
	}
	ix := &sessionIndex{pos: make(map[string]int, len(rows)), hosts: map[string]int{}}
	for _, r := range rows {
		ix.put(r)
	}
	s.index[id] = ix
	s.touch(id)
	return ix, nil
}

func (ix *sessionIndex) put(r model.Summary) {
	ix.version++
	if i, ok := ix.pos[r.ID]; ok {
		old := ix.rows[i]
		ix.untrack(old)
		ix.rows[i] = r
		ix.track(r)
		return
	}
	ix.pos[r.ID] = len(ix.rows)
	ix.rows = append(ix.rows, r)
	ix.track(r)
	// Keep sequence order even if rows arrive slightly out of order.
	if n := len(ix.rows); n > 1 && ix.rows[n-2].Seq > r.Seq {
		slices.SortStableFunc(ix.rows, func(a, b model.Summary) int { return cmp.Compare(a.Seq, b.Seq) })
		for i := range ix.rows {
			ix.pos[ix.rows[i].ID] = i
		}
	}
}

func countsAsDomain(r model.Summary) bool { return r.Host != "" && r.Kind != model.KindDNS }

func (ix *sessionIndex) track(r model.Summary) {
	if countsAsDomain(r) {
		ix.hosts[r.Host]++
	}
	ix.bytes += r.RequestSize + r.ResponseSize
}

func (ix *sessionIndex) untrack(r model.Summary) {
	if countsAsDomain(r) {
		if ix.hosts[r.Host]--; ix.hosts[r.Host] <= 0 {
			delete(ix.hosts, r.Host)
		}
	}
	ix.bytes -= r.RequestSize + r.ResponseSize
}

// Apply records a new or updated row for a session. Sessions that are not
// cached are ignored (they will be loaded from storage when queried).
func (s *Service) Apply(sessionID string, r model.Summary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ix, ok := s.index[sessionID]; ok {
		ix.put(r)
	}
}

// Track makes sure a (new, possibly empty) session is cached so live rows are
// indexed from the first event on.
func (s *Service) Track(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.load(ctx, sessionID)
	return err
}

// Invalidate drops the cached index of a session (after clear/delete).
func (s *Service) Invalidate(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.index, sessionID)
	s.lru = slices.DeleteFunc(s.lru, func(x string) bool { return x == sessionID })
}

// Stats returns live counters of a session.
func (s *Service) Stats(ctx context.Context, sessionID string) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load(ctx, sessionID)
	if err != nil {
		return Stats{}, err
	}
	return Stats{Requests: int64(len(ix.rows)), Domains: int64(len(ix.hosts)), Bytes: ix.bytes}, nil
}

var sorters = map[string]func(a, b *model.Summary) int{
	"seq":      func(a, b *model.Summary) int { return cmp.Compare(a.Seq, b.Seq) },
	"time":     func(a, b *model.Summary) int { return a.StartedAt.Compare(b.StartedAt) },
	"method":   func(a, b *model.Summary) int { return strings.Compare(a.Method, b.Method) },
	"host":     func(a, b *model.Summary) int { return strings.Compare(a.Host, b.Host) },
	"path":     func(a, b *model.Summary) int { return strings.Compare(a.Path, b.Path) },
	"status":   func(a, b *model.Summary) int { return cmp.Compare(a.Status, b.Status) },
	"type":     func(a, b *model.Summary) int { return strings.Compare(string(a.Category), string(b.Category)) },
	"size":     func(a, b *model.Summary) int { return cmp.Compare(a.ResponseSize, b.ResponseSize) },
	"duration": func(a, b *model.Summary) int { return cmp.Compare(a.DurationMs, b.DurationMs) },
}

// Query filters, sorts and pages a session's rows.
func (s *Service) Query(ctx context.Context, req Request) (Page, error) {
	m, err := Compile(req.Filter)
	if err != nil {
		return Page{}, err
	}
	quick, err := QuickFilter(req.Quick)
	if err != nil {
		return Page{}, err
	}
	sortKey := req.Sort
	if sortKey == "" {
		sortKey = "seq"
	}
	less, ok := sorters[sortKey]
	if !ok {
		return Page{}, fmt.Errorf("cannot sort by %q", req.Sort)
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 200
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	s.mu.Lock()
	ix, err := s.load(ctx, req.SessionID)
	if err != nil {
		s.mu.Unlock()
		return Page{}, err
	}
	// Filter under the lock into a private slice; sorting happens outside.
	matched := make([]model.Summary, 0, 256)
	for i := range ix.rows {
		r := &ix.rows[i]
		if quick(r) && m(r) {
			matched = append(matched, *r)
		}
	}
	page := Page{All: len(ix.rows), Version: ix.version}
	s.mu.Unlock()

	if sortKey != "seq" || req.Desc {
		slices.SortStableFunc(matched, func(a, b model.Summary) int {
			c := less(&a, &b)
			if c == 0 {
				c = cmp.Compare(a.Seq, b.Seq)
			}
			if req.Desc {
				return -c
			}
			return c
		})
	}
	page.Total = len(matched)
	if req.Offset < len(matched) {
		end := min(req.Offset+req.Limit, len(matched))
		page.Rows = matched[req.Offset:end]
	} else {
		page.Rows = []model.Summary{}
	}
	return page, nil
}

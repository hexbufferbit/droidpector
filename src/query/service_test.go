package query

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

type fakeSource struct {
	rows  map[string][]model.Summary
	loads atomic.Int32
	err   error
}

func (f *fakeSource) Summaries(_ context.Context, id string) ([]model.Summary, error) {
	f.loads.Add(1)
	return append([]model.Summary(nil), f.rows[id]...), f.err
}

func row(seq int64, host string, status int) model.Summary {
	return model.Summary{ID: fmt.Sprintf("e%d", seq), Seq: seq, Kind: model.KindHTTP, Category: model.CatAPI, Host: host,
		Method: "GET", Status: status, ResponseSize: seq * 10, RequestSize: 1, DurationMs: float64(100 - seq), StartedAt: time.Unix(seq, 0)}
}

func TestQueryPagingFilterSort(t *testing.T) {
	src := &fakeSource{rows: map[string][]model.Summary{"s": {row(1, "a.com", 200), row(2, "b.com", 404), row(3, "a.com", 500), row(4, "c.com", 200)}}}
	svc := NewService(src, 2)
	ctx := context.Background()

	p, err := svc.Query(ctx, Request{SessionID: "s", Limit: 2})
	if err != nil || p.Total != 4 || p.All != 4 || len(p.Rows) != 2 || p.Rows[0].Seq != 1 {
		t.Fatalf("page1: %+v %v", p, err)
	}
	p, _ = svc.Query(ctx, Request{SessionID: "s", Offset: 2, Limit: 2})
	if len(p.Rows) != 2 || p.Rows[1].Seq != 4 {
		t.Fatalf("page2: %+v", p)
	}
	p, _ = svc.Query(ctx, Request{SessionID: "s", Offset: 10})
	if len(p.Rows) != 0 || p.Total != 4 {
		t.Fatal("offset beyond end")
	}
	p, _ = svc.Query(ctx, Request{SessionID: "s", Filter: "host:a.com"})
	if p.Total != 2 {
		t.Fatalf("filter: %+v", p)
	}
	p, _ = svc.Query(ctx, Request{SessionID: "s", Sort: "status", Desc: true})
	if p.Rows[0].Status != 500 || p.Rows[3].Status != 200 || p.Rows[2].Seq != 4 {
		t.Fatalf("sort: %+v", p.Rows)
	}
	p, _ = svc.Query(ctx, Request{SessionID: "s", Sort: "duration"})
	if p.Rows[0].Seq != 4 {
		t.Fatal("duration sort")
	}
	if _, err := svc.Query(ctx, Request{SessionID: "s", Sort: "banana"}); err == nil {
		t.Fatal("bad sort accepted")
	}
	if _, err := svc.Query(ctx, Request{SessionID: "s", Filter: "(x"}); err == nil {
		t.Fatal("bad filter accepted")
	}
	if _, err := svc.Query(ctx, Request{SessionID: "s", Quick: "zzz"}); err == nil {
		t.Fatal("bad quick filter accepted")
	}
	if src.loads.Load() != 1 {
		t.Fatalf("session loaded %d times, want 1 (cached)", src.loads.Load())
	}
}

func TestLiveApplyAndStats(t *testing.T) {
	src := &fakeSource{rows: map[string][]model.Summary{}}
	svc := NewService(src, 2)
	ctx := context.Background()
	svc.Apply("s", row(1, "x", 200)) // not tracked yet: ignored
	if err := svc.Track(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	p0, _ := svc.Query(ctx, Request{SessionID: "s"})
	pending := row(1, "a.com", 0)
	pending.State = model.StatePending
	svc.Apply("s", pending)
	svc.Apply("s", row(3, "b.com", 200))
	svc.Apply("s", row(2, "b.com", 200)) // out of order
	done := row(1, "a.com", 201)
	svc.Apply("s", done) // upsert
	p, _ := svc.Query(ctx, Request{SessionID: "s"})
	if p.Total != 3 || p.Rows[0].Status != 201 || p.Rows[1].Seq != 2 || p.Version <= p0.Version {
		t.Fatalf("live rows: %+v", p)
	}
	st, _ := svc.Stats(ctx, "s")
	if st.Requests != 3 || st.Domains != 2 || st.Bytes != 10+1+30+1+20+1 {
		t.Fatalf("stats %+v", st)
	}
	svc.Invalidate("s")
	src.rows["s"] = []model.Summary{row(9, "z", 200)}
	p, _ = svc.Query(ctx, Request{SessionID: "s"})
	if p.Total != 1 {
		t.Fatal("invalidate did not reload")
	}
}

func TestLRUEvictionAndErrors(t *testing.T) {
	src := &fakeSource{rows: map[string][]model.Summary{"a": {row(1, "a", 1)}, "b": nil, "c": nil}}
	svc := NewService(src, 2)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "a"} {
		svc.Query(ctx, Request{SessionID: id})
	}
	if src.loads.Load() != 4 {
		t.Fatalf("expected 'a' to be evicted and reloaded, loads=%d", src.loads.Load())
	}
	bad := NewService(&fakeSource{err: errors.New("disk")}, 1)
	if _, err := bad.Query(ctx, Request{SessionID: "x"}); err == nil {
		t.Fatal("source error must propagate")
	}
}

func TestConcurrentApplyAndQuery(t *testing.T) {
	svc := NewService(&fakeSource{rows: map[string][]model.Summary{}}, 2)
	ctx := context.Background()
	svc.Track(ctx, "s")
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				svc.Apply("s", row(int64(w*1000+i), "h", 200))
			}
		}(w)
	}
	for i := 0; i < 50; i++ {
		if _, err := svc.Query(ctx, Request{SessionID: "s", Filter: "status:200", Sort: "host"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if st, _ := svc.Stats(ctx, "s"); st.Requests != 2000 {
		t.Fatal(st)
	}
}

// BenchmarkQuery100k guards the UI responsiveness target: filtering and
// paging a 100k-row session must take well under 50ms.
func BenchmarkQuery100k(b *testing.B) {
	rows := make([]model.Summary, 100_000)
	for i := range rows {
		rows[i] = row(int64(i), fmt.Sprintf("h%d.example.com", i%50), 200+i%300)
		rows[i].Path = fmt.Sprintf("/api/v1/items/%d", i)
	}
	svc := NewService(&fakeSource{rows: map[string][]model.Summary{"s": rows}}, 1)
	ctx := context.Background()
	svc.Track(ctx, "s")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.Query(ctx, Request{SessionID: "s", Filter: `host contains "h1" status:2xx path:/items/`, Offset: 500, Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestQueryPerformanceBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget check")
	}
	res := testing.Benchmark(BenchmarkQuery100k)
	if per := time.Duration(res.NsPerOp()); per > 150*time.Millisecond {
		t.Fatalf("query over 100k rows took %v per op (budget 150ms incl. race/CI slack)", per)
	}
}

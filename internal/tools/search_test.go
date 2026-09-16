package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"quecto-harness/internal/search"
)

// fakeSearch is a provider the tests steer: it records what it was asked and
// answers with what the test put in it. With errOnCtxDone it blocks until the
// call's context is cancelled and then answers with the ctx's error.
type fakeSearch struct {
	results      *search.Results
	err          error
	got          search.Query
	calls        int
	errOnCtxDone bool
}

func (f *fakeSearch) Name() search.Name { return search.NameExa }

func (f *fakeSearch) Search(ctx context.Context, q search.Query) (*search.Results, error) {
	f.calls++
	f.got = q
	if f.errOnCtxDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func (f *fakeSearch) Check(ctx context.Context) error { return nil }

func searchRunner(fs *fakeSearch) *Runner {
	return &Runner{OutputLimit: 16384, SearchHits: 10, SearchChars: 4000, Search: fs}
}

func twoHitResults() *search.Results {
	return &search.Results{
		Mode:  search.ModeExcerpts,
		Chars: 4000,
		Hits: []search.Hit{
			{Title: "Working with Errors", URL: "https://go.dev/blog/go1.13-errors",
				Body: "excerpt one"},
			{Title: "Best Practices", URL: "https://example.org/best",
				Body: "excerpt two", Published: "2024-05-24T00:00:00.000Z"},
		},
	}
}

// One block per hit, the url on its own line, and a published line only where
// the hit carries one.
func TestWebSearchRendersItsHits(t *testing.T) {
	fs := &fakeSearch{results: twoHitResults()}
	r := searchRunner(fs)

	res, err := r.WebSearch(context.Background(),
		[]byte(`{"query":"go errors","results":2}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	want := `2 results for "go errors".

1. Working with Errors
https://go.dev/blog/go1.13-errors

excerpt one

2. Best Practices
https://example.org/best
published 2024-05-24T00:00:00.000Z

excerpt two
`
	if res.Output != want {
		t.Errorf("output = %q, want\n%q", res.Output, want)
	}
	if res.Truncated {
		t.Error("a small result was reported truncated")
	}
}

// Nothing found is an answer, and a search that came back with nothing it can
// point at says so in a different sentence.
func TestWebSearchRendersEmptyAndDropped(t *testing.T) {
	fs := &fakeSearch{results: &search.Results{Mode: search.ModeExcerpts}}
	res, err := searchRunner(fs).WebSearch(context.Background(),
		[]byte(`{"query":"nothing"}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	if res.Output != `No results for "nothing".` {
		t.Errorf("output = %q", res.Output)
	}

	fs2 := &fakeSearch{results: &search.Results{Mode: search.ModeExcerpts, Dropped: 3}}
	res2, err := searchRunner(fs2).WebSearch(context.Background(),
		[]byte(`{"query":"nothing"}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	if res2.Output != `No usable results for "nothing": 3 came back with no address.` {
		t.Errorf("output = %q", res2.Output)
	}
}

// A search that mostly dropped still says how much dropped, under the hits.
func TestWebSearchNamesTheDropped(t *testing.T) {
	res := twoHitResults()
	res.Dropped = 1
	fs := &fakeSearch{results: res}

	out, err := searchRunner(fs).WebSearch(context.Background(),
		[]byte(`{"query":"go errors"}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	if !strings.Contains(out.Output, "\n1 further result had no address and was dropped.\n") {
		t.Errorf("output = %q, want the dropped line", out.Output)
	}
}

// The model's ask is held to the configured maximum, and a budget that cannot
// give every result a readable share buys fewer results instead.
func TestWebSearchClampsTheAsk(t *testing.T) {
	fs := &fakeSearch{results: twoHitResults()}
	if _, err := searchRunner(fs).WebSearch(context.Background(),
		[]byte(`{"query":"q","results":50}`)); err != nil {
		t.Fatalf("web search: %v", err)
	}
	if fs.got.Hits != 10 {
		t.Errorf("hits = %d, want the configured maximum", fs.got.Hits)
	}

	// 500 is two results at the per-result floor, not the ten asked for.
	fs2 := &fakeSearch{results: twoHitResults()}
	r := searchRunner(fs2)
	r.SearchChars = 500
	if _, err := r.WebSearch(context.Background(),
		[]byte(`{"query":"q","results":25}`)); err != nil {
		t.Fatalf("web search: %v", err)
	}
	if fs2.got.Hits != 2 {
		t.Errorf("hits = %d, want 2 that the budget can answer", fs2.got.Hits)
	}
	if fs2.got.Chars != 500 {
		t.Errorf("chars = %d, want the whole budget, which the two split", fs2.got.Chars)
	}

	// No ask takes the maximum rather than asking for nothing.
	fs3 := &fakeSearch{results: twoHitResults()}
	if _, err := searchRunner(fs3).WebSearch(context.Background(),
		[]byte(`{"query":"q"}`)); err != nil {
		t.Fatalf("web search: %v", err)
	}
	if fs3.got.Hits != 10 {
		t.Errorf("hits = %d, want the configured maximum", fs3.got.Hits)
	}

	// Full pages are kept to the one or two the prompt names, so a budget
	// that could hold more is spent on depth rather than on more pages.
	fs4 := &fakeSearch{results: twoHitResults()}
	if _, err := searchRunner(fs4).WebSearch(context.Background(),
		[]byte(`{"query":"q","full":true}`)); err != nil {
		t.Fatalf("web search: %v", err)
	}
	if fs4.got.Hits != 2 || !fs4.got.Full {
		t.Errorf("full ask = %d (%v), want two pages", fs4.got.Hits, fs4.got.Full)
	}
}

// The deadline a search runs under is read from the same config the offer is,
// so a timeout saved in settings applies to the tool without a restart. A
// provider that never answers on its own is the only way to see the deadline
// do its work.
func TestWebSearchRunsUnderItsTimeout(t *testing.T) {
	r := searchRunner(&fakeSearch{errOnCtxDone: true})
	r.SearchTimeout = 100 * time.Millisecond

	start := time.Now()
	_, err := r.WebSearch(context.Background(), []byte(`{"query":"q"}`))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline reached rather than the provider's own answer", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the call ran for %v, past its 100 ms deadline", time.Since(start))
	}
}

// Every tool result respects output_limit_bytes, and search is no exception.
func TestWebSearchTruncatesAtTheOutputLimit(t *testing.T) {
	res := twoHitResults()
	res.Hits[0].Body = strings.Repeat("a", 2000)
	fs := &fakeSearch{results: res}
	r := searchRunner(fs)
	r.OutputLimit = 400

	out, err := r.WebSearch(context.Background(), []byte(`{"query":"q"}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	if !out.Truncated {
		t.Error("a result over the limit was not reported truncated")
	}
	if !strings.Contains(out.Output, "bytes omitted") {
		t.Errorf("output = %q, want the omission marker", out.Output)
	}
}

// The three refusals: no query, no provider, and a provider error passed
// through with its own sentence.
func TestWebSearchRefusals(t *testing.T) {
	fs := &fakeSearch{results: twoHitResults()}
	r := searchRunner(fs)

	if _, err := r.WebSearch(context.Background(), []byte(`{"results":1}`)); err == nil ||
		!strings.Contains(err.Error(), "query is required") {
		t.Errorf("no query: err = %v", err)
	}
	if fs.calls != 0 {
		t.Error("a call with no query still reached the provider")
	}

	r.Search = nil
	if _, err := r.WebSearch(context.Background(), []byte(`{"query":"q"}`)); err == nil ||
		!strings.Contains(err.Error(), "web search is not configured") {
		t.Errorf("no provider: err = %v", err)
	}

	// A date in the wrong shape is refused before anything is sent, since the
	// endpoint's 400 would spend the round trip naming the wrong thing.
	fs1 := &fakeSearch{results: twoHitResults()}
	r1 := searchRunner(fs1)
	if _, err := r1.WebSearch(context.Background(), []byte(`{"query":"q","since":"last week"}`)); err == nil ||
		!strings.Contains(err.Error(), "since is not a date") {
		t.Errorf("bad since: err = %v", err)
	}
	if fs1.calls != 0 {
		t.Error("a call with a bad since still reached the provider")
	}

	fs2 := &fakeSearch{err: errors.New("search returned 401 Unauthorized: Invalid API key")}
	if _, err := searchRunner(fs2).WebSearch(context.Background(), []byte(`{"query":"q"}`)); err == nil ||
		!strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("provider error: err = %v", err)
	}
}

// The meta carries what the transcript needs and the sentence does not, and
// never the bodies, which are already in the output and would be kept in the
// session file a second time.
func TestWebSearchMeta(t *testing.T) {
	res := twoHitResults()
	res.CostUSD = 0.007
	fs := &fakeSearch{results: res}

	out, err := searchRunner(fs).WebSearch(context.Background(),
		[]byte(`{"query":"go errors","since":"2024-01-01"}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	meta, err := json.Marshal(out.Meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"provider":"exa"`, `"query":"go errors"`, `"mode":"excerpts"`,
		`"chars":4000`, `"cost_usd":0.007`, `"since":"2024-01-01"`,
		`"url":"https://go.dev/blog/go1.13-errors"`,
		`"published":"2024-05-24T00:00:00.000Z"`,
	} {
		if !strings.Contains(string(meta), want) {
			t.Errorf("meta = %s, want it to carry %s", meta, want)
		}
	}
	if strings.Contains(string(meta), "excerpt one") || strings.Contains(string(meta), "excerpt two") {
		t.Errorf("meta = %s, carries the bodies it should not", meta)
	}

	// Nothing to report is left out, so a search of any age is not recorded
	// as one bounded at a date.
	fs2 := &fakeSearch{results: &search.Results{Mode: search.ModeExcerpts,
		Hits: []search.Hit{{Title: "t", URL: "https://example.org/a"}}}}
	out2, err := searchRunner(fs2).WebSearch(context.Background(), []byte(`{"query":"q"}`))
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	meta2, _ := json.Marshal(out2.Meta)
	for _, absent := range []string{"cost_usd", "since", "dropped"} {
		if strings.Contains(string(meta2), absent) {
			t.Errorf("meta = %s, reports %s with nothing to say", meta2, absent)
		}
	}
}

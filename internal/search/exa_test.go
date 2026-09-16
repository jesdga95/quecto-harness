package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Fixture comes from the endpoint's own answer, read at the test's side so a
// field that arrived once stays the test's ground truth.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

// endpoint stands in for Exa, keeping the request it was sent so what was
// asked can be checked rather than assumed.
type endpoint struct {
	*httptest.Server
	body   string
	status int
	got    apiRequest
	raw    string
	key    string
	path   string
	calls  int
}

func newEndpoint(t *testing.T, body string, status int) *endpoint {
	t.Helper()
	e := &endpoint{body: body, status: status}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.calls++
		e.key, e.path = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		e.raw = string(raw)
		_ = json.Unmarshal(raw, &e.got)
		w.WriteHeader(e.status)
		io.WriteString(w, e.body)
	}))
	t.Cleanup(e.Close)
	return e
}

func (e *endpoint) provider() Provider {
	return &exa{baseURL: e.URL, key: "k", http: e.Server.Client()}
}

// The fixture is the endpoint's own answer to a search for three excerpts:
// the first result carries a published date and the other two do not, and
// nothing the documentation promises (a score, a named resolved search type)
// arrives.
func TestExaDecodesTheCapturedSearch(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-search.json"), http.StatusOK)

	res, err := e.provider().Search(context.Background(),
		Query{Text: "go error handling", Hits: 3, Chars: 1200})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if e.path != "/search" {
		t.Errorf("path = %q, want /search", e.path)
	}
	if e.key != "Bearer k" {
		t.Errorf("authorization = %q, want the key as a bearer token", e.key)
	}
	if e.got.NumResults != 3 || e.got.Type != "auto" {
		t.Errorf("numResults = %d, type = %q, want 3 and auto", e.got.NumResults, e.got.Type)
	}
	// Excerpts by default, and only them: asking for both would be charged
	// for both and return two copies of the page.
	if e.got.Contents.Highlights == nil {
		t.Fatal("a search asked the endpoint for no excerpts")
	}
	if e.got.Contents.Highlights.MaxCharacters != 400 {
		t.Errorf("highlights.maxCharacters = %d, want a third of the 1200 budget",
			e.got.Contents.Highlights.MaxCharacters)
	}
	if e.got.Contents.Text != nil {
		t.Error("a search for excerpts also asked for the full text")
	}
	if res.Mode != ModeExcerpts {
		t.Errorf("mode = %q, want %q", res.Mode, ModeExcerpts)
	}

	if len(res.Hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(res.Hits))
	}
	if res.Hits[0].Title != "Error handling and Go" {
		t.Errorf("title = %q", res.Hits[0].Title)
	}
	// The first result carries a date and the second does not. That is the
	// optionality a fixture written from the documentation would have
	// flattened, and it is why an absent date is not an error.
	if res.Hits[0].Published != "2011-07-12T00:00:00.000Z" {
		t.Errorf("published = %q, want the captured date", res.Hits[0].Published)
	}
	if res.Hits[1].Published != "" {
		t.Errorf("published = %q, want it absent on the second result", res.Hits[1].Published)
	}
	// Excerpts arrive as spans the endpoint itself joins with the same gap it
	// uses inside one span, and the client joins the spans the same way.
	if !strings.Contains(res.Hits[0].Body, "\n...\n") ||
		!strings.Contains(res.Hits[0].Body, "abnormal state") {
		t.Errorf("body = %q, want the excerpt with its gap intact", res.Hits[0].Body)
	}
	// What the search cost, as the endpoint reported it.
	if res.CostUSD != 0.007 {
		t.Errorf("CostUSD = %v, want 0.007 from the response", res.CostUSD)
	}
}

// Full pages are the other mode, for a page whose relevant part cannot be
// named in advance. The request asks for text and not excerpts, and the page
// arrives as its body with no section filter on it, since excluding the
// navigation takes nothing off what is returned and including it returns the
// site menu.
func TestExaAsksForFullPagesOnRequest(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-full.json"), http.StatusOK)

	res, err := e.provider().Search(context.Background(),
		Query{Text: "go error handling", Hits: 1, Chars: 2000, Full: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if e.got.Contents.Text == nil || e.got.Contents.Text.MaxCharacters != 2000 {
		t.Errorf("text contents = %+v, want the whole 2000 budget", e.got.Contents.Text)
	}
	if e.got.Contents.Highlights != nil {
		t.Error("a full-page search also asked for excerpts")
	}
	if strings.Contains(e.raw, "sections") || strings.Contains(e.raw, "Sections") {
		t.Errorf("request = %s, want no section filter on it", e.raw)
	}
	if res.Mode != ModeFull {
		t.Errorf("mode = %q, want %q", res.Mode, ModeFull)
	}
	if len(res.Hits) != 1 || !strings.Contains(res.Hits[0].Body, "essential feature of solid code") {
		t.Errorf("hits = %+v, want the head of the page", res.Hits)
	}
}

// A cap of zero would ask the endpoint for a limit of nothing rather than for
// no limit, so it is left out of the request entirely.
func TestExaOmitsAnUnsetCap(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-search.json"), http.StatusOK)

	if _, err := e.provider().Search(context.Background(),
		Query{Text: "q", Hits: 1}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if e.got.Contents.Highlights == nil {
		t.Fatal("excerpts were not asked for at all")
	}
	if e.got.Contents.Highlights.MaxCharacters != 0 {
		t.Errorf("maxCharacters = %d, want it omitted", e.got.Contents.Highlights.MaxCharacters)
	}
	if !strings.Contains(e.raw, `"highlights":{}`) {
		t.Errorf("request = %s, want the empty cap left out", e.raw)
	}
}

// A share above what the endpoint answers with buys nothing, so it is held to
// the ceiling before it is sent.
func TestExaHoldsTheShareToTheCeiling(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-search.json"), http.StatusOK)

	if _, err := e.provider().Search(context.Background(),
		Query{Text: "q", Hits: 1, Chars: 100000}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := e.got.Contents.Highlights.MaxCharacters; got != maxPageChars {
		t.Errorf("maxCharacters = %d, want %d", got, maxPageChars)
	}
	if got := perPage(100000, 1); got != maxPageChars {
		t.Errorf("perPage(100000, 1) = %d, want %d", got, maxPageChars)
	}
	if got := perPage(0, 10); got != 0 {
		t.Errorf("perPage(0, 10) = %d, want 0 for no limit", got)
	}
	if got := perPage(5, 10); got != 1 {
		t.Errorf("perPage(5, 10) = %d, want the floor of 1", got)
	}
}

// What a thing costs now is a different question from what it cost, and the
// endpoint can answer only the first if it is told which pages count.
func TestExaPassesTheDateThrough(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-search.json"), http.StatusOK)

	if _, err := e.provider().Search(context.Background(),
		Query{Text: "q", Hits: 1, Since: "2024-06-01"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if e.got.StartPublishedDate != "2024-06-01" {
		t.Errorf("startPublishedDate = %q, want the date it was given", e.got.StartPublishedDate)
	}

	// Left empty it is left out, rather than sent as a date that bounds nothing.
	e2 := newEndpoint(t, fixture(t, "exa-search.json"), http.StatusOK)
	if _, err := e2.provider().Search(context.Background(),
		Query{Text: "q", Hits: 1}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Contains(e2.raw, `"startPublishedDate"`) {
		t.Errorf("startPublishedDate was sent even though no date was given")
	}
}

// A hit missing everything but its address is still somewhere the model can
// go, and a hit missing its address is not a hit. Neither takes the search
// down with it.
func TestExaKeepsThinHitsAndDropsAddressless(t *testing.T) {
	body := `{"results":[
	  {"url":"https://example.org/thin"},
	  {"title":"No address","text":"orphaned"},
	  {"url":"   ","title":"Blank address"}
	]}`
	e := newEndpoint(t, body, http.StatusOK)

	res, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 3, Chars: 500})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want the one with an address", len(res.Hits))
	}
	// A hit with no title is titled by its address, so the rendered list
	// never has a blank line where a name should be.
	if res.Hits[0].Title != "https://example.org/thin" {
		t.Errorf("title = %q, want the url standing in for it", res.Hits[0].Title)
	}
	if res.Dropped != 2 {
		t.Errorf("dropped = %d, want 2 counted rather than hidden", res.Dropped)
	}
}

// Excerpts arrive as separate spans, and the endpoint joins fragments inside
// one of them the same way, so a gap between quotes reads like a gap inside
// one. Blank spans are dropped.
func TestExaJoinsSeparateExcerpts(t *testing.T) {
	e := newEndpoint(t, `{"results":[{"url":"https://example.org/a",
	  "highlights":["first span","  ","second span"]}]}`, http.StatusOK)

	res, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := res.Hits[0].Body, "first span\n...\nsecond span"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// An endpoint that says nothing about cost is not a free one, so nothing is
// reported.
func TestExaLeavesAnUnstatedCostUnstated(t *testing.T) {
	e := newEndpoint(t, `{"results":[{"url":"https://example.org/a","highlights":["x"]}]}`,
		http.StatusOK)

	res, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want nothing reported", res.CostUSD)
	}
}

// A request the endpoint does not count as authenticated is answered with a
// price quote. The message has to name both causes, in the order the operator
// checks them, and it must not spill the payment envelope behind the sentence.
func TestExaNamesBothCausesOfA402(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-402.json"), http.StatusPaymentRequired)

	_, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 1})
	if err == nil {
		t.Fatal("a 402 answered as a success")
	}
	msg := err.Error()
	for _, want := range []string{
		"402 Payment Required",
		"no api key reaches the endpoint",
		"check that a key is saved in the search settings",
		"Payment required to access this resource",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q, want it to say %q", msg, want)
		}
	}
	if strings.Contains(msg, "accepts") || strings.Contains(msg, "payTo") || strings.Contains(msg, "0x") {
		t.Errorf("message = %q, spilled the payment envelope", msg)
	}
}

// A key that arrived and was rejected is a 401, and the endpoint's own
// sentence is the one the error carries.
func TestExaCarriesThe401Sentence(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-401.json"), http.StatusUnauthorized)

	_, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 1})
	if err == nil {
		t.Fatal("a 401 answered as a success")
	}
	if !strings.Contains(err.Error(), "401 Unauthorized") ||
		!strings.Contains(err.Error(), "Invalid API key (INVALID_API_KEY)") {
		t.Errorf("message = %q, want the status and the endpoint's sentence", err.Error())
	}
}

// A 200 that is not the expected shape is an error rather than an empty
// result set, since there is nothing to hand the model either way.
func TestExaReportsANonJSONAnswerAsAnError(t *testing.T) {
	e := newEndpoint(t, `<html><body>edge</body></html>`, http.StatusOK)

	_, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 1})
	if err == nil {
		t.Fatal("a non-json 200 answered as a success")
	}
	if !strings.Contains(err.Error(), "not the expected shape") {
		t.Errorf("message = %q", err.Error())
	}
}

// A refusal whose body is not the error shape keeps the start of the body
// instead of inventing a sentence.
func TestExaFallsBackToTheBodySnippet(t *testing.T) {
	e := newEndpoint(t, strings.Repeat("x", 500), http.StatusBadGateway)

	_, err := e.provider().Search(context.Background(), Query{Text: "q", Hits: 1})
	if err == nil {
		t.Fatal("a 502 answered as a success")
	}
	if !strings.HasSuffix(err.Error(), "...") || len(err.Error()) > 300 {
		t.Errorf("message = %q, want the body held to a snippet", err.Error())
	}
}

// Check is the Test button's request: one result, no contents, and the key on
// the wire. An empty key is refused before anything is sent, since the
// endpoint would only answer with a price.
func TestExaSendsTheCheapestRequest(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-check.json"), http.StatusOK)

	if err := e.provider().Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if e.got.NumResults != 1 {
		t.Errorf("numResults = %d, want 1", e.got.NumResults)
	}
	if !strings.Contains(e.raw, `"contents":{}`) {
		t.Errorf("request = %s, want no contents asked for", e.raw)
	}
	if e.key != "Bearer k" {
		t.Errorf("authorization = %q, want the key as a bearer token", e.key)
	}
	if e.calls != 1 {
		t.Errorf("calls = %d, want one", e.calls)
	}
}

func TestExaCheckRefusesAnEmptyKeyWithoutASend(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-check.json"), http.StatusOK)
	p := &exa{baseURL: e.URL, http: e.Server.Client()}

	err := p.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no api key was entered") {
		t.Errorf("err = %v, want the empty key refused", err)
	}
	if e.calls != 0 {
		t.Errorf("calls = %d, want none", e.calls)
	}
}

// A query that is only whitespace is the same as no query.
func TestExaRefusesAnEmptyQuery(t *testing.T) {
	e := newEndpoint(t, fixture(t, "exa-search.json"), http.StatusOK)

	_, err := e.provider().Search(context.Background(), Query{Text: "   ", Hits: 1})
	if err == nil || err.Error() != "query is required" {
		t.Errorf("err = %v, want the query refusal", err)
	}
	if e.calls != 0 {
		t.Errorf("calls = %d, want none", e.calls)
	}
}

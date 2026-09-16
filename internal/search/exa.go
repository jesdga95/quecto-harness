package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// exa is the provider at api.exa.ai.
type exa struct {
	baseURL string
	key     string
	http    *http.Client
}

func (e *exa) Name() Name { return NameExa }

// Search runs one query. An endpoint that answers with something unreadable
// is an error, since there is nothing to hand the model and nothing to retry
// differently.
func (e *exa) Search(ctx context.Context, q Query) (*Results, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, errors.New("query is required")
	}
	// Only the one asked for is sent. Requesting both would be charged for
	// both and would return two copies of the same page to choose between.
	mode := ModeExcerpts
	share := perPage(q.Chars, q.Hits)
	contents := apiContents{Highlights: &apiBounded{MaxCharacters: share}}
	if q.Full {
		mode = ModeFull
		contents = apiContents{Text: &apiBounded{MaxCharacters: share}}
	}

	decoded, err := e.post(ctx, apiRequest{
		Query:              q.Text,
		NumResults:         q.Hits,
		Type:               "auto",
		StartPublishedDate: q.Since,
		Contents:           contents,
	})
	if err != nil {
		return nil, err
	}

	out := &Results{Mode: mode, Chars: q.Chars}
	if decoded.CostDollars != nil {
		out.CostUSD = decoded.CostDollars.Total
	}

	// A hit with no url cannot be cited or fetched again, so it is not a
	// hit. Absent and empty are the same answer here, which is why neither
	// is a distinct case. A hit with no title is titled by its url, so the
	// rendered list never has a blank line where a name should be.
	for _, r := range decoded.Results {
		if strings.TrimSpace(r.URL) == "" {
			out.Dropped++
			continue
		}
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = r.URL
		}
		out.Hits = append(out.Hits, Hit{
			Title:     title,
			URL:       r.URL,
			Body:      pageBody(r),
			Published: strings.TrimSpace(r.PublishedDate),
		})
	}
	return out, nil
}

// Check is the Test button's request: one result, no contents, which the
// endpoint answers like any other search.
func (e *exa) Check(ctx context.Context) error {
	if e.key == "" {
		return errors.New("no api key was entered, so the endpoint would only answer with a price")
	}
	_, err := e.post(ctx, apiRequest{Query: "ping", NumResults: 1, Type: "auto"})
	return err
}

// post sends one request and answers with what the endpoint decoded into, or
// an error that carries the endpoint's own sentence when it refused.
func (e *exa) post(ctx context.Context, req apiRequest) (*apiResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode search request: %w", err)
	}

	url := strings.TrimSuffix(e.baseURL, "/") + "/search"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.key)

	client := e.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", url, err)
	}
	defer resp.Body.Close()

	// Read before checking the status, because the endpoint explains a
	// refusal in the body and a status alone will not say whether the key or
	// the query was the problem.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}
	// A request the endpoint does not count as authenticated is quoted a
	// price rather than refused, since paying per request is an alternative
	// to holding a key. It is worth saying so, because a bare "payment
	// required" reads as an account out of credit when a key that never
	// arrived produces exactly the same answer. A key the endpoint has and
	// rejects is a 401 instead.
	if resp.StatusCode == http.StatusPaymentRequired {
		return nil, fmt.Errorf("search returned %s, which is also what arrives when no api key "+
			"reaches the endpoint: check that a key is saved in the search settings and that "+
			"nothing between here and the endpoint strips the header, then the account's credit (%s)",
			resp.Status, reason(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned %s: %s", resp.Status, reason(raw))
	}

	var decoded apiResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("search response was not the expected shape: %w", err)
	}
	return &decoded, nil
}

// pageBody is what one hit contributes to the answer. Excerpts arrive as
// separate spans and are joined the way the endpoint joins fragments inside
// one of them, so a gap in the middle of a quote and a gap between two quotes
// read alike.
func pageBody(r apiHit) string {
	parts := make([]string, 0, len(r.Highlights))
	for _, h := range r.Highlights {
		if h = strings.TrimSpace(h); h != "" {
			parts = append(parts, h)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n...\n")
	}
	return strings.TrimSpace(r.Text)
}

// perPage divides a search's budget across the pages it asked for. Never
// zero, which would ask for a limit of nothing rather than for no limit, and
// never above the most a single page is asked for.
func perPage(chars, hits int) int {
	if chars <= 0 {
		return 0
	}
	if hits < 1 {
		hits = 1
	}
	return min(max(chars/hits, 1), maxPageChars)
}

// The most characters one page is asked for. Exa sets no ceiling on the ask,
// so this is a budget choice rather than an endpoint limit: asking above it
// spends more of the budget on one page and returns it, so a share above it
// is held to it.
const maxPageChars = 10000

// A ceiling on what one search may hand back before anything has looked at
// it, so a misconfigured endpoint cannot exhaust memory ahead of the limits
// that apply after.
const maxResponseBytes = 8 << 20

type apiRequest struct {
	Query              string      `json:"query"`
	NumResults         int         `json:"numResults,omitempty"`
	Type               string      `json:"type,omitempty"`
	StartPublishedDate string      `json:"startPublishedDate,omitempty"`
	Contents           apiContents `json:"contents"`
}

// Pointers because an absent key means "do not send this at all" while an
// empty object asks for it, and the two are charged differently.
type apiContents struct {
	Text       *apiBounded `json:"text,omitempty"`
	Highlights *apiBounded `json:"highlights,omitempty"`
}

// The cap is per url either way, and omitted rather than sent as zero, since
// zero would ask for a limit of nothing rather than for no limit.
//
// No section filter goes with it. A page arrives as its body already, and
// excluding the navigation, header, footer, sidebar and banner takes nothing
// off it.
type apiBounded struct {
	MaxCharacters int `json:"maxCharacters,omitempty"`
}

type apiResponse struct {
	Results     []apiHit `json:"results"`
	CostDollars *apiCost `json:"costDollars"`
}

// A pointer, so an endpoint that says nothing about cost is not recorded as
// free.
type apiCost struct {
	Total float64 `json:"total"`
}

type apiHit struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Text          string   `json:"text"`
	Highlights    []string `json:"highlights"`
	PublishedDate string   `json:"publishedDate"`
}

// What the endpoint says when it will not answer. Tag is carried because it
// names the condition where the sentence only describes it, and out of credit
// is a condition an operator has to act on rather than retry.
type apiError struct {
	Error string `json:"error"`
	Tag   string `json:"tag"`
}

// reason finds the endpoint's own explanation of a refusal, falling back to
// the start of the body. Worth looking for rather than hoping: a refusal can
// arrive as kilobytes of something else, and the sentence that says what
// happened does not have to be near the front of it.
func reason(b []byte) string {
	var e apiError
	if err := json.Unmarshal(b, &e); err == nil && e.Error != "" {
		if e.Tag != "" {
			return fmt.Sprintf("%s (%s)", e.Error, e.Tag)
		}
		return e.Error
	}
	return snippet(b)
}

// snippet keeps an error message readable when the body behind it is a web
// page.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "no body"
	}
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

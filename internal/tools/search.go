package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/search"
)

// minCharsPerHit is the most a budget gives one result: below it a page comes
// back as a line and a half of whatever the endpoint reached first. A budget
// that cannot give every result this much buys fewer results instead.
const minCharsPerHit = 250

// sincePattern is the only shape the endpoint accepts for a published-date
// bound; anything else is refused here rather than spending the round trip
// on a 400 that names the wrong thing.
var sincePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// WebSearchArgs is what web_search is asked for.
type WebSearchArgs struct {
	Query   string `json:"query"`
	Results int    `json:"results"`
	Full    bool   `json:"full"`
	Since   string `json:"since"`
}

// WebSearch runs one search and answers with what the pages say. Every other
// tool here touches the project; this one leaves the machine, which is why
// its path is not guarded and its call is shown before it is sent.
func (r *Runner) WebSearch(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args WebSearchArgs
	if err := DecodeArgs("web_search", raw, &args); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(args.Query) == "" {
		return Result{}, errors.New("query is required")
	}
	if r.Search == nil {
		return Result{}, errors.New("web search is not configured: no provider is named in settings")
	}
	if since := strings.TrimSpace(args.Since); since != "" && !sincePattern.MatchString(since) {
		return Result{}, errors.New("since is not a date: use the form 2024-06-01")
	}

	hits := args.Results
	if hits <= 0 || hits > r.SearchHits {
		hits = r.SearchHits
	}
	if r.SearchChars > 0 {
		hits = max(min(hits, r.SearchChars/minCharsPerHit), 1)
	}
	// Full pages are the mode that cannot be guessed at, so the prompt keeps
	// them at one or two and the budget that follows is spent on depth.
	if args.Full && hits > 2 {
		hits = 2
	}
	if r.SearchTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.SearchTimeout)
		defer cancel()
	}

	res, err := r.Search.Search(ctx, search.Query{
		Text:  strings.TrimSpace(args.Query),
		Hits:  hits,
		Chars: r.SearchChars,
		Full:  args.Full,
		Since: strings.TrimSpace(args.Since),
	})
	if err != nil {
		return Result{}, err
	}

	out, truncated := Truncate(renderSearch(args.Query, res), r.OutputLimit)
	meta := map[string]any{
		"provider": string(r.Search.Name()),
		"query":    args.Query,
		"mode":     res.Mode,
		"chars":    res.Chars,
		"hits":     searchHits(res.Hits),
	}
	// Reported only when there is something to report, so the transcript does
	// not carry a zero that reads as a fact.
	if res.Dropped > 0 {
		meta["dropped"] = res.Dropped
	}
	if since := strings.TrimSpace(args.Since); since != "" {
		meta["since"] = since
	}
	return Result{Output: out, Truncated: truncated, Bytes: len(out), Meta: meta}, nil
}

// searchHits is what the transcript needs about each result: where it is, not
// what it said. The bodies are already in the output and the meta is kept in
// the session file, so a second copy of them would outlive the turn for
// nothing.
func searchHits(hits []search.Hit) []map[string]any {
	out := make([]map[string]any, len(hits))
	for i, h := range hits {
		out[i] = map[string]any{"title": h.Title, "url": h.URL, "published": h.Published}
	}
	return out
}

// renderSearch writes what the model reads. One block per hit, the url on its
// own line so it survives being quoted back, and nothing that repeats between
// blocks.
func renderSearch(query string, res *search.Results) string {
	if len(res.Hits) == 0 {
		if res.Dropped > 0 {
			return fmt.Sprintf("No usable results for %q: %d came back with no address.",
				query, res.Dropped)
		}
		return fmt.Sprintf("No results for %q.", query)
	}

	var b strings.Builder
	if len(res.Hits) == 1 {
		fmt.Fprintf(&b, "1 result for %q.\n", query)
	} else {
		fmt.Fprintf(&b, "%d results for %q.\n", len(res.Hits), query)
	}
	for i, h := range res.Hits {
		b.WriteString("\n")
		fmt.Fprintf(&b, "%d. %s\n%s\n", i+1, h.Title, h.URL)
		if h.Published != "" {
			fmt.Fprintf(&b, "published %s\n", h.Published)
		}
		if h.Body != "" {
			fmt.Fprintf(&b, "\n%s\n", h.Body)
		}
	}
	if res.Dropped > 0 {
		if res.Dropped == 1 {
			b.WriteString("\n1 further result had no address and was dropped.\n")
		} else {
			fmt.Fprintf(&b, "\n%d further results had no address and were dropped.\n", res.Dropped)
		}
	}
	return b.String()
}

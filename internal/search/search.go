// Package search answers a query with what the pages say about it.
//
// A provider is named in configuration and never detected: two endpoints
// that answer the same shape cannot be told apart, and a wrong guess spends
// money before it is noticed. Exa returns the part of each page that answers
// the query alongside the hit, so finding a page and reading it are one call
// rather than two, and the character limit goes to the endpoint, which
// selects fewer excerpts to fit it rather than returning the page to be cut.
//
// Every field is optional and every shape is a guess. The published date
// arrives on some results and not others, and the documented score field is
// absent. A hit missing its excerpts is still a hit worth returning, and one
// unusable hit is not a failed search.
package search

import (
	"context"
	"errors"
	"net/http"
)

// Name is the service that answers a query. It is named in configuration and
// never detected, for the reason in the package comment.
type Name string

// NameExa is the only provider there is.
const NameExa Name = "exa"

// Names is every provider there is.
var Names = []Name{NameExa}

// ValidName reports whether s is a provider name.
func ValidName(s string) bool {
	for _, n := range Names {
		if n == Name(s) {
			return true
		}
	}
	return false
}

// DefaultURL is the address a provider's account starts at.
func DefaultURL(n Name) string {
	switch n {
	case NameExa:
		return "https://api.exa.ai"
	}
	return ""
}

// What a search asks each page for.
const (
	ModeExcerpts = "excerpts" // the parts that answer the query
	ModeFull     = "full"     // the page from the top
)

// Query is one search. Full asks for the page from the top instead of the
// parts that answer the query, for a task that cannot say in advance which
// part matters.
type Query struct {
	Text string
	Hits int
	Full bool

	// Chars is what the whole search may carry, not what each result may.
	// Each page is asked for a share of it. Zero sets no limit.
	Chars int

	// Since bounds how old a page may be, as an ISO date. Empty asks for any
	// age: a question about what a thing costs now wants this, and one about
	// what happened once does not.
	Since string
}

// Hit is one result. Body is what the page contributes: excerpts joined in
// the endpoint's own style, or the head of the page.
type Hit struct {
	Title     string
	URL       string
	Body      string
	Published string
}

// Results is what one query returned.
type Results struct {
	Hits []Hit
	Mode string

	// Chars is the budget the search was asked to keep to.
	Chars int

	// Dropped counts hits that came back with nothing to identify them.
	// They are counted rather than hidden, since a search that mostly
	// dropped is a different thing from one that mostly found nothing.
	Dropped int
}

// Provider is one service that answers a query.
type Provider interface {
	Name() Name
	Search(ctx context.Context, q Query) (*Results, error)
	// Check is the cheapest request the endpoint accepts, for the Test
	// button. It spends one search like any other.
	Check(ctx context.Context) error
}

// Providers builds the provider a configuration names. The HTTP client is
// shared, so a run of searches costs one connection rather than one a turn.
type Providers struct{ HTTP *http.Client }

// NewProviders builds one shared client. The search deadline is set on the
// call, so a timeout saved in settings applies without a restart.
func NewProviders() *Providers {
	return &Providers{HTTP: &http.Client{}}
}

// For builds the provider the name names, or refuses one it does not know.
// An empty address takes the provider's default.
func (p *Providers) For(n Name, baseURL, apiKey string) (Provider, error) {
	switch n {
	case NameExa:
		if baseURL == "" {
			baseURL = DefaultURL(n)
		}
		return &exa{baseURL: baseURL, key: apiKey, http: p.HTTP}, nil
	}
	return nil, errors.New("no provider named " + string(n))
}

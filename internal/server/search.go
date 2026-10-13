package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/search"
)

// searchJSON is the search block as the settings dialog draws it. The key
// round-trips in plaintext, exactly as the agent key does: a field that cannot
// show what it holds cannot be cleared by emptying it.
type searchJSON struct {
	Provider  string              `json:"provider"`
	MaxHits   int                 `json:"max_hits"`
	MaxChars  int                 `json:"max_chars"`
	TimeoutS  int                 `json:"timeout_s"`
	Providers []searchAccountJSON `json:"providers"`
	// Known is every provider name there is, so the dialog offers a list it
	// did not have to know.
	Known []string `json:"known"`
	// Ready is whether a turn is offered the tool.
	Ready bool `json:"ready"`
}

type searchAccountJSON struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Key  string `json:"key"`
}

func toSearchJSON(s config.Search) searchJSON {
	accounts := make([]searchAccountJSON, 0, len(s.Accounts))
	for _, a := range s.Accounts {
		accounts = append(accounts, searchAccountJSON{Name: string(a.Name), URL: a.URL, Key: a.Key})
	}
	known := make([]string, len(search.Names))
	for i, n := range search.Names {
		known[i] = string(n)
	}
	return searchJSON{
		Provider:  string(s.Provider),
		MaxHits:   s.MaxHits,
		MaxChars:  s.MaxChars,
		TimeoutS:  int(s.Timeout / time.Second),
		Providers: accounts,
		Known:     known,
		Ready:     s.Enabled(),
	}
}

func (s *Server) writeSearch(w http.ResponseWriter, status int, cfg config.Config) {
	writeJSON(w, status, map[string]any{"search": toSearchJSON(cfg.Search)})
}

// searchBody is what PUT /api/search takes: the block the dialog draws, minus
// what the dialog cannot set. A field it leaves out keeps the value it has.
type searchBody struct {
	Provider  *string             `json:"provider"`
	MaxHits   *int                `json:"max_hits"`
	MaxChars  *int                `json:"max_chars"`
	TimeoutS  *int                `json:"timeout_s"`
	Providers []searchAccountBody `json:"providers"`
}

type searchAccountBody struct {
	Name *string `json:"name"`
	URL  *string `json:"url"`
	Key  *string `json:"key"`
}

// check refuses a value the block cannot hold, with the sentence the reader
// sees, before anything is written. Resolve would clamp the same value and
// answer with a note the dialog does not draw.
func (b searchBody) check() error {
	if b.Provider != nil && *b.Provider != "" && !search.ValidName(*b.Provider) {
		return errors.New("Provider \"" + *b.Provider + "\" is not in the list of allowed " +
			"providers: leave it empty to turn search off")
	}
	if v := b.MaxHits; v != nil && (*v < 1 || *v > config.MaxSearchMaxHits) {
		return errors.New("Results is outside 1 to " + strconv.Itoa(config.MaxSearchMaxHits))
	}
	if v := b.MaxChars; v != nil && (*v < config.MinSearchMaxChars || *v > config.MaxSearchMaxChars) {
		return errors.New("Characters is outside " + strconv.Itoa(config.MinSearchMaxChars) +
			" to " + strconv.Itoa(config.MaxSearchMaxChars))
	}
	if v := b.TimeoutS; v != nil && (*v < 1 || *v > config.MaxSearchTimeoutS) {
		return errors.New("Timeout is outside 1 to " + strconv.Itoa(config.MaxSearchTimeoutS) + " seconds")
	}
	for _, a := range b.Providers {
		if a.URL != nil && strings.TrimSpace(*a.URL) != "" {
			if _, ok := config.ValidAddress(strings.TrimSpace(*a.URL)); !ok {
				return errors.New("Address \"" + *a.URL + "\" is not an http or https URL with a host")
			}
		}
	}
	return nil
}

// apply folds the body over the block. The providers list it carries replaces
// the accounts whole: the dialog sends every account, the ones it did not
// touch unchanged, which is what keeps a key when the named provider moves.
func (b searchBody) apply(c *config.Config) {
	s := c.Search
	if b.Provider != nil {
		s.Provider = config.SearchProvider(strings.TrimSpace(*b.Provider))
	}
	if b.MaxHits != nil {
		s.MaxHits = *b.MaxHits
	}
	if b.MaxChars != nil {
		s.MaxChars = *b.MaxChars
	}
	if b.TimeoutS != nil {
		s.Timeout = time.Duration(*b.TimeoutS) * time.Second
	}
	if b.Providers != nil {
		accounts := make([]config.SearchAccount, 0, len(b.Providers))
		for _, a := range b.Providers {
			if a.Name == nil || strings.TrimSpace(*a.Name) == "" {
				continue
			}
			ac := config.SearchAccount{Name: config.SearchProvider(strings.TrimSpace(*a.Name))}
			if a.URL != nil {
				ac.URL = strings.TrimSpace(*a.URL)
			}
			if a.Key != nil {
				ac.Key = *a.Key
			}
			accounts = append(accounts, ac)
		}
		s.Accounts = accounts
	}
	c.Search = s
}

// putSearch saves the whole block and answers with the resolved one.
func (s *Server) putSearch(w http.ResponseWriter, r *http.Request) {
	var body searchBody
	if !readJSON(w, r, &body) {
		return
	}
	if err := body.check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := s.cfg.Update(body.apply)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeSearch(w, http.StatusOK, cfg)
}

// probeSearch answers the Test button. It spends one billed request, so it
// runs only when the button is pressed and is never cached. It answers 200
// even on failure, so the pane draws the error inside the form.
func (s *Server) probeSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string  `json:"provider"`
		URL      *string `json:"url"`
		Key      *string `json:"key"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Provider == "" {
		writeError(w, http.StatusBadRequest, "Pick a provider first")
		return
	}
	if !search.ValidName(body.Provider) {
		writeError(w, http.StatusBadRequest, "Provider \""+body.Provider+"\" is not in the list of allowed providers")
		return
	}
	url := ""
	if body.URL != nil {
		url = strings.TrimSpace(*body.URL)
		if url != "" {
			if _, ok := config.ValidAddress(url); !ok {
				writeError(w, http.StatusBadRequest, "Address \""+url+"\" is not an http or https URL with a host")
				return
			}
		}
	}
	key := ""
	if body.Key != nil {
		key = *body.Key
	}

	p, err := s.searches.For(search.Name(body.Provider), url, key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	timeout := s.cfg.Config().Search.Timeout
	if timeout <= 0 {
		timeout = config.DefaultSearchTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if err := p.Check(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

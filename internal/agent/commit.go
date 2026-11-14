package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// draftTimeout and draftTokens bound the request for a commit message, which
// leaves room for a pass of thinking ahead of a summary and a short body.
// draftChars is the most of the diff the request carries.
const (
	draftTimeout = 2 * time.Minute
	draftTokens  = 2048
	draftChars   = 24000
)

// ErrNotReady is returned for a request that needs the session's model before
// any message has made it ready.
var ErrNotReady = errors.New("The model is not loaded yet: send a message in this session first")

// ErrNoDraft is returned when the model answered with no text.
var ErrNoDraft = errors.New("The model did not write a message: try again")

// DraftCommit asks the session's model for a commit message for a diff. The
// request opens with the session's own conversation, so the model server
// keeps that prefix cached and the next turn does not read it again. It loads
// nothing: a session whose model has not been made ready is refused.
func (e *Engine) DraftCommit(ctx context.Context, id, diff string, recent []string) (summary, body string, err error) {
	s, err := e.Get(id)
	if err != nil {
		return "", "", err
	}
	a, err := e.point(s)
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	ready := s.prepared
	s.mu.Unlock()
	if !ready {
		return "", "", ErrNotReady
	}

	cut := len(diff) > draftChars
	if cut {
		diff = strings.ToValidUTF8(diff[:draftChars], "")
	}
	b := e.briefFor(ctx, s, e.cfg.Config())
	msgs := append(e.messages(s, a, b), llm.Message{Role: "user", Content: prompts.CommitMessage(diff, recent, cut)})

	ctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()
	res, err := e.backends.For(a).Chat(ctx, llm.ChatRequest{
		Model: a.Model, Messages: msgs, Tools: tools.Schemas(b.offer), Effort: a.Effort,
		PreserveReasoning: a.PreserveReasoning,
		Temperature:       a.Temperature, TopP: a.TopP, TopK: a.TopK,
		RepeatPenalty: a.RepeatPenalty, Seed: a.Seed, Caps: e.turnCaps(s),
		MaxTokens: draftTokens, Session: s.ID,
	}, nil)
	if err != nil {
		return "", "", err
	}
	summary, body = splitMessage(res.Content)
	if summary == "" {
		return "", "", ErrNoDraft
	}
	return summary, body, nil
}

// splitMessage takes a reply apart into its first line and the rest, dropping
// the fence or quotes a model wraps a message in.
func splitMessage(reply string) (summary, body string) {
	text := strings.TrimSpace(reply)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	summary = strings.Trim(strings.TrimSpace(first), "\"'`*#")
	return strings.TrimSpace(summary), strings.TrimSpace(rest)
}

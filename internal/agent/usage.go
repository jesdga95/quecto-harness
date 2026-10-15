package agent

import (
	"time"

	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/store"
)

// turnStart is the index of the message this turn answers: the newest user
// entry that is neither a message picked up off the queue part way through a
// turn nor the harness's own nudge to carry on with a plan. Everything after it
// was written by the turn.
func turnStart(entries []store.Entry) int {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind != "user" || e.Meta["steer"] == true || e.Meta["continue"] == true {
			continue
		}
		return i
	}
	return 0
}

// turnEnd is the entry a turn's totals belong on: the last reply it wrote, or
// -1 where it wrote none.
func turnEnd(entries []store.Entry, from int) int {
	for i := len(entries) - 1; i > from; i-- {
		if entries[i].Kind == "assistant" {
			return i
		}
	}
	return -1
}

// turnUsage is what a turn spent, summed over the calls its entries recorded.
type turnUsage struct {
	calls int
	// unrecorded counts the calls missing from the sums below, because their
	// server named no counts, and stopped the ones among them that this
	// harness cut short. stopped is never larger than unrecorded.
	unrecorded int
	stopped    int
	prompt     int
	completion int
	cached     int
	cachedSeen bool
	// window is the prompt of the turn's last measured call, the context as it
	// stood then. A summary's prompt counted the messages it replaced, so it
	// is not the context.
	window  int
	firstMs int64
	genMs   int64
	totalMs int64
	// perSecond is the rate a server named for itself, read only where no
	// generating time was measured.
	perSecond float64
	// each holds every call of the turn, in the order they were made.
	each []store.CallUsage
}

// turnUsageOf sums the records the entries after from carry. A call recorded
// on any of them counts, whichever entry it produced: a reply, a summary, or
// the note saying an attempt was sent again.
func turnUsageOf(entries []store.Entry, from int) turnUsage {
	var u turnUsage
	if from+1 > len(entries) {
		return u
	}
	for _, e := range entries[from+1:] {
		c := e.Usage
		if c == nil {
			continue
		}
		u.calls++
		u.each = append(u.each, *c)
		if !c.Reported {
			u.unrecorded++
			// stopped names the uncounted calls this harness cut short, so the
			// two figures describe the same set: all of them are missing from
			// the totals, and some of them were cut. A cut call that reported
			// its usage is in the totals and belongs to neither figure.
			if c.Stopped {
				u.stopped++
			}
			continue
		}
		u.prompt += c.Prompt
		u.completion += c.Completion
		if c.Cached != nil {
			u.cached += *c.Cached
			u.cachedSeen = true
		}
		u.firstMs += c.FirstMs
		u.genMs += c.GenMs
		u.totalMs += c.TotalMs
		if c.PerSecond > 0 {
			u.perSecond = c.PerSecond
		}
		if !c.Compaction {
			u.window = c.Prompt
		}
	}
	return u
}

// fields renders the turn's totals for the transcript entry that ends it and
// for the done event that ends it in the browser. wall and tool come from the
// turn's own clock. nil says the turn recorded no call, so nothing is written
// for it.
func (u turnUsage) fields(wall, tool time.Duration, agent, model string) map[string]any {
	if u.calls == 0 {
		return nil
	}
	rate := 0.0
	if secs := float64(u.genMs) / 1000; secs > 0 {
		rate = float64(u.completion) / secs
	} else if u.perSecond > 0 {
		rate = u.perSecond
	}
	out := map[string]any{
		"tokens":       u.completion,
		"input_tokens": u.prompt,
		"ms":           wall.Milliseconds(),
		"llm_ms":       u.totalMs,
		"gen_ms":       u.genMs,
		"first_ms":     u.firstMs,
		"tool_ms":      tool.Milliseconds(),
		"tok_per_s":    rate,
		"requests":     u.calls,
		"per_request":  u.rows(),
	}
	if u.window > 0 {
		out["window_tokens"] = u.window
	}
	if u.cachedSeen {
		out["cached_tokens"] = u.cached
	}
	if u.unrecorded > 0 {
		out["unrecorded"] = u.unrecorded
	}
	if u.stopped > 0 {
		out["stopped"] = u.stopped
	}
	if agent != "" {
		out["agent"] = agent
	}
	if model != "" {
		out["model"] = model
	}
	return out
}

// rows renders each call of the turn, in the order they were made. A count the
// server did not report is left out rather than written as zero, and so is a
// summary's prompt, which counted the messages it replaced.
func (u turnUsage) rows() []map[string]any {
	out := make([]map[string]any, 0, len(u.each))
	for _, c := range u.each {
		row := map[string]any{"first_ms": c.FirstMs, "gen_ms": c.GenMs}
		if !c.Reported {
			out = append(out, row)
			continue
		}
		row["tokens"] = c.Completion
		if !c.Compaction && c.Prompt > 0 {
			row["window_tokens"] = c.Prompt
		}
		out = append(out, row)
	}
	return out
}

// callUsage is what one request measured and reported, read to be kept on the
// entry it produced. sent is when the request went out and first when it began
// to produce, which is the zero time for a request that streamed nothing.
// stopped is set where this harness ended the call rather than the model.
func callUsage(res llm.Result, sent, first time.Time, tools int, stopped bool) *store.CallUsage {
	u := &store.CallUsage{Stopped: stopped, TotalMs: time.Since(sent).Milliseconds(), ToolTokens: tools}
	if !first.IsZero() {
		u.FirstMs = first.Sub(sent).Milliseconds()
		u.GenMs = time.Since(first).Milliseconds()
	}
	if r := res.Usage; r != nil {
		u.Reported = true
		u.Prompt = r.PromptTokens
		u.Completion = r.CompletionTokens
		u.Cached = r.CachedTokens
	}
	if t := res.Timings; t != nil && t.PredictedPerSecond > 0 {
		u.PerSecond = t.PredictedPerSecond
	}
	return u
}

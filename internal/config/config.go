package config

import (
	"crypto/rand"
	"encoding/base32"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quecto-harness/internal/llm"
)

// File mirrors config.json. A nil field is absent and takes the default,
// which is why every scalar is a pointer.
type File struct {
	Listen *string `json:"listen,omitempty"`
	// Agents and Projects have no omitempty: a saved file names every field, so
	// a reader never has to know which default was in force.
	Agents           []FileAgent `json:"agents"`
	Agent            *string     `json:"agent,omitempty"`
	Permissions      *string     `json:"permissions,omitempty"`
	CompactAt        *float64    `json:"compact_at,omitempty"`
	Shell            *string     `json:"shell,omitempty"`
	CommandTimeoutS  *int        `json:"command_timeout_s,omitempty"`
	OutputLimitBytes *int        `json:"output_limit_bytes,omitempty"`
	MaxToolCalls     *int        `json:"max_tool_calls,omitempty"`
	Projects         []string    `json:"projects"`
}

// FileAgent mirrors one entry of the agents array.
type FileAgent struct {
	ID                *string  `json:"id,omitempty"`
	Name              *string  `json:"name,omitempty"`
	URL               *string  `json:"url,omitempty"`
	Key               *string  `json:"key,omitempty"`
	Protocol          *string  `json:"protocol,omitempty"`
	Model             *string  `json:"model,omitempty"`
	Effort            *string  `json:"effort,omitempty"`
	PreserveReasoning *bool    `json:"preserve_reasoning,omitempty"`
	Temperature       *float64 `json:"temperature,omitempty"`
	TopP              *float64 `json:"top_p,omitempty"`
	TopK              *int     `json:"top_k,omitempty"`
	RepeatPenalty     *float64 `json:"repeat_penalty,omitempty"`
	MaxTokens         *int     `json:"max_tokens,omitempty"`
	Seed              *int     `json:"seed,omitempty"`
	Instructions      *string  `json:"instructions,omitempty"`
}

// Config is the resolved configuration. Every field is usable as it stands.
type Config struct {
	Listen string
	Agents []Agent
	// AgentID names the active agent. Empty when none is chosen, which is the
	// state the setup screen answers.
	AgentID          string
	Mode             Mode
	CompactAt        float64
	Shell            string
	CommandTimeout   time.Duration
	OutputLimitBytes int
	// MaxToolCalls bounds the tool calls one turn may make. Zero is no bound,
	// which leaves the Stop button as the only thing that ends a runaway turn.
	MaxToolCalls int
	Projects     []string
}

// Agent is one model server the harness can answer with: where it is, what key
// opens it, which model, and how that model is asked. One is active at a time
// and every session uses it.
type Agent struct {
	ID   string
	Name string
	URL  string
	Key  string
	// Protocol is what the address speaks. It is named rather than found:
	// two of the three answer the same model list at the same address.
	Protocol          Protocol
	Model             string
	Effort            Effort
	PreserveReasoning bool
	// The sampling fields are sent with every request that generates. Nil is
	// left out of the body, so the model server's own value stands. TopK and
	// RepeatPenalty reach a llama.cpp server alone, since neither OpenAI
	// protocol defines them.
	Temperature   *float64
	TopP          *float64
	TopK          *int
	RepeatPenalty *float64
	// MaxTokens caps one reply. Seed makes a reply repeatable.
	MaxTokens *int
	Seed      *int
	// Instructions follow the built-in system prompt in the same message.
	Instructions string
}

// Agent is the active agent. The second result is false when none is chosen.
func (c Config) Agent() (Agent, bool) { return c.AgentByID(c.AgentID) }

// AgentByID finds one agent by id, so callers never write the loop.
func (c Config) AgentByID(id string) (Agent, bool) {
	if id == "" {
		return Agent{}, false
	}
	for _, a := range c.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// DefaultAgent is what a new agent starts as, before anything it was created
// with is folded in.
func DefaultAgent() Agent {
	return Agent{Protocol: ProtocolLlama, Effort: EffortMedium, PreserveReasoning: true}
}

// cloneAgents copies the list and the values behind its pointers, so a
// resolved config and the one it came from never share a value.
func cloneAgents(in []Agent) []Agent {
	if in == nil {
		return nil
	}
	out := make([]Agent, len(in))
	for i, a := range in {
		a.Temperature, a.TopP = clonef(a.Temperature), clonef(a.TopP)
		a.RepeatPenalty = clonef(a.RepeatPenalty)
		a.TopK, a.MaxTokens, a.Seed = clonei(a.TopK), clonei(a.MaxTokens), clonei(a.Seed)
		out[i] = a
	}
	return out
}

// Effort is defined in internal/llm, which is what turns it into request
// fields. The alias keeps callers of this package to one import.
// Protocol is the wire protocol an agent's address speaks.
type Protocol = llm.Protocol

const (
	ProtocolLlama     = llm.ProtocolLlama
	ProtocolChat      = llm.ProtocolChat
	ProtocolResponses = llm.ProtocolResponses
)

// ProtocolNames lists what the protocol field accepts, for the note a
// rejected value produces.
const ProtocolNames = "llamacpp, chat or responses"

type Effort = llm.Effort

const (
	EffortAuto   = llm.EffortAuto
	EffortOff    = llm.EffortOff
	EffortLow    = llm.EffortLow
	EffortMedium = llm.EffortMedium
	EffortHigh   = llm.EffortHigh
)

// EffortNames lists what the effort field accepts, for the note a rejected
// value produces.
const EffortNames = "auto, off, low, medium or high"

// Mode is the permission policy. One field rather than a pair of booleans,
// which would have a fourth state with no meaning.
type Mode string

const (
	// ModeAsk shows a card for every write and every command.
	ModeAsk Mode = "ask"
	// ModeAllowlist runs writes and allowlisted command prefixes on their own.
	ModeAllowlist Mode = "allowlist"
	// ModeFull runs every tool call. Nothing is asked and nothing is checked.
	ModeFull Mode = "full"
)

// ValidMode reports whether s is one of the three policies.
func ValidMode(s string) bool {
	switch Mode(s) {
	case ModeAsk, ModeAllowlist, ModeFull:
		return true
	}
	return false
}

func Defaults() Config {
	return Config{
		Listen:           "127.0.0.1:7331",
		Agents:           nil,
		AgentID:          "",
		Mode:             ModeAsk,
		CompactAt:        0.8,
		Shell:            defaultShell(),
		CommandTimeout:   120 * time.Second,
		OutputLimitBytes: 16384,
		MaxToolCalls:     50,
		Projects:         nil,
	}
}

// defaultShell prefers $SHELL when it is an absolute path to an executable,
// since that is the shell the user's aliases and PATH were written for.
func defaultShell() string {
	sh := os.Getenv("SHELL")
	if filepath.IsAbs(sh) {
		if info, err := os.Stat(sh); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return sh
		}
	}
	return "/bin/sh"
}

// Resolve folds the file over the defaults. An out-of-range value falls back
// to the default and produces one note, so a hand-edited file does not fail
// startup.
func (f File) Resolve(d Config) (Config, []string) {
	c := d
	var notes []string
	note := func(s string) { notes = append(notes, s) }

	if f.Listen != nil {
		if v, ok := validListen(*f.Listen); ok {
			c.Listen = v
		} else {
			note("listen " + strconv.Quote(*f.Listen) + " is not a host:port with a port from 1 to 65535, using " + d.Listen)
		}
	}
	c.Agents = resolveAgents(f.Agents, note)
	if f.Agent != nil {
		c.AgentID = *f.Agent
	}
	if _, ok := c.Agent(); !ok && c.AgentID != "" {
		note("agent " + strconv.Quote(c.AgentID) + " is not in the agent list, no agent is selected")
		c.AgentID = ""
	}
	if f.Permissions != nil {
		if ValidMode(*f.Permissions) {
			c.Mode = Mode(*f.Permissions)
		} else {
			note("permissions " + strconv.Quote(*f.Permissions) + " is not ask, allowlist or full, using ask")
		}
	}
	if f.CompactAt != nil {
		if *f.CompactAt >= 0.05 && *f.CompactAt <= 0.95 {
			c.CompactAt = *f.CompactAt
		} else {
			note("compact_at " + strconv.FormatFloat(*f.CompactAt, 'g', -1, 64) + " is outside 0.05 to 0.95, using 0.8")
		}
	}
	if f.Shell != nil {
		if filepath.IsAbs(*f.Shell) {
			c.Shell = *f.Shell
		} else {
			note("shell " + strconv.Quote(*f.Shell) + " is not an absolute path, using /bin/sh")
			c.Shell = "/bin/sh"
		}
	}
	if f.CommandTimeoutS != nil {
		if *f.CommandTimeoutS >= 1 && *f.CommandTimeoutS <= 3600 {
			c.CommandTimeout = time.Duration(*f.CommandTimeoutS) * time.Second
		} else {
			note("command_timeout_s " + strconv.Itoa(*f.CommandTimeoutS) + " is outside 1 to 3600, using 120")
		}
	}
	if f.OutputLimitBytes != nil {
		if *f.OutputLimitBytes >= 1024 && *f.OutputLimitBytes <= 4194304 {
			c.OutputLimitBytes = *f.OutputLimitBytes
		} else {
			note("output_limit_bytes " + strconv.Itoa(*f.OutputLimitBytes) + " is outside 1024 to 4194304, using 16384")
		}
	}
	if f.MaxToolCalls != nil {
		if *f.MaxToolCalls >= 0 && *f.MaxToolCalls <= 1000 {
			c.MaxToolCalls = *f.MaxToolCalls
		} else {
			note("max_tool_calls " + strconv.Itoa(*f.MaxToolCalls) + " is outside 0 to 1000, using 50")
		}
	}
	if f.Projects != nil {
		seen := map[string]bool{}
		var keep []string
		for _, p := range f.Projects {
			if !filepath.IsAbs(p) {
				note("project " + strconv.Quote(p) + " is not an absolute path, dropped")
				continue
			}
			clean := filepath.Clean(p)
			if seen[clean] {
				continue
			}
			seen[clean] = true
			keep = append(keep, clean)
		}
		c.Projects = keep
	}
	return c, notes
}

// File is the inverse of Resolve: every field written, so a saved file is a
// complete document and a later reader never guesses which defaults applied.
// A sampling value left to the server is the one thing not written, since
// there is no number to write.
func (c Config) File() File {
	listen, mode, compact := c.Listen, string(c.Mode), c.CompactAt
	shell := c.Shell
	timeout, limit := int(c.CommandTimeout/time.Second), c.OutputLimitBytes
	maxCalls, agent := c.MaxToolCalls, c.AgentID
	projects := c.Projects
	if projects == nil {
		projects = []string{}
	}
	agents := make([]FileAgent, 0, len(c.Agents))
	for _, a := range c.Agents {
		id, name, url, key := a.ID, a.Name, a.URL, a.Key
		protocol, model, effort := string(a.Protocol), a.Model, string(a.Effort)
		preserve, instructions := a.PreserveReasoning, a.Instructions
		agents = append(agents, FileAgent{
			ID:                &id,
			Name:              &name,
			URL:               &url,
			Key:               &key,
			Protocol:          &protocol,
			Model:             &model,
			Effort:            &effort,
			PreserveReasoning: &preserve,
			Temperature:       clonef(a.Temperature),
			TopP:              clonef(a.TopP),
			TopK:              clonei(a.TopK),
			RepeatPenalty:     clonef(a.RepeatPenalty),
			MaxTokens:         clonei(a.MaxTokens),
			Seed:              clonei(a.Seed),
			Instructions:      &instructions,
		})
	}
	return File{
		Listen:           &listen,
		Agents:           agents,
		Agent:            &agent,
		Permissions:      &mode,
		CompactAt:        &compact,
		Shell:            &shell,
		CommandTimeoutS:  &timeout,
		OutputLimitBytes: &limit,
		MaxToolCalls:     &maxCalls,
		Projects:         projects,
	}
}

// resolveAgents folds each file entry over the agent defaults. A bad value
// falls back and produces one note; only a repeated id costs the entry, since
// two agents that cannot be told apart cannot both be selected.
func resolveAgents(in []FileAgent, note func(string)) []Agent {
	if in == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]Agent, 0, len(in))
	for _, f := range in {
		a := DefaultAgent()
		if f.Name != nil {
			a.Name = strings.TrimSpace(*f.Name)
		}
		if f.ID != nil {
			a.ID = strings.TrimSpace(*f.ID)
		}
		if a.ID == "" {
			a.ID = NewAgentID()
			note("agent " + strconv.Quote(a.Name) + " had no id, using " + a.ID)
		}
		if seen[a.ID] {
			note("agent " + strconv.Quote(a.Name) + " repeats id " + a.ID + ", dropped")
			continue
		}
		seen[a.ID] = true
		if a.Name == "" {
			a.Name = "Agent"
			note("agent " + a.ID + " has no name, using " + strconv.Quote(a.Name))
		}
		// A blank address is an agent still being filled in, not a bad value.
		if f.URL != nil && strings.TrimSpace(*f.URL) != "" {
			if v, ok := ValidAddress(*f.URL); ok {
				a.URL = v
			} else {
				note("agent " + strconv.Quote(a.Name) + " address " + strconv.Quote(*f.URL) + " is not an http or https URL with a host, clearing it")
			}
		}
		if f.Key != nil {
			a.Key = *f.Key
		}
		if f.Protocol != nil {
			if llm.ValidProtocol(*f.Protocol) {
				a.Protocol = Protocol(*f.Protocol)
			} else {
				note("agent " + strconv.Quote(a.Name) + " protocol " + strconv.Quote(*f.Protocol) + " is not " + ProtocolNames + ", using llamacpp")
			}
		}
		if f.Model != nil {
			a.Model = *f.Model
		}
		if f.Effort != nil {
			if llm.ValidEffort(*f.Effort) {
				a.Effort = Effort(*f.Effort)
			} else {
				note("agent " + strconv.Quote(a.Name) + " effort " + strconv.Quote(*f.Effort) + " is not " + EffortNames + ", using medium")
			}
		}
		if f.PreserveReasoning != nil {
			a.PreserveReasoning = *f.PreserveReasoning
		}
		if f.Temperature != nil {
			if *f.Temperature >= 0 && *f.Temperature <= 2 {
				a.Temperature = clonef(f.Temperature)
			} else {
				note("agent " + strconv.Quote(a.Name) + " temperature " + strconv.FormatFloat(*f.Temperature, 'g', -1, 64) + " is outside 0 to 2, leaving it to the model server")
			}
		}
		if f.TopP != nil {
			if *f.TopP >= 0 && *f.TopP <= 1 {
				a.TopP = clonef(f.TopP)
			} else {
				note("agent " + strconv.Quote(a.Name) + " top_p " + strconv.FormatFloat(*f.TopP, 'g', -1, 64) + " is outside 0 to 1, leaving it to the model server")
			}
		}
		if f.TopK != nil {
			if *f.TopK >= 0 && *f.TopK <= 1000 {
				a.TopK = clonei(f.TopK)
			} else {
				note("agent " + strconv.Quote(a.Name) + " top_k " + strconv.Itoa(*f.TopK) + " is outside 0 to 1000, leaving it to the model server")
			}
		}
		if f.RepeatPenalty != nil {
			if *f.RepeatPenalty >= 0 && *f.RepeatPenalty <= 2 {
				a.RepeatPenalty = clonef(f.RepeatPenalty)
			} else {
				note("agent " + strconv.Quote(a.Name) + " repeat_penalty " + strconv.FormatFloat(*f.RepeatPenalty, 'g', -1, 64) + " is outside 0 to 2, leaving it to the model server")
			}
		}
		if f.MaxTokens != nil {
			if *f.MaxTokens >= 1 && *f.MaxTokens <= 1000000 {
				a.MaxTokens = clonei(f.MaxTokens)
			} else {
				note("agent " + strconv.Quote(a.Name) + " max_tokens " + strconv.Itoa(*f.MaxTokens) + " is outside 1 to 1000000, leaving it to the model server")
			}
		}
		if f.Seed != nil {
			a.Seed = clonei(f.Seed)
		}
		if f.Instructions != nil {
			a.Instructions = strings.TrimSpace(*f.Instructions)
		}
		out = append(out, a)
	}
	return out
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewAgentID mints an id for an agent. It is minted rather than taken from the
// name so that a rename moves nothing: not the selection, not a session, not
// a pooled client.
func NewAgentID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return "agt_" + idEncoding.EncodeToString(b)[:16]
}

// clonei copies an int behind a pointer, for the same reason clonef does.
func clonei(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// clonef copies a float behind a pointer, so a resolved config and the file
// it came from never share the value.
func clonef(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func validListen(s string) (string, bool) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	_ = host
	return s, true
}

// ValidAddress reports whether s is an address the harness can reach, and
// answers with it trimmed of the trailing slash a client would drop anyway.
func ValidAddress(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return strings.TrimRight(s, "/"), true
}

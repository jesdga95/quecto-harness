package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quecto-harness/internal/llama"
)

// File mirrors config.json. A nil field is absent and takes the default,
// which is why every scalar is a pointer.
type File struct {
	Listen            *string  `json:"listen,omitempty"`
	Server            *Server  `json:"server,omitempty"`
	Model             *string  `json:"model,omitempty"`
	Effort            *string  `json:"effort,omitempty"`
	PreserveReasoning *bool    `json:"preserve_reasoning,omitempty"`
	Permissions       *string  `json:"permissions,omitempty"`
	CompactAt         *float64 `json:"compact_at,omitempty"`
	Shell             *string  `json:"shell,omitempty"`
	CommandTimeoutS   *int     `json:"command_timeout_s,omitempty"`
	OutputLimitBytes  *int     `json:"output_limit_bytes,omitempty"`
	// Projects has no omitempty: a saved file names every field, so a reader
	// never has to know which default was in force.
	Projects []string `json:"projects"`
}

type Server struct {
	URL   *string `json:"url,omitempty"`
	Token *string `json:"token,omitempty"`
}

// Config is the resolved configuration. Every field is usable as it stands.
type Config struct {
	Listen            string
	ServerURL         string
	ServerToken       string
	Model             string
	Effort            Effort
	PreserveReasoning bool
	Mode              Mode
	CompactAt         float64
	Shell             string
	CommandTimeout    time.Duration
	OutputLimitBytes  int
	Projects          []string
}

// Effort is defined in internal/llama, which is what turns it into request
// fields. The alias keeps callers of this package to one import.
type Effort = llama.Effort

const (
	EffortAuto    = llama.EffortAuto
	EffortOff     = llama.EffortOff
	EffortMinimal = llama.EffortMinimal
	EffortLow     = llama.EffortLow
	EffortMedium  = llama.EffortMedium
	EffortHigh    = llama.EffortHigh
	EffortXHigh   = llama.EffortXHigh
	EffortMax     = llama.EffortMax
)

// EffortNames lists what the effort field accepts, for the note a rejected
// value produces.
const EffortNames = "auto, off, minimal, low, medium, high, xhigh or max"

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
		Listen:            "127.0.0.1:7331",
		ServerURL:         "",
		ServerToken:       "",
		Model:             "",
		Effort:            EffortMedium,
		PreserveReasoning: true,
		Mode:              ModeAsk,
		CompactAt:         0.8,
		Shell:             defaultShell(),
		CommandTimeout:    120 * time.Second,
		OutputLimitBytes:  16384,
		Projects:          nil,
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
	if f.Server != nil {
		if f.Server.URL != nil {
			if v, ok := validURL(*f.Server.URL); ok {
				c.ServerURL = v
			} else if strings.TrimSpace(*f.Server.URL) == "" {
				// Empty means no server has been set yet, which the setup
				// screen answers. It is not a bad value to report.
				c.ServerURL = ""
			} else {
				note("server.url " + strconv.Quote(*f.Server.URL) + " is not an http or https URL with a host, using " + d.ServerURL)
			}
		}
		if f.Server.Token != nil {
			c.ServerToken = *f.Server.Token
		}
	}
	if f.Model != nil {
		c.Model = *f.Model
	}
	if f.Effort != nil {
		if llama.ValidEffort(*f.Effort) {
			c.Effort = Effort(*f.Effort)
		} else {
			note("effort " + strconv.Quote(*f.Effort) + " is not " + EffortNames + ", using medium")
		}
	}
	if f.PreserveReasoning != nil {
		c.PreserveReasoning = *f.PreserveReasoning
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
func (c Config) File() File {
	listen, model, effort := c.Listen, c.Model, string(c.Effort)
	shell, mode, compact := c.Shell, string(c.Mode), c.CompactAt
	preserve := c.PreserveReasoning
	timeout, limit := int(c.CommandTimeout/time.Second), c.OutputLimitBytes
	surl, stok := c.ServerURL, c.ServerToken
	projects := c.Projects
	if projects == nil {
		projects = []string{}
	}
	return File{
		Listen:            &listen,
		Server:            &Server{URL: &surl, Token: &stok},
		Model:             &model,
		Effort:            &effort,
		PreserveReasoning: &preserve,
		Permissions:       &mode,
		CompactAt:         &compact,
		Shell:             &shell,
		CommandTimeoutS:   &timeout,
		OutputLimitBytes:  &limit,
		Projects:          projects,
	}
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

func validURL(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return strings.TrimRight(s, "/"), true
}

# quecto harness: implementation spec

Status: reviewed, ready to implement. Every decision in the approved design plan is taken as
settled. This document expands those decisions into package boundaries, type definitions,
wire shapes, state machines and tests. The gaps and contradictions raised at review are
answered in section 14, and those answers are already folded into the sections above.

Captured fixtures used throughout: `docs/fixtures/props-router.json`,
`docs/fixtures/v1-models.json`, `docs/fixtures/props-model.json`. All three were read from
the live router at `http://192.168.2.205:8080` (build `b10812-8b4b3558f`). The model-scoped
capture used `?autoload=false`. `docs/fixtures/openai-models.json` was read from a live
OpenAI-compatible endpoint, which has no `/props` at all. Model names in the fixtures are
renamed; the shapes are verbatim.

---

## 1. Overview and process model

### 1.1 One binary

`quecto-harness` is a single Go binary with no runtime assets on disk. The Svelte build
output is compiled in with `//go:embed all:dist` inside `internal/web`. The binary is built
with `CGO_ENABLED=0`, so it is one file that can be copied between machines of the same
GOOS and GOARCH.

Argument shape:

```
quecto-harness [flags]                     serve
quecto-harness open                        open the browser at the saved URL
quecto-harness service install [flags]     write and load the OS unit
quecto-harness service uninstall           unload and remove the OS unit
quecto-harness service status              report unit presence, run state, URL
quecto-harness version                     print version, commit, Go version
```

Flags accepted by the serve form and by `service install` (which records them in the unit):

| flag | type | default | meaning |
|------|------|---------|---------|
| `--listen` | string | from config, else `127.0.0.1:7331` | host:port to bind |
| `--no-open` | bool | false | do not open a browser after binding |
| `--home` | string | `$QUECTO_HARNESS_HOME`, else `~/.quecto-harness` | state directory |

`--listen` on the command line overrides `config.json` for this process only and is not
written back. Subcommand dispatch happens before `flag.Parse` on the serve flag set, by
inspecting `os.Args[1]` against the fixed set `open`, `service`, `version`. Anything
starting with `-` falls through to the serve form.

### 1.2 Startup sequence

`cmd/quecto-harness/main.go` runs the following in order. Any step that returns an error
prints `quecto-harness: <what failed>: <what to do>` to stderr and exits 1.

1. Dispatch the subcommand. `service` and `open` return before any of the rest.
2. `config.DiscoverPaths()` resolves the home directory and derived paths. It does not
   touch the filesystem.
3. `paths.EnsureDirs()` creates `~/.quecto-harness`, `sessions/` and `log/` with mode
   0700. Existing directories are left alone; their mode is not changed.
4. `config.Open(paths)` reads `config.json` and `allowlist.json`. A missing file is not an
   error: defaults are resolved in memory and written back at once, so a first run leaves
   both files on disk. A file that exists but does not parse is an error and stops startup,
   with the parse position in the message.
5. `store.Open(paths.Sessions)` scans the sessions directory for `*.json` and builds an
   index of `(id, project, agent, model, created, updated, title, messages)` without reading each
   file in full. See section 9.4.
6. Load or mint the token. `~/.quecto-harness/token` is read when it exists and its contents
   are non-empty after trimming; otherwise 32 bytes from `crypto/rand` are encoded with
   `base64.RawURLEncoding` and written there with the atomic replace, mode 0600. The token
   is therefore stable across restarts, which is what keeps an open browser tab working
   when a service supervisor restarts the process. Deleting the file rotates the token on
   the next start, which is the only way to revoke it.
7. Construct `llm.NewPool()`. Clients are made per agent, on the first request to one.
8. `net.Listen("tcp", listen)`. Binding happens before anything is printed, so a port
   conflict fails with `listen 127.0.0.1:7331: address already in use: another process is
   using the port, pass --listen to choose another` and no URL is emitted.
9. Build the `agent.Engine`, the `perms.Broker`, the `server.Server`, and start the SSE hub
   goroutine.
10. Kick off the probe of the active agent in a goroutine with a 5 second timeout, or
    nothing at all when no agent is selected. Startup does not wait on
    it. Until it returns, `GET /api/state` reports `"mode": "unknown"` and the agent chip
    shows a spinner. A failure is cached as an error string and re-attempted on the next
    `/api/state` that finds the cache older than 15 seconds.
11. Write the URL file: `~/.quecto-harness/url`, mode 0600, content
    `http://127.0.0.1:7331/?token=<token>\n`. Written with the atomic replace of section
    3.4 so a concurrent `quecto-harness open` never sees a partial line.
12. Print the same URL to stdout.
13. Unless `--no-open`, spawn the browser opener (section 1.4).
14. `http.Serve(ln, srv)` in a goroutine; block on `signal.NotifyContext(ctx,
    os.Interrupt, syscall.SIGTERM)`.

If the listen address is not loopback, step 8 additionally prints one warning line to
stderr: `listening on <addr>: the session token is the only access control, bind to
127.0.0.1 unless you meant this`. The server still starts.

### 1.3 Shutdown

On the first SIGINT or SIGTERM:

1. Cancel the root context. Every in-flight `llm.Chat` request, every running shell
   command, and every pending permission wait observes it.
2. `perms.Broker.CancelAll()` releases blocked tool calls with `context.Canceled`, which the
   agent loop turns into a terminal `stopped` state for each active turn.
3. `agent.Engine.Wait(3 * time.Second)` waits for turn goroutines to reach a terminal state
   and persist. Turns that have not finished by the deadline are abandoned; their session
   file keeps whatever was last flushed.
4. `srv.Shutdown(ctx)` with a 5 second deadline. Open SSE connections are closed by
   cancelling their per-connection context first, since `Shutdown` waits on active handlers
   and an SSE handler is active for as long as the client is attached.
5. `os.Remove(paths.URL)`. A failure is logged, not fatal.
6. Exit 0. A second signal exits 130 immediately without any of the above.

Running shell commands are killed by process group as described in section 6.5. A command
that ignores SIGTERM is SIGKILLed 2 seconds later, which happens inside step 3.

### 1.4 Opening the browser

```go
// internal/server or cmd; single small helper.
func OpenBrowser(url string) error
```

macOS runs `open <url>`, Linux runs `xdg-open <url>`. The process is started with
`exec.Command(...).Start()` and never waited on; stdout and stderr go to `os.DevNull`. A
missing `xdg-open` returns an error which is printed as a single line
(`open browser: xdg-open not found: visit <url> yourself`) and does not stop the server.

`quecto-harness open` reads `~/.quecto-harness/url`, trims whitespace, and calls the same
helper. A missing file prints `no saved URL at ~/.quecto-harness/url: start quecto-harness
first, or check service status` and exits 1.

### 1.5 The token file and the URL file

`~/.quecto-harness/token` holds the token alone, one line, mode 0600. It is created on first
start and reused afterwards. Nothing rewrites it; removing it is how the token is rotated.

`~/.quecto-harness/url` holds the full URL with the token, one line, mode 0600. It is
rewritten on every start because the listen address can change between starts, and it exists
because a service-managed process has no terminal to print to. `service status` prints its
contents.

The URL file is removed on clean shutdown. A stale one left by a crash still carries a valid
token, since the token now outlives the process, so `quecto-harness open` against a stopped
server fails at the connection rather than at the login.

---

## 2. Package map

```
cmd/quecto-harness
  |
  +-> internal/config      paths, config.json, allowlist.json, atomic write
  +-> internal/store       session files
  +-> internal/llm         model server clients and wire types
  +-> internal/perms       decision broker
  +-> internal/tools       read_file, write_file, run_shell
  +-> internal/prompts     embedded markdown, render funcs
  +-> internal/agent       turn loop
  +-> internal/server      routes, SSE hub, auth
  +-> internal/service     launchd and systemd units
  +-> internal/web         go:embed dist, fallback page
```

Import direction, no cycles:

```
llm       -> (stdlib only)
prompts   -> (stdlib only)
web       -> (stdlib only)
config    -> llm (Effort type)
tools     -> config (WriteAtomic), llm (Tool type for schemas)
store     -> config (WriteAtomic), llm (Message, ToolCall, Usage)
perms     -> config (allowlist read and append)
agent     -> config, llm, tools, perms, prompts, store
service   -> config (paths only)
server    -> config, store, agent, perms, llm, web
cmd       -> everything
```

Why each boundary exists:

- **config** is the only package that reads or writes `~/.quecto-harness/config.json` and
  `allowlist.json`. Anything else takes a resolved value or a `*config.Store`. This keeps
  the "every field optional on read" rule in one file and makes defaults testable without a
  filesystem. It imports `llm` for one type alias and nothing
  else, which cannot create a cycle because `llm` imports no internal package.
- **llm** knows nothing about sessions, tools or permissions. It converts HTTP and SSE
  into Go values and back. It owns the `Effort` type, because effort only means something
  as a set of request fields and that translation lives here. It is the package with the
  highest fixture-to-code ratio. It holds one backend per protocol behind a single
  interface, so a protocol is a file in this package and nothing above it changes.
- **tools** has no knowledge of who approved a call. It takes a resolved project root and
  arguments and returns bytes. Its only policy is the path guard, which is a correctness
  property of the tool rather than a permission.
- **perms** decides. It never executes. Splitting decision from execution is what lets the
  loop block on a channel while the HTTP handler resolves it from another goroutine.
- **agent** is the only package that knows the order of operations in a turn. It is also the
  only package that mutates a session.
- **store** persists and lists. It does not know what a turn is.
- **server** translates HTTP to calls on `agent`, `store`, `config`, `perms` and `llm`. It
  owns the SSE hub because fan-out to browsers is a transport concern.
- **prompts** is separate so that every string sent to a model is in one directory of
  markdown, and a missing file fails the build.
- **web** is separate from `server` so that a checkout without Node still compiles: the
  embed directive and the fallback page live together and `server` only asks for an
  `http.Handler`.
- **service** is separate so that the unit text is a pure function of a few paths and can be
  asserted in a test on a machine that has neither launchd nor systemd.

---

## 3. internal/config

### 3.1 Paths

```go
package config

// Paths are the locations under the state directory. Nothing is written
// outside it, and nothing is written into a project folder.
type Paths struct {
	Home      string // ~/.quecto-harness
	Config    string // <Home>/config.json
	Allowlist string // <Home>/allowlist.json
	Sessions  string // <Home>/sessions
	Log       string // <Home>/log
	Token     string // <Home>/token
	URL       string // <Home>/url
}

func DiscoverPaths() (Paths, error)   // honours $QUECTO_HARNESS_HOME
func DiscoverPathsIn(home string) Paths
func (p Paths) EnsureDirs() error     // 0700 on Home, Sessions, Log
```

`DiscoverPaths` uses `os.UserHomeDir`. On a machine with no home directory it returns an
error naming `$QUECTO_HARNESS_HOME` as the fix.

### 3.2 The wire shape and the resolved shape

Two types. `File` is what is on disk, every field a pointer or a slice so that absent and
zero are distinguishable. `Config` is what the rest of the program sees, every field a
value that has already been defaulted and validated.

```go
// File mirrors config.json. A nil field is absent and takes the default.
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
	Model             *string  `json:"model,omitempty"`
	Effort            *string  `json:"effort,omitempty"`
	PreserveReasoning *bool    `json:"preserve_reasoning,omitempty"`
	Temperature       *float64 `json:"temperature,omitempty"`
	TopP              *float64 `json:"top_p,omitempty"`
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
	MaxToolCalls     int
	Projects         []string
}

// Agent is one model server the harness can answer with: where it is, what key
// opens it, which model, and how that model is asked. One is active at a time
// and every session uses it.
type Agent struct {
	ID                string
	Name              string
	URL               string
	Key               string
	Model             string
	Effort            Effort
	PreserveReasoning bool
	Temperature       *float64
	TopP              *float64
	// Instructions follow the built-in system prompt in the same message.
	Instructions string
}

func (c Config) Agent() (Agent, bool)
func (c Config) AgentByID(id string) (Agent, bool)
func NewAgentID() string // "agt_" + 16 base32 characters
```

An agent id is minted rather than taken from the name, so a rename moves nothing: not the
selection, not `Session.Agent`, not a pooled client, not a cached reading. Two agents may
not share a name, which is refused by the API and tolerated in a hand-edited file, since
the id is the identity and dropping a working connection over a label is worse.

`config.json`:

```json
{
  "listen": "127.0.0.1:7331",
  "agents": [{
    "id": "agt_2f7qk3m9x4pd8s1v",
    "name": "Local llama.cpp",
    "url": "http://192.168.2.205:8080",
    "key": "",
    "model": "Gemma4-12B",
    "effort": "medium",
    "preserve_reasoning": true,
    "temperature": 0.35,
    "instructions": ""
  }],
  "agent": "agt_2f7qk3m9x4pd8s1v",
  "permissions": "ask", "compact_at": 0.8, "shell": "/bin/sh",
  "command_timeout_s": 120, "output_limit_bytes": 16384,
  "max_tool_calls": 50, "projects": []
}
```

`top_p` is absent because it is unset. A sampling value left to the server is the one
thing `Config.File()` does not write, and that rule holds per agent.

### 3.3 Defaults and validation

```go
func Defaults() Config {
	return Config{
		Listen:           "127.0.0.1:7331",
		Agents:           nil,
		AgentID:          "",
		Mode:             ModeAsk,
		CompactAt:        0.8,
		Shell:            defaultShell(), // $SHELL if absolute and executable, else /bin/sh
		CommandTimeout:   120 * time.Second,
		OutputLimitBytes: 16384,
		MaxToolCalls:     50,
		Projects:         nil,
	}
}

// Resolve folds the file over the defaults. Out-of-range values fall back to
// the default and are reported, so a hand-edited file does not fail startup.
func (f File) Resolve(d Config) (Config, []string)
```

Clamping rules applied by `Resolve`, each producing one note string:

| field | accepted | on violation |
|-------|----------|--------------|
| `listen` | parses with `net.SplitHostPort`, port 1 to 65535 | default |
| `permissions` | one of ask, allowlist, full | `ask` |
| `compact_at` | 0.05 to 0.95 | 0.8 |
| `shell` | absolute path | `/bin/sh` |
| `command_timeout_s` | 1 to 3600 | 120 |
| `output_limit_bytes` | 1024 to 4194304 | 16384 |
| `max_tool_calls` | 0 to 1000 | 50 |
| `projects` | each entry absolute and `filepath.Clean`ed; duplicates dropped | offending entries dropped |
| `agent` | names an entry of `agents` | selection cleared |

And per agent:

| field | accepted | on violation |
|-------|----------|--------------|
| `id` | any non-empty string, unique in the list | absent is minted; a repeat drops the entry |
| `name` | non-empty after trimming | `Agent` |
| `url` | empty, or `url.Parse` with scheme http or https and a host | cleared, the agent kept |
| `effort` | one of auto, off, low, medium, high | `medium` |
| `temperature` | 0 to 2 | left to the model server |
| `top_p` | 0 to 1 | left to the model server |
| `instructions` | any text, trimmed | trimming is not a violation |
| `model` | anything | never validated: it is a remote id and the list is not known at read time |

A blank `url` is not a violation. An agent being filled in is a normal state, and so is
the empty list a first run leaves.

Minting an id inside `Resolve` makes it impure. It is the only place that sees the whole
list and can keep ids unique, and it means a hand-written `{"name":"x","url":"..."}`
becomes a working agent rather than vanishing.

Notes are printed once at startup as `Config.json: <note>` and are also returned by
`GET /api/state` as `config_notes`, so a bad edit is visible in the UI.

`Config.File()` is the inverse: every field written, no omissions, so a saved file is a
complete document and a later reader never has to guess which defaults were in force.

### 3.4 Atomic write

```go
// WriteAtomic replaces path with data. The temp file is created in the same
// directory so the rename cannot cross a filesystem boundary.
func WriteAtomic(path string, data []byte, perm os.FileMode) error
```

Procedure: `os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")`, `Chmod`
to `perm`, `Write`, `Sync`, `Close`, `os.Rename`. On any failure before the rename the temp
file is removed. The parent directory is not fsynced; a crash between rename and directory
flush loses the write but never leaves a half-written file, which is the property that
matters for a config file.

`config.json` and `allowlist.json` are written 0600. Session files are written 0600. The
token file and the URL file are written 0600. `WriteAtomic` is exported and used by `store`
and by `tools.WriteFile`.

### 3.5 The store

```go
// Store holds the resolved configuration and the allowlist, and is the only
// writer of the two files. All methods are safe for concurrent use.
type Store struct {
	mu    sync.RWMutex
	paths Paths
	cfg   Config
	allow map[string][]string
	notes []string
}

func Open(p Paths) (*Store, error)

func (s *Store) Paths() Paths
func (s *Store) Config() Config
func (s *Store) Notes() []string

// Update applies fn to a copy, validates, persists, and returns the result.
// The lock is held across the write so two concurrent PUTs cannot interleave.
func (s *Store) Update(fn func(*Config)) (Config, error)

func (s *Store) Allowlist(project string) []string
func (s *Store) AllowMatch(project, command string) (prefix string, ok bool)
func (s *Store) AddAllow(project, prefix string) error
func (s *Store) RemoveAllow(project, prefix string) error

func (s *Store) AddProject(path string) (Config, error)
func (s *Store) RemoveProject(path string) (Config, error)

// The four agent methods each need an error Update cannot express. AddAgent
// mints the id and selects the agent when nothing was selected; RemoveAgent
// hands the turn to the first agent left, or clears the selection when the
// list empties.
func (s *Store) AddAgent(a Agent) (Config, Agent, error)
func (s *Store) UpdateAgent(id string, fn func(*Agent)) (Config, Agent, error)
func (s *Store) RemoveAgent(id string) (Config, error)
func (s *Store) SelectAgent(id string) (Config, error)
func (s *Store) AgentKeys() []string // llm.Key per agent, for the pool to keep
```

`Config()` and `Update` copy the agent list and the values behind its pointers, so a
config taken before a write does not change with it.

`AddProject` resolves the path with `filepath.Abs` then `filepath.EvalSymlinks`, requires it
to be an existing directory, requires it to be readable, and rejects a duplicate. It stores
the symlink-resolved absolute path, which is also what the path guard compares against and
what keys the allowlist.

### 3.6 allowlist.json

```json
{
  "/Users/jay/Projects/Personal/quecto-harness": ["git status", "go test", "ls"],
  "/Users/jay/src/other": ["make"]
}
```

Keys are symlink-resolved absolute project roots. Values are prefixes, deduplicated,
sorted longest-first on load so that `AllowMatch` reports the most specific match. An entry
that is the empty string is dropped on load.

### 3.7 Migration stance

There is no version field and no migration code. Unknown keys are ignored on read. A key
that disappears in a future version simply stops being read; a key that appears takes its
default until the file is next written, at which point it is materialised. Because
`Config.File()` writes every field, the file self-heals into the current shape after any
`PUT /api/config`. If a future change needs a genuinely incompatible reinterpretation of an
existing key, the key gets a new name and the old one is ignored.

---

## 4. internal/llm

### 4.1 Client and backends

Two protocols are spoken. A llama.cpp server, in router or single-model mode, and an
OpenAI-compatible endpoint, which has a model list and a chat endpoint and nothing else.
One interface covers both, and nothing above this package names a protocol:

```go
type Backend interface {
	Detect(ctx context.Context) (Info, error)
	Models(ctx context.Context) ([]Model, error)
	Prepare(ctx context.Context, model string) (Ready, error)
	Props(ctx context.Context, model string) (Props, error)
	Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error)
}
```

`Client` is the address: the HTTP half both backends share, the probe that settles which one
answers, and a delegation of the five calls to it.

```go
// Client talks to one model server and works out on its first request which
// protocol that server speaks.
type Client struct {
	BaseURL string       // no trailing slash
	Token   string       // optional bearer
	HTTP    *http.Client // defaults to a client with no global timeout
}

func New(baseURL, token string) *Client

// Pool hands out one client per address and key. Two agents at one address
// share the client and the protocol it settled on.
type Pool struct { ... }

func NewPool() *Pool
func Key(baseURL, token string) string
func (p *Pool) Get(baseURL, token string) *Client
func (p *Pool) Retain(live []string)
```

Keying a client on the address and the key together is the whole of the invalidation
rule, which is why there is no invalidation code. A rename, or a change of model, effort,
sampling or instructions, is the same key and so the same client and no fresh probe. A
changed address or key is a different client, which is correct: nothing about what
answered before carries over. `Retain` runs after a write that touched the agent list and
drops the clients no agent points at, closing the connections they held.

`Detect` is the probe. It reads `GET /props` with no query parameters, which is a read on
any llama.cpp server and cannot load a model, and a reply carrying `build_info` selects the
llama.cpp backend. Anything else, a 404 included, falls through to the OpenAI-compatible
backend, whose own detection is the model list. Silence and a refused token name no protocol,
so they are returned as the failure they are and nothing is remembered.

`Detect` decides again every time it is called rather than answering from what it found
before, because what answers at an address can change while the harness runs: a box that
served one protocol at breakfast can serve the other by lunch, and a gateway that was
starting up answered like neither. The other four calls take the last decision for
`resolveTTL`, one minute, so a run of requests costs one probe and a swapped server is
followed within it.

The HTTP client has no `Timeout`, because a chat stream can legitimately run for minutes.
Deadlines come from the caller's context. Transport settings: `MaxIdleConnsPerHost: 4`,
`IdleConnTimeout: 90s`, `ResponseHeaderTimeout: 60s`, `DisableCompression: true` for the
streaming path (gzip on an SSE body defeats incremental delivery on some proxies).
`ExpectContinueTimeout: 1s`.

Every request carries `Accept: application/json` (or `text/event-stream` for chat) and, when
`Token` is non-empty, `Authorization: Bearer <token>`.

### 4.2 Detect

```go
type Mode string

const (
	ModeUnknown Mode = "unknown"
	ModeRouter  Mode = "router"
	ModeSingle  Mode = "single"
	ModeOpenAI  Mode = "openai"
)

// Info is what one probe tells us about the server as a whole.
type Info struct {
	Mode     Mode
	Build    string // llama.cpp only; build_info, e.g. "b10812-8b4b3558f"
	Autoload bool   // router only; models_autoload
	Model    string // the one model this server holds, when it holds one
	NCtx     int    // that model's window; 0 when the server did not say
}

func (c *Client) Detect(ctx context.Context) (Info, error)
```

`GET /props` with no query parameters. Router capture:

```json
{"role":"router","max_instances":1,"models_autoload":true,"model_alias":"llama-server",
 "model_path":"none","default_generation_settings":{"params":null,"n_ctx":0},
 "ui_settings":{},"build_info":"b10812-8b4b3558f","cors_proxy_enabled":false}
```

Rule: `role == "router"` selects `ModeRouter`. Anything else, including an absent `role`,
selects `ModeSingle`. `model_alias` is present in both shapes (the router reports
`"llama-server"`), so it is not a discriminator and is only read in single mode.
`default_generation_settings.n_ctx` is `0` on a router and is only read in single mode.

The OpenAI-compatible backend has no such endpoint. It reads `GET /v1/models`, reports
`ModeOpenAI`, and names `Model` and `NCtx` when the list holds exactly one model.

### 4.3 Models

```go
type ModelStatus string

const (
	StatusUnloaded    ModelStatus = "unloaded"
	StatusLoading     ModelStatus = "loading"
	StatusLoaded      ModelStatus = "loaded"
	StatusSleeping    ModelStatus = "sleeping"
	StatusDownloading ModelStatus = "downloading"
)

// Model is one entry of GET /v1/models, reduced to what the picker needs.
type Model struct {
	ID         string
	Status     ModelStatus
	Failed     bool
	ExitCode   int
	Modalities []string // architecture.input_modalities
	CtxHint    int      // per-slot context, best available guess
	CtxSource  string   // "meta", "args" or "" when unknown
}

func (c *Client) Models(ctx context.Context) ([]Model, error)
```

`GET /v1/models`. No query parameters, so no model is loaded by this call. Both backends
read this route; what they read out of an entry differs.

`CtxHint` resolution on a llama.cpp server, in order:

1. `meta.n_ctx` when present and greater than zero. Only a loaded instance carries `meta`.
   In the capture, `Gemma4-12B` is `loaded` and reports `meta.n_ctx: 131072`, which equals
   the per-slot value from `/props?model=`. Source `"meta"`.
2. Otherwise parse `status.args`: find `--ctx-size` (also accept `-c`) and `--parallel`
   (also accept `-np`), each as the argv element followed by its value. Hint is
   `ctxSize / max(parallel, 1)`, integer division. In the capture,
   `--ctx-size 262144 --parallel 2` gives `131072`. Source `"args"`.
3. Otherwise 0. Source `""`. The picker row shows `ctx unknown`. A session bound to such a
   model does not carry the zero forward; it assumes 32768, as described in 8.5.

`status.value` maps directly. `status.failed` and `status.exit_code` are read when present
and surfaced so the picker can show `failed (exit 1)`. `status.progress` for `downloading`
is not read; the status word alone is shown.

A model whose status is not one of the five known words is passed through verbatim in
`Status` and rendered as a grey dot with the raw word as its label.

An OpenAI-compatible list says nothing about loading, so every model it names is reported
`loaded`: it can answer, which is all the picker is asking. `CtxHint` comes from
`max_model_len`, or `context_length` when that is what the server calls it, with source
`"models"`. The capture reports `max_model_len: 240000`.

### 4.4 Props

```go
// Caps is chat_template_caps. Known is false when the server did not send the
// object, which is how a not-yet-loaded model looks.
type Caps struct {
	Known             bool
	ToolCalls         bool
	ParallelToolCalls bool
	ReasoningEffort   bool
	SystemRole        bool
	PreserveReasoning bool
}

// Props is what one model reports about itself.
type Props struct {
	Model      string // model_alias
	NCtx       int    // default_generation_settings.n_ctx, per slot
	TotalSlots int
	Build      string
	Sleeping   bool
	Caps       Caps
}

func (c *Client) Props(ctx context.Context, model string) (Props, error)
```

Request in router mode: `GET /props?model=<url-escaped id>&autoload=false`. In single mode
the model parameter is omitted entirely. The `autoload=false` parameter is added by a single
helper that every model-scoped GET goes through:

```go
// modelURL builds a model-scoped GET. autoload=false is not optional: a
// model-scoped GET without it loads the model on a router.
func modelURL(base, path, model string, autoload bool) string
```

The capture for `Gemma4-12B` gives `default_generation_settings.n_ctx: 131072`,
`total_slots: 2`, `model_alias: "Gemma4-12B"`, `is_sleeping: false`, and:

```json
"chat_template_caps": {
  "supports_object_arguments": true,
  "supports_parallel_tool_calls": true,
  "supports_preserve_reasoning": false,
  "supports_reasoning_effort": false,
  "supports_string_content": true,
  "supports_system_role": true,
  "supports_tool_calls": true,
  "supports_tools": true,
  "supports_typed_content": false
}
```

`chat_template_caps` is decoded as `map[string]bool` and folded into `Caps`, so a new
capability key does not break decoding. `Caps.Known` is set when the map is non-nil.

A 4xx on `Props` means the model is not loaded (the router refuses a model-scoped read for
an unloaded instance when autoload is off). The caller keeps the `CtxHint` and leaves
`Caps.Known` false. This is not surfaced as an error to the user.

An OpenAI-compatible server publishes no capabilities and no slot count, so its `Props` is
the model list entry: `Model` and `NCtx`, with `Caps.Known` false. A model the list does not
name is `KindNotFound`, since there is nothing to load it. `Prepare` on that backend is the
same read, so a session starts with the published window and a template it must not assume
anything about.

### 4.5 Chat: the request

```go
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON text, not a decoded object
}

type Tool struct {
	Type     string       `json:"type"` // "function"
	Function FunctionSpec `json:"function"`
}

type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Effort is the UI thinking level. It becomes request fields in applyEffort
// and has no other meaning.
type Effort string

const (
	// Auto sends no thinking field at all. That is not the same as off: the
	// template decides, which is what llama.cpp does with --reasoning auto.
	EffortAuto   Effort = "auto"
	EffortOff    Effort = "off"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// ChatRequest is the harness-level request. Each backend renders it into its
// own wire body, so a caller never names a protocol field.
type ChatRequest struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	Effort    Effort
	Caps      Caps
	MaxTokens int // 0 leaves the cap to the server
}
```

`baseBody(ChatRequest)` builds the part both protocols take: `model`, `messages`, `tools`,
`max_tokens`, `tool_choice`, `stream` and `stream_options`. Each backend adds its own
thinking fields on top. `buildLlamaBody(ChatRequest) ([]byte, error)` emits, in this field
order:

```json
{
  "model": "Gemma4-12B",
  "messages": [
    {"role": "system", "content": "You are a coding agent working in /Users/jay/src/x on darwin.\n..."},
    {"role": "user", "content": "list the files here"},
    {"role": "assistant", "content": "", "tool_calls": [
      {"id": "call_0", "type": "function",
       "function": {"name": "run_shell", "arguments": "{\"command\":\"ls\"}"}}
    ]},
    {"role": "tool", "tool_call_id": "call_0", "name": "run_shell",
     "content": "README.md\nmain.go\n\nexit code: 0"}
  ],
  "tools": [
    {"type": "function", "function": {
      "name": "read_file",
      "description": "Read a text file in the project. Returns numbered lines. Use offset and limit for large files.",
      "parameters": {"type": "object", "properties": {
        "path":   {"type": "string",  "description": "Path relative to the project root."},
        "offset": {"type": "integer", "description": "First line to return, 1-based."},
        "limit":  {"type": "integer", "description": "Number of lines to return. Default 400."}
      }, "required": ["path"]}
    }},
    {"type": "function", "function": {"name": "write_file", "...": "..."}},
    {"type": "function", "function": {"name": "run_shell",  "...": "..."}}
  ],
  "tool_choice": "auto",
  "stream": true,
  "stream_options": {"include_usage": true},
  "reasoning_format": "deepseek",
  "reasoning_budget": 8192
}
```

`max_tokens` appears between `tools` and `tool_choice` when `MaxTokens` is greater than
zero, which only the context measurement of section 8.6 sets.

The example is a medium-effort turn against `Gemma4-12B`, whose template reports
`supports_reasoning_effort: false`. That is why it carries `reasoning_budget: 8192` and no
`reasoning_effort`. Against a template that reports the capability, the same turn carries
`"reasoning_effort": "medium"` and no budget.

Rules for the thinking fields, derived from the settled effort table:

```go
func applyEffort(body, kwargs map[string]any, e Effort, caps Caps) {
	switch {
	case e == EffortAuto:
		// Nothing is set. Omitting the field and setting it to a level the
		// template did not ask for are different requests.
	case e == EffortOff:
		body["reasoning_budget"] = 0
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	case caps.Known && caps.ReasoningEffort:
		body["reasoning_effort"] = string(e) // "low", "medium", "high"
	default:
		body["reasoning_budget"] = budget[e] // 2048, 8192, -1
	}
}

var budget = map[Effort]int{EffortLow: 2048, EffortMedium: 8192, EffortHigh: -1}
```

`reasoning_format: "deepseek"` is sent on every request regardless of effort, so any thought
text arrives in `reasoning_content` rather than inline in `content`. `tool_choice: "auto"` is
sent on every request. `tools` is omitted entirely when the tool list is empty, which is the
case for the compaction request.

For `Gemma4-12B` as captured, `supports_reasoning_effort` is false, so a medium turn sends
`"reasoning_budget": 8192` and no `reasoning_effort`.

The body is built as a `map[string]any` rather than a struct because the field set is
conditional and Go struct tags cannot express "omit unless the caps say so" without a
pointer for every field. Determinism for tests comes from `json.Marshal` on a map, which
sorts keys.

`buildOpenAIBody` sends only fields the OpenAI API defines. `reasoning_format`,
`reasoning_budget` and `chat_template_kwargs` are llama.cpp's and are left out; thinking
travels as `reasoning_effort`, with `"none"` for off and nothing at all for auto. There are
no capabilities to consult, so the level goes out as chosen and a server that does not take
the field, or does not take that level, refuses the request and says why. Thought text still
arrives in `reasoning_content` or `reasoning`, both of which the parser already reads.

### 4.6 Chat: the call

```go
type EventKind int

const (
	EventContent EventKind = iota
	EventReasoning
	EventToolCall
	EventFinish
	EventUsage
	EventTimings
)

// Event is one decoded piece of a stream. Only the fields relevant to Kind
// are set.
type Event struct {
	Kind EventKind

	Text string // EventContent, EventReasoning

	Index     int    // EventToolCall
	ID        string // EventToolCall, cumulative
	Name      string // EventToolCall, cumulative
	Arguments string // EventToolCall, the fragment from this chunk

	FinishReason string   // EventFinish
	Usage        *Usage   // EventUsage
	Timings      *Timings // EventTimings
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int // prompt_tokens_details.cached_tokens
}

type Timings struct {
	PromptN            int
	PromptPerSecond    float64
	PredictedN         int
	PredictedPerSecond float64
	CacheN             int
}

// Result is the assembled turn. It is also derivable from the events, and is
// returned so callers that do not stream to a UI need not accumulate.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
	Timings      *Timings
	Chunks       int // frames that decoded
	Dropped      int // frames that did not
}

// Chat streams one completion. on is called synchronously from the read loop,
// in stream order; returning an error from it aborts the request.
func (c *Client) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error)
```

`POST /v1/chat/completions`, `Content-Type: application/json`,
`Accept: text/event-stream`, `Cache-Control: no-store`.

Non-2xx: the body is read up to 8 KiB and turned into an `*Error` (section 4.10) without
being streamed. A 2xx whose `Content-Type` is not `text/event-stream` is decoded as a whole
completion and converted into the same event sequence, so a server that quietly ignores
`stream: true` still works.

### 4.7 SSE parser state machine

Input is the response body. Reading is line-oriented over a `bufio.Reader` with
`ReadSlice('\n')` and a hand-rolled grow, capped at `maxFrameBytes = 1 << 20`. A frame
larger than the cap is discarded and counted as dropped, and the parser resynchronises at
the next blank line.

States:

| state | meaning |
|-------|---------|
| `sIdle` | between events; only a field line or a blank line is expected |
| `sEvent` | at least one `data:` line seen for the current event |
| `sSkip` | current event exceeded the frame cap; discard until a blank line |
| `sDone` | `data: [DONE]` seen, or the body ended |

Transitions, applied per line with the trailing `\r\n` or `\n` stripped:

| state | line | action | next |
|-------|------|--------|------|
| `sIdle` | empty | nothing | `sIdle` |
| `sIdle` | starts with `:` | ignore (comment or keepalive) | `sIdle` |
| `sIdle` | `data: X` | start buffer with `X` | `sEvent` |
| `sIdle` | `event: X`, `id: X`, `retry: X` | record and ignore | `sIdle` |
| `sIdle` | anything else | count as dropped, ignore | `sIdle` |
| `sEvent` | `data: X` | append `"\n" + X` to buffer | `sEvent` |
| `sEvent` | other field line | ignore | `sEvent` |
| `sEvent` | empty | dispatch buffer, reset | `sIdle` or `sDone` |
| `sEvent` | buffer exceeds cap | drop buffer, `Dropped++` | `sSkip` |
| `sSkip` | empty | reset | `sIdle` |
| `sSkip` | anything | ignore | `sSkip` |
| `sDone` | anything | ignore | `sDone` |

Dispatch of a buffered payload:

1. Trim space. If the payload is `[DONE]`, move to `sDone` and stop reading.
2. If the payload does not start with `{`, `Dropped++` and return.
3. `json.Unmarshal` into the chunk type. On error, `Dropped++` and return. A malformed
   chunk never ends the stream and never propagates an error.
4. `Chunks++`, then emit events in this order for the chunk: reasoning delta, content delta,
   each tool-call fragment in the order the server listed them, finish reason, usage,
   timings.

At EOF without `[DONE]`, three cases:

- At least one chunk decoded and a finish reason was seen: the stream ends normally.
- At least one chunk decoded but no finish reason arrived: the server closed mid-generation.
  This is `KindUpstream` with the message `the model server closed the stream early: send
  the message again`. `Result` still carries whatever content, reasoning and tool calls had
  accumulated, so the UI shows the partial reply, and the turn ends in `failed`.
- No chunk decoded at all: `KindMalformed`, carrying the count of dropped frames.

`ctx.Done()` closes the body and returns `KindCanceled`.

### 4.8 Every field optional

The chunk type uses pointers for every scalar that has a meaningful zero, and pointers for
every nested object, so that "absent" and "zero" stay distinct. Slices are left as slices;
an absent array and an empty array mean the same thing here.

```go
type chunk struct {
	ID      *string  `json:"id"`
	Object  *string  `json:"object"`
	Model   *string  `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage"`
	Timings *timings `json:"timings"`
	Error   *apiErr  `json:"error"`
}

type choice struct {
	Index        *int    `json:"index"`
	Delta        *delta  `json:"delta"`
	Message      *delta  `json:"message"` // non-streaming shape
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role             *string        `json:"role"`
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content"`
	Reasoning        *string        `json:"reasoning"` // alias some builds use
	ToolCalls        []deltaToolCall `json:"tool_calls"`
}

type deltaToolCall struct {
	Index    *int    `json:"index"`
	ID       *string `json:"id"`
	Type     *string `json:"type"`
	Function *struct {
		Name      *string `json:"name"`
		Arguments *string `json:"arguments"`
	} `json:"function"`
}

type usage struct {
	PromptTokens         *int `json:"prompt_tokens"`
	CompletionTokens     *int `json:"completion_tokens"`
	TotalTokens          *int `json:"total_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type timings struct {
	CacheN             *int     `json:"cache_n"`
	PromptN            *int     `json:"prompt_n"`
	PromptPerSecond    *float64 `json:"prompt_per_second"`
	PredictedN         *int     `json:"predicted_n"`
	PredictedPerSecond *float64 `json:"predicted_per_second"`
}

type apiErr struct {
	Code    *int    `json:"code"`
	Message *string `json:"message"`
	Type    *string `json:"type"`
}
```

Two helpers keep the call sites short: `deref[T any](p *T) T` and `derefOr[T any](p *T, d T) T`.

`Content` and `ReasoningContent` are both read; when a build sends `reasoning` instead of
`reasoning_content`, the alias is used only if `reasoning_content` is absent. Only
`choices[0]` is read; a chunk with more than one choice logs nothing and uses the first,
because `n` is never sent.

A chunk carrying `error` is turned into an `*Error` with `KindUpstream` and terminates the
stream, since a mid-stream error object means the server has stopped generating.

### 4.9 Tool call fragment accumulation

State held for the duration of one `Chat` call:

```go
type partial struct {
	index int
	id    strings.Builder
	name  strings.Builder
	args  strings.Builder
	seen  int // order of first appearance
}

acc := map[int]*partial{}
order := []int{}
```

For each `deltaToolCall` in a chunk:

1. `idx := derefOr(d.Index, len(order))`. An absent index means the server is streaming a
   single call without indices; the first such fragment allocates index 0 and every later
   indexless fragment appends to the last index seen.
2. If `acc[idx]` is nil, create it and append `idx` to `order`.
3. Append `deref(d.ID)`, `deref(d.Function.Name)` and `deref(d.Function.Arguments)` to their
   builders. All three are cumulative because some builds resend the id on every fragment
   and others send it once; appending an empty string is a no-op, and a repeated identical
   id is guarded by "only append when the builder is empty or the new text is not a prefix
   already present" for `id` and `name`. `arguments` is always appended raw.
4. Emit `Event{Kind: EventToolCall, Index: idx, ID: <accumulated id>, Name: <accumulated
   name>, Arguments: <this fragment only>}`.

At the end of the stream, `order` is walked and each `partial` becomes a `ToolCall` with
`Type: "function"`. An entry whose accumulated name is empty is dropped and counted in
`Dropped`. An entry whose id is empty gets `fmt.Sprintf("call_%d", idx)`, because the tool
result message needs a `tool_call_id` and some builds omit it.

`Arguments` is not parsed by `llm`. Validation against the schema happens in `agent` when
the call is dispatched, so a malformed argument object becomes a tool error message the
model can react to rather than a client error.

### 4.10 Usage and timings

`stream_options: {"include_usage": true}` makes the server send a final chunk with
`choices: []` and a `usage` object. Recorded shape from the llama.cpp server:

```json
{"choices":[],"created":1757141666,"id":"chatcmpl-...","model":"Gemma4-12B",
 "usage":{"completion_tokens":48,"prompt_tokens":44,"total_tokens":92,
          "prompt_tokens_details":{"cached_tokens":0}},
 "timings":{"cache_n":236,"prompt_n":1,"prompt_ms":30.958,"prompt_per_second":32.30,
            "predicted_n":35,"predicted_ms":661.064,"predicted_per_second":52.94}}
```

`usage` and `timings` may also ride on a chunk that carries choices. Both are handled the
same way: the last non-nil value wins, `Result.Usage` and `Result.Timings` hold it, and an
`EventUsage` and `EventTimings` are emitted when they arrive. `TotalTokens` falls back to
`PromptTokens + CompletionTokens` when absent.

`Timings` is surfaced but not persisted. The UI shows `52 tok/s` next to a finished
assistant message and `32 tok/s prompt` in a tooltip. `cache_n` is shown in the same tooltip
as `236 cached`.

### 4.11 Error taxonomy

```go
type ErrKind string

const (
	KindUnreachable  ErrKind = "unreachable"   // dial, DNS, TLS, connection reset
	KindAuth         ErrKind = "auth"          // 401, 403
	KindNotFound     ErrKind = "not_found"     // 404, unknown model id
	KindBadRequest   ErrKind = "bad_request"   // other 4xx
	KindModelLoading ErrKind = "model_loading" // 503 with "loading" in the body
	KindUpstream     ErrKind = "upstream"      // 5xx, or an error object mid-stream
	KindMalformed    ErrKind = "malformed"     // nothing decodable arrived
	KindCanceled     ErrKind = "canceled"      // caller's context
)

// Error is every failure this package returns.
type Error struct {
	Kind    ErrKind
	Status  int    // 0 when no response was received
	Message string // server message when it sent one, else our own
	Body    string // first 8 KiB of the response body, for the log
	Op      string // "detect", "models", "props", "chat"
	err     error
}

func (e *Error) Error() string { ... } // "chat: model Gemma4-12B is loading, retry in a moment"
func (e *Error) Unwrap() error { return e.err }

func Kind(err error) ErrKind // errors.As, returns "" for any other error
```

Classification:

| condition | kind |
|-----------|------|
| `errors.Is(err, context.Canceled)` or `context.DeadlineExceeded` | `KindCanceled` |
| `*net.OpError`, `*url.Error` wrapping a dial or reset | `KindUnreachable` |
| 401, 403 | `KindAuth` |
| 404, or 400 whose message contains `model` and `not found` | `KindNotFound` |
| 503 whose body message contains `loading` | `KindModelLoading` |
| other 4xx | `KindBadRequest` |
| 5xx | `KindUpstream` |
| stream ended with `Chunks > 0` and no finish reason | `KindUpstream` |
| stream ended with `Chunks == 0` | `KindMalformed` |

Message text follows the "errors are for the reader" rule:

- `KindUnreachable`: `Reach <base URL>: the model server is not answering, check it is running and that the address is right`
- `KindAuth`: `Authenticate with <base URL>: the key was rejected, check the API key for this address`
- `KindNotFound`: `model "<id>" is not on <base URL>: pick another from the model list`
- `KindModelLoading`: `model "<id>" is loading: send the message again in a moment`
- `KindBadRequest`: `<server message>` verbatim, prefixed with `the model server refused the request: `
- `KindUpstream`, from a status code or an error object: `the model server failed: <server
  message>`
- `KindUpstream`, from an early close: `the model server closed the stream early: send the
  message again`
- `KindMalformed`: `the model server sent nothing usable: <n> frames could not be read`

A malformed chunk inside an otherwise good stream is not an error at all. It increments
`Result.Dropped`, and the agent emits a single `error` SSE event with kind `malformed` at
the end of the turn when `Dropped > 0`, so the user knows some output was lost without the
turn failing.

---

## 5. internal/prompts

### 5.1 Layout

```
internal/prompts/
  prompts.go
  prompts_test.go
  system.md
  tool_read_file.md
  tool_write_file.md
  tool_run_shell.md
  compact.md
```

```go
package prompts

//go:embed system.md tool_read_file.md tool_write_file.md tool_run_shell.md compact.md
var files embed.FS
```

Each file is named individually rather than by glob, so deleting one fails the build. A
package-level `var systemTmpl = template.Must(template.New("system").Parse(mustRead("system.md")))`
turns a template syntax error into a panic at init, which the package test catches.

### 5.2 Render functions

```go
// SystemVars are the values system.md takes. Instructions is the agent's own
// text, which follows the built-in prompt in the same message. It is inserted
// as a value and never parsed, so a stray template action in it is text.
type SystemVars struct {
	Project      string // absolute project root
	OS           string // runtime.GOOS
	Instructions string // the agent's, empty for most
}

func System(v SystemVars) string

func ToolReadFile() string
func ToolWriteFile() string
func ToolRunShell() string
func Compact() string
```

The four tool and compact functions return the file contents with trailing whitespace
trimmed. Only `system.md` is a template.

### 5.3 system.md

```
You are a coding agent working in {{.Project}} on {{.OS}}.
Tools: read_file, find, write_file, replace_in_file, run_shell, write_todos,
ask_question.
...
When the task is done, stop and summarize in two sentences.
{{if .Instructions}}

{{.Instructions}}
{{end}}
```

The agent's instructions come last, which is what makes them win where they disagree with
the guidance above, and there is no line introducing them. Nothing is dropped: the project
directory, the operating system and the tool names are stated whatever an agent adds, so a
short instruction cannot cost the model the facts it works from. `System` trims the
trailing whitespace, so an empty value renders the prompt byte for byte as it was before
agents existed.

The text is a value and never a template. `System` panics on a render error, so parsing it
would let a typo in a settings field take the process down, and two braces in someone's
instructions have to survive as two braces.

### 5.4 tool_read_file.md

```
Read a text file in the project. Returns numbered lines. Use offset and limit for large files.
```

Argument descriptions, which live in the Go schema and not in the markdown:

- `path`: `Path relative to the project root.`
- `offset`: `First line to return, 1-based.`
- `limit`: `Number of lines to return. Default 400.`

### 5.5 tool_write_file.md

```
Write content to a file, creating or replacing it. Parent folders are created.
```

- `path`: `Path relative to the project root.`
- `content`: `The full file contents.`

### 5.6 tool_run_shell.md

```
Run a shell command in the project root. Returns output and exit code. Each call is a new shell; nothing persists between calls.
```

- `command`: `The command line to run.`
- `timeout_s`: `Seconds before the command is killed. Default 120.`

### 5.7 compact.md

```
Summarize this conversation for yourself to continue the work.
Keep: the task, decisions, files changed with paths, current state, open problems, next step.
Drop chatter and tool output already acted on.
Plain text, under 400 words.
```

---

## 6. internal/tools

### 6.1 Runner

```go
// Runner executes the three tools against one project root. All paths are
// resolved against Root and may not leave it.
type Runner struct {
	Root        string // symlink-resolved absolute directory
	Shell       string
	Timeout     time.Duration // default per call
	MaxTimeout  time.Duration // ceiling a call may ask for
	OutputLimit int           // bytes retained from run_shell
	ReadLimit   int           // bytes retained from read_file, 64 << 10
	ReadLines   int           // default line count, 400
}

func NewRunner(root string, cfg config.Config) (*Runner, error)

// Result is what the model is told. Output is already truncated and, for
// run_shell, already carries the exit code line.
type Result struct {
	Output    string
	ExitCode  *int  // run_shell only
	Truncated bool
	Bytes     int   // bytes before truncation
}

func (r *Runner) ReadFile(ctx context.Context, args json.RawMessage) (Result, error)
func (r *Runner) WriteFile(ctx context.Context, args json.RawMessage) (Result, error)
func (r *Runner) RunShell(ctx context.Context, args json.RawMessage) (Result, error)

// Schemas returns the three tool definitions in the order the model sees them.
func Schemas() []llm.Tool
```

`NewRunner` fails if `root` is not an existing directory after `filepath.EvalSymlinks`.

Argument types:

```go
type ReadFileArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type WriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type RunShellArgs struct {
	Command  string `json:"command"`
	TimeoutS *int   `json:"timeout_s"`
}
```

Decoding uses `json.Decoder` with `DisallowUnknownFields` turned off, since a small model
routinely invents a field and the call should still run. A missing required field returns a
one-line error that becomes the tool result: `read_file needs a path`.

### 6.2 JSON schemas

```json
{
  "type": "function",
  "function": {
    "name": "read_file",
    "description": "<contents of tool_read_file.md>",
    "parameters": {
      "type": "object",
      "properties": {
        "path":   {"type": "string",  "description": "Path relative to the project root."},
        "offset": {"type": "integer", "description": "First line to return, 1-based.", "minimum": 1},
        "limit":  {"type": "integer", "description": "Number of lines to return. Default 400.", "minimum": 1}
      },
      "required": ["path"],
      "additionalProperties": false
    }
  }
}
```

```json
{
  "type": "function",
  "function": {
    "name": "write_file",
    "description": "<contents of tool_write_file.md>",
    "parameters": {
      "type": "object",
      "properties": {
        "path":    {"type": "string", "description": "Path relative to the project root."},
        "content": {"type": "string", "description": "The full file contents."}
      },
      "required": ["path", "content"],
      "additionalProperties": false
    }
  }
}
```

```json
{
  "type": "function",
  "function": {
    "name": "run_shell",
    "description": "<contents of tool_run_shell.md>",
    "parameters": {
      "type": "object",
      "properties": {
        "command":   {"type": "string",  "description": "The command line to run."},
        "timeout_s": {"type": "integer", "description": "Seconds before the command is killed. Default 120.", "minimum": 1}
      },
      "required": ["command"],
      "additionalProperties": false
    }
  }
}
```

The schemas are built once at init into `json.RawMessage` values, so they are marshalled
without re-encoding on every turn.

### 6.3 Path guard

```go
// resolve turns a tool path argument into an absolute path inside Root, or
// returns the message the model is shown.
func (r *Runner) resolve(p string) (string, error)
```

Algorithm:

1. Reject an empty path: `path is empty`.
2. Reject a path containing a NUL byte: `path contains a null byte`.
3. If `filepath.IsAbs(p)`, keep it. Otherwise `p = filepath.Join(r.Root, p)`.
4. `p = filepath.Clean(p)`.
5. Split `p` into the longest existing prefix and a non-existent tail, by walking up with
   `os.Lstat` until a component exists. In practice this is at most a handful of `Lstat`
   calls because a write target's parent usually exists.
6. `real, err := filepath.EvalSymlinks(existingPrefix)`. An error other than
   `fs.ErrNotExist` is returned as `resolve <path>: <err>`.
7. Reject if any component of the non-existent tail is `..`. After step 4 this can only
   happen when the path started outside the root, which step 8 also catches, but the check
   is cheap and gives a clearer message.
8. `resolved := filepath.Join(real, tail)`. `rel, err := filepath.Rel(r.Root, resolved)`.
   Reject when `err != nil`, when `rel == ".."`, or when
   `strings.HasPrefix(rel, ".."+string(filepath.Separator))`.
9. Return `resolved`.

Refusal message, identical for every cause so a probing model learns nothing from the
difference: `path is outside the project: <original argument>`.

Symlink notes:

- `Runner.Root` is itself symlink-resolved once at construction. On macOS this matters:
  `/tmp` is a symlink to `/private/tmp`, and a project under `/tmp` would otherwise fail
  every comparison.
- A symlink inside the project that points outside it is refused, because step 6 resolves it
  and step 8 then sees a path outside the root.
- A symlink inside the project that points inside it is allowed and is followed.
- `write_file` to a path whose final component is an existing symlink writes through the
  symlink to its target, and the target is subject to the same check because the final
  component is part of the existing prefix in step 5.
- The check is done once per call, immediately before the operation. There is no protection
  against a symlink being swapped in between the check and the open. That race is accepted:
  the tools run as the user, on the user's own machine, on a directory the user chose.

### 6.4 read_file

1. Resolve the path.
2. `os.Stat`. A directory returns `<path> is a directory`. A file larger than 64 MiB returns
   `<path> is <n> MB, too large to read`.
3. Open and read the first 8192 bytes for binary detection (section 6.7). On detection,
   return `<path> looks like a binary file, not read`.
4. Seek back to 0. Scan lines with `bufio.Scanner`, buffer cap 1 MiB. A line longer than the
   cap is truncated at the cap with ` ...` appended and scanning continues.
5. `offset` defaults to 1, `limit` defaults to `ReadLines` (400). Lines before `offset` are
   counted and discarded.
6. Emit lines as `%6d\t%s\n` with the original 1-based line number. Stop after `limit` lines
   or after the accumulated output reaches `ReadLimit` (64 KiB), whichever comes first.
7. When the file has more lines after the emitted range, append a final line
   `... <n> more lines, use offset <m>`. When the read stopped on the byte limit rather than
   the line limit, the appended line is `... stopped at 64 KB, use offset <m>`.
8. An empty file returns `<path> is empty`.

### 6.5 write_file

1. Resolve the path.
2. `os.MkdirAll(filepath.Dir(resolved), 0755)`.
3. `config.WriteAtomic(resolved, []byte(content), 0644)`. When the file already exists, its
   current mode is read first and preserved instead of 0644.
4. Result output: `wrote <rel> (<n> bytes, <m> lines)` where `rel` is the path relative to
   the project root. When the file existed, the word is `replaced` rather than `wrote`.

There is no diff and no backup. The tool description says the file is replaced whole.

### 6.6 run_shell

```go
cmd := exec.CommandContext(ctx, r.Shell, "-c", args.Command)
cmd.Dir = r.Root
cmd.Env = os.Environ()
cmd.Stdin = nil
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
```

`SysProcAttr` lives in `run_shell_unix.go` behind `//go:build darwin || linux`, which is the
whole supported set. There is no other build tag in the package.

Timeout: `t := args.TimeoutS` clamped to `[1, MaxTimeout]`, defaulting to `Runner.Timeout`.
`MaxTimeout` is `4 * Runner.Timeout` with a floor of 600 seconds, so a model asking for
`timeout_s: 100000` gets a bounded wait rather than a hang.

Kill semantics, identical on macOS and Linux:

1. `cmd.Cancel` is set to a function that sends `syscall.Kill(-cmd.Process.Pid,
   syscall.SIGTERM)`. The negative pid targets the process group created by `Setpgid`, so a
   shell that forked children takes the whole tree down rather than orphaning it.
2. `cmd.WaitDelay = 2 * time.Second`. If the group has not exited by then, `os/exec` closes
   the pipes and `Wait` returns; a second explicit `syscall.Kill(-pid, syscall.SIGKILL)` is
   issued from the same cancel path to make the kill unconditional.
3. Timeout and stop are the same mechanism: the call runs under a context derived from both
   the turn context and a `context.WithTimeout` for the requested seconds. Whichever fires
   first cancels.
4. `syscall.Kill` returning `ESRCH` is ignored; the group has already gone.

Output: stdout and stderr are both written into one `*limitBuffer` (section 6.7), so
interleaving is preserved in arrival order. The buffer is guarded by a mutex because
`os/exec` writes from two goroutines when the same writer is used for both streams.

Exit code: `cmd.ProcessState.ExitCode()`. A signalled process reports -1; the result line
then reads `exit code: killed by signal <name>`. A timeout appends
`\n\ncommand timed out after <n>s and was killed` before the exit line. Cancellation by the
Stop button appends `\n\nstopped` and returns a `Result` with `ExitCode` nil, which the agent
turns into a tool message so the transcript is complete.

Final output shape given to the model:

```
README.md
main.go

exit code: 0
```

### 6.7 Truncation and binary detection

```go
// Truncate keeps the head and the tail of s within limit bytes, cutting on
// line boundaries where one is close enough.
func Truncate(s string, limit int) (out string, truncated bool)
```

Algorithm:

1. If `len(s) <= limit`, return unchanged.
2. `head := limit * 2 / 3`, `tail := limit - head`.
3. Move the head cut forward to the next `\n` within 512 bytes; if there is none, cut where
   it is and back off to the previous UTF-8 rune boundary.
4. Move the tail cut backward to the previous `\n` within 512 bytes; if there is none, cut
   where it is and advance to the next rune boundary.
5. Join with `"\n... " + strconv.Itoa(omitted) + " bytes omitted ...\n"`.

The returned string may exceed `limit` by the length of the marker. That is deliberate: the
limit governs retained output, not the total string, so the marker cannot be silently cut.

```go
// looksBinary reports whether the sniff is not text. A NUL byte settles it;
// otherwise invalid UTF-8 over 1 percent of the sniff does.
func looksBinary(sniff []byte) bool
```

1. `bytes.IndexByte(sniff, 0) >= 0` returns true.
2. Trim up to 3 trailing bytes that form an incomplete rune at the sniff boundary.
3. Decode runes; count `utf8.RuneError` with width 1. True when the count exceeds
   `len(sniff)/100` or when the count is at least 1 and `len(sniff) < 100`.

UTF-16 files are reported as binary. That is accepted.

---

## 7. internal/perms

### 7.1 Types

```go
package perms

// Action is what policy says about a call before any human is asked.
type Action string

const (
	ActionAllow  Action = "allow"
	ActionPrompt Action = "prompt"
)

type DecisionKind string

const (
	DecideOnce   DecisionKind = "once"
	DecideAlways DecisionKind = "always"
	DecideDeny   DecisionKind = "deny"
)

// Request is one pending question about one tool call.
type Request struct {
	ID        string    `json:"id"`      // "perm_" + 12 random base32 chars
	SessionID string    `json:"session"`
	Project   string    `json:"project"`
	Tool      string    `json:"tool"`    // read_file, write_file, run_shell
	Command   string    `json:"command,omitempty"`
	Path      string    `json:"path,omitempty"`
	Prefix    string    `json:"prefix"`  // proposed "allow always" prefix
	Args      string    `json:"args"`    // raw arguments JSON, for display
	Created   time.Time `json:"created"`
}

// Decision is the answer.
type Decision struct {
	Kind   DecisionKind `json:"decision"`
	Prefix string       `json:"prefix,omitempty"` // used when Kind is always
	Reason string       `json:"reason,omitempty"` // used when Kind is deny
}
```

### 7.2 Broker

```go
// Broker turns a policy question into a pending request and blocks until the
// UI answers or the session is cancelled.
type Broker struct {
	cfg  *config.Store
	mu   sync.Mutex
	wait map[string]chan Decision // by request id
	byS  map[string][]string      // request ids by session id
	emit func(sessionID string, r Request)
}

func New(cfg *config.Store, emit func(string, Request)) *Broker

// Evaluate applies the static policy. It never blocks and never asks.
func (b *Broker) Evaluate(project, tool, command string, mode config.Mode) Action

// Ask registers a pending request, calls emit, and blocks. It returns
// context.Canceled when the turn is stopped and ErrSessionCanceled when the
// session's pending decisions were dropped.
func (b *Broker) Ask(ctx context.Context, r Request) (Decision, error)

// Resolve answers a pending request. It reports ErrNoSuchRequest when the id
// is unknown, which is what a double click on the card produces.
func (b *Broker) Resolve(id string, d Decision) error

// Pending lists unanswered requests for a session, for replay after a browser
// reconnect.
func (b *Broker) Pending(sessionID string) []Request

func (b *Broker) CancelSession(sessionID string)
func (b *Broker) CancelAll()
```

`Ask` registers a channel buffered to one under the lock, then selects on `ctx.Done()` and
that channel. `Resolve` sends on the same channel and deletes the map entry, both under the
lock. The buffer is what lets `Resolve` return without waiting for `Ask` to be scheduled, so
a resolve that races with a cancel either wins or reports `ErrNoSuchRequest`, and can never
block the HTTP handler.

`DecideAlways` calls `cfg.AddAllow(project, prefix)` before returning, so the allowlist is on
disk before the command runs. A write failure downgrades the decision to `DecideOnce` and
attaches a note that reaches the UI as an `error` event: `save allowlist: <err>: the command
ran, but the rule was not kept`.

### 7.3 Policy

```go
func (b *Broker) Evaluate(project, tool, command string, mode config.Mode) Action {
	if mode == config.ModeFull {
		return ActionAllow
	}
	auto := mode == config.ModeAllowlist
	switch tool {
	case "read_file":
		return ActionAllow
	case "write_file":
		if auto {
			return ActionAllow
		}
		return ActionPrompt
	case "run_shell":
		if !auto {
			return ActionPrompt
		}
		if hasOperator(command) {
			return ActionPrompt
		}
		if _, ok := b.cfg.AllowMatch(project, command); ok {
			return ActionAllow
		}
		return ActionPrompt
	}
	return ActionPrompt
}
```

`read_file` is always allowed because the path guard already confines it to the project root
and reading is the operation the model must do constantly for the loop to be worth running.

### 7.4 Operator rejection list

```go
var operators = []string{";", "&", "|", ">", "<", "`", "$(", "\n", "\r"}

func hasOperator(cmd string) bool {
	for _, op := range operators {
		if strings.Contains(cmd, op) {
			return true
		}
	}
	return false
}
```

Bare `&` covers `&&` and backgrounding, which would otherwise escape the process group wait
and the timeout. Bare `|` covers `||` and pipelines. `\r` is a line separator to some
shells. `${` is not on the list, so `echo ${HOME}` runs under an allowlist match; parameter
expansion cannot start a second command on its own.

It is a substring scan, not a parse: `echo "a > b"` contains `>` and therefore prompts even
though the redirect is quoted. Over-prompting is the intended direction of error.

### 7.5 Allowlist prefix matching

```go
func (s *Store) AllowMatch(project, command string) (string, bool)
```

1. Normalise the command: `strings.TrimSpace`, then collapse every run of spaces and tabs to
   a single space. The stored prefix is normalised the same way when it is added.
2. For each prefix for the project, longest first: match when `command == prefix`, or when
   `strings.HasPrefix(command, prefix)` and the byte at `len(prefix)` is a space. The
   trailing-space requirement is what stops `git status` from allowing `git statusfoo`, and
   what stops `rm` from allowing `rmdir`.
3. Return the matched prefix and true, or `"", false`.

Matching is per project. A prefix allowed in one project has no effect in another.

### 7.6 Default prefix offered by the card

```go
// DefaultPrefix is the first two whitespace-separated fields of the command.
func DefaultPrefix(cmd string) string
```

`go test ./...` gives `go test`. `ls` gives `ls`. The field is editable in the card, and what
is stored is what the user submits after the same normalisation as section 7.5. A submitted
prefix that is empty, or that is not a prefix of the command being decided, is rejected with
400 and the message `prefix "<p>" does not match the command: edit it or choose allow once`.

### 7.7 Permission modes

One global `permissions` field in `config.json`, picked from the top-bar menu. It is one
field and not a pair of booleans, so there is no fourth state for a layer to interpret:

| value | what runs without a card |
|-------|--------------------------|
| `ask` | reads only |
| `allowlist` | reads, `write_file`, and `run_shell` on an allowlisted prefix with no shell operator |
| `full` | everything |

`full` is answered before `Evaluate` looks at the tool name, so the allowlist, the operator
list and the card all stop applying, and an unknown tool is allowed through to the runner
that rejects it. Path confinement is unaffected: it lives in `internal/tools`, so `read_file`
and `write_file` still cannot leave the project root. `run_shell` only starts there, and can
leave. The interface asks for confirmation before switching to `full`, before any state is
written or sent.

The mode is read at the moment `Evaluate` is called, so changing it mid-turn affects the next
tool call. It is not per project and not per session: it persists in `config.json` until it
is changed, which the warning says.

### 7.8 What a denial returns to the model

The tool message content is exactly:

```
denied by user: <reason>
```

When the reason is empty, the content is `denied by user`. The message keeps the
`tool_call_id` of the refused call, so the assistant message and its result stay paired and
the history remains valid. The loop continues to the next iteration; a denial is not a
turn-ending event. The UI shows the permission card in its denied state with the reason
underneath, and no tool output card.

A tool call that arrived while the session was being stopped gets
`stopped by user` instead, and the turn then ends.

---

## 8. internal/agent

### 8.1 Types

```go
package agent

// Session is one conversation with one model in one project. It is the
// in-memory authority; store holds the same data on disk.
type Session struct {
	mu sync.Mutex

	ID      string
	Project string
	AgentID string // the agent this session last ran under
	Model   string
	Created time.Time
	Updated time.Time
	Title   string

	Entries []store.Entry // full transcript, including out-of-context entries

	// Accounting.
	NCtx           int    // resolved context size, 0 when unknown
	NCtxSource     string // "props", "meta", "args", ""
	LastUsage      *llm.Usage
	LastToolTokens int                 // schema cost inside LastUsage, 0 when none were sent
	ContextBase    *store.ContextBase  // measured system and schema cost, nil until measured
	Compactions    int

	state   State
	cancel  context.CancelFunc
	turnSeq int
}

// Turn is one Send or one Compact, from the user message to a terminal state.
type Turn struct {
	Session   *Session
	Seq       int
	Started   time.Time
	Iteration int
	State     State
	Err       error
}

// Engine owns every live session and dispatches turns.
type Engine struct {
	cfg      *config.Store
	store    *store.Sessions
	backends Backends
	perms    *perms.Broker
	hub      Publisher // implemented by server's SSE hub

	mu   sync.Mutex
	live map[string]*Session
}

// Backends is the model server for one agent. The engine holds this rather
// than a client, so a turn resolves the agent it runs against and two agents
// can be answering at once.
type Backends interface {
	For(a config.Agent) Backend
}

type Publisher interface {
	Publish(sessionID string, ev Event)
}

func NewEngine(cfg *config.Store, st *store.Sessions, backends Backends,
	pb *perms.Broker, hub Publisher) *Engine

func (e *Engine) Create(project string) (*Session, error)  // takes the active agent
func (e *Engine) Open(id string) (*Session, error)         // Get, and marks the model unread
func (e *Engine) Get(id string) (*Session, error)
func (e *Engine) Send(id, text string) error     // returns once the turn is started
func (e *Engine) Stop(id string) error
func (e *Engine) Compact(id string) error
func (e *Engine) SetAgent()                      // points every session at the active agent
func (e *Engine) Forget(agentID string)          // drops what was read from one agent
func (e *Engine) Delete(id string) error
func (e *Engine) State(id string) State
```

`ensureReady` is where the agent is read, once, at the start of a turn. The record and the
backend it resolves to are then held for the whole turn and passed down to the title
request, the compaction and the context measurement, so an edit landing between two steps
cannot change the prompt under the model, and a request built for one address is never
sent to another. `Forget` is what a moved address or key asks for: what answered before
may not be what answers next, so the window and the template capabilities are read again.

`Send` appends the user entry, persists, sets `StatePreflight`, starts a goroutine, and
returns. A message sent while a turn runs is queued and picked up when the step under way
has finished, the model read at the top of that turn included. `Resend` answers `ErrBusy`
for anything that is not idle, which the server maps to 409.

`Open` is `Get` followed by `session.reopen`, which marks the model unread, and it is what
`GET /api/sessions/{id}` calls. Nothing is sent: a model server can be restarted between two
opens and a router holds nothing after that, so what the harness read before is a guess as
soon as it looks away, and the turn the next message starts reads it again. Every other way
of reaching a session uses `Get` and marks nothing.

`pointTo` moves a session onto an agent and a model and forgets what the last one's
tokenizer implied. `SetAgent` runs it over every live session and stops there; `ensureReady`
runs it at the top of a turn and reads the model when the session has not read it.

`DELETE /api/sessions/{id}` needs an idle session, since anything else is a turn to stop
first, and answers 409. The session is marked gone as it goes, so a turn that started
between the check and the delete does not write its file back out.

`Get` resolves in this order, holding the engine lock:

1. `e.live[id]`, returned as it is.
2. `e.store.Load(id)`. On success the session is put into `e.live` and returned, so the next
   `Get` is a map hit and there is only ever one `*Session` per id in the process.
3. `ErrNotFound`, which the server maps to 404.

Nothing is evicted from `e.live`. A session stays resident for the life of the process once
it has been opened, which is what makes the per-session mutex meaningful and what lets the
SSE hub publish into a session no request is currently touching. The memory cost is the
transcript, and a process holding every session a person opened in one sitting is not a
size worth managing.

`SetAgent` is called by the agent routes whenever the active agent changes or is edited. It
is described in 8.5.

`Delete` requires `StateIdle`, removes the file through `store.Delete`, removes the live
entry, and drops the hub channel for the id.

### 8.2 States

```go
type State string

const (
	StateIdle      State = "idle"
	StateLoading   State = "loading"
	StatePreflight State = "preflight"
	StateCompacting State = "compacting"
	StateStreaming State = "streaming"
	StateDeciding  State = "deciding"
	StateExecuting State = "executing"
	StateFinal     State = "final"
	StateStopped   State = "stopped"
	StateFailed    State = "failed"
	StateLimit     State = "limit"
)
```

| state | entered when | work done | leaves to |
|-------|--------------|-----------|-----------|
| `idle` | any terminal state completes | none | `preflight` on Send, `compacting` on Compact |
| `loading` | a turn found its model unread | read the model: its window and its chat template | `preflight` when it answers; `failed` when it does not |
| `preflight` | top of each loop iteration | check iteration count, compute fill, compare with `compact_at * n_ctx`, build the request | `limit` when iteration is 50; `compacting` when over threshold; `streaming` otherwise |
| `compacting` | threshold hit, or Compact pressed | run the compaction request, rewrite the in-context set | `preflight` when it came from the loop; `idle` when it came from the Compact button; `failed` on error. Stop is refused here |
| `streaming` | request sent | read the SSE stream, publish `delta`, `reasoning`, `tool_call`, `usage` | `final` when no tool calls; `deciding` when there are; `stopped` on cancel; `failed` on error |
| `deciding` | assistant asked for tools | for each call in order: `Evaluate`, then `Ask` when it says prompt | `executing` for the call just decided; `stopped` on cancel |
| `executing` | a call was allowed or denied | run the tool or synthesise the denial, append the tool entry | `deciding` for the next call; `preflight` when the calls are exhausted |
| `final` | assistant replied with no tool calls | append the assistant entry, persist, publish `done` | `idle` |
| `stopped` | Stop pressed, or shutdown | kill the command, cancel pending decisions, append and publish a stopped marker, persist, send any queued message | `idle` |
| `failed` | any non-cancel error | publish `error` and `done`. The message is not stored: it belongs to the request that failed | `idle` |
| `limit` | 50 iterations without a plain reply | append `turn limit reached`, persist, publish `error` and `done` | `idle` |

Every state change publishes `{"type":"status","state":"<state>"}` on the session's SSE
stream. The UI drives its spinner and its Stop button off that one event.

### 8.3 The loop

```
Send(text):
  append Entry{Kind: user, Content: text, InContext: true, Visible: true}
  persist
  go run(turn)

run(turn):
  ctx, cancel = context.WithCancel(engine root ctx); session.cancel = cancel
  ensureReady(ctx)                              // take cfg.Model, read it if new
  cfg = config.Config()                         // read once, hold for the turn
  model, caps = session.Model, session.caps     // the same, for the same reason
  titleSession(ctx)                             // first turn only, before the reply
  for iteration = 0; iteration < 50; iteration++ {
      state = preflight
      fill = computeFill()
      if session.NCtx > 0 && float64(fill) >= cfg.CompactAt*float64(session.NCtx) {
          state = compacting
          if err := compact(ctx); err != nil { fail(err); return }
      }
      state = streaming
      req = llm.ChatRequest{
          Model:    model,
          Messages: session.contextMessages(),
          Tools:    tools.Schemas(),
          Effort:   cfg.Effort,
          Caps:     caps,
      }
      res, err = client.Chat(ctx, req, publishEvent)
      if err != nil { classify: canceled -> stop(); else fail(err); return }
      if res.Usage != nil {
          session.LastUsage = res.Usage
          session.LastToolTokens = tools were sent ? ContextBase.Tools : 0
          publish usage
      }
      if len(res.ToolCalls) == 0 {
          append Entry{Kind: assistant, Content: res.Content, Reasoning: res.Reasoning,
                       InContext: true, Visible: true}
          state = final; persist; probeContextBase(); publish done; return
      }
      append Entry{Kind: assistant, Content: res.Content, Reasoning: res.Reasoning,
                   ToolCalls: res.ToolCalls, InContext: true, Visible: true}
      persist
      for _, call := range res.ToolCalls {
          state = deciding
          decision = decide(ctx, call)      // may block on the UI
          state = executing
          out = execute(ctx, call, decision)
          append Entry{Kind: tool, ToolCallID: call.ID, ToolName: call.Function.Name,
                       Content: out.Output, ExitCode: out.ExitCode,
                       Truncated: out.Truncated, InContext: true, Visible: true}
          persist
          if stopped { state = stopped; return }
      }
  }
  state = limit
  append Entry{Kind: error, Content: "turn limit reached after 50 model calls"}
  persist; publish done
```

The assistant entry is appended before the tools run, so a crash between the model reply and
the tool result leaves a history whose last message is an assistant with unanswered
`tool_calls`. On the next `Send`, `contextMessages` detects that and synthesises a tool
message `not run: the session was interrupted` for each unanswered call, which keeps the
request valid.

### 8.4 Cancellation

`Stop` does three things under the session lock: calls `session.cancel()`, calls
`perms.CancelSession(id)`, and publishes `status: stopped`. The running `llm.Chat` sees a
closed body and returns `KindCanceled`. A running shell command sees its context cancelled
and its process group killed. A blocked `Ask` returns `context.Canceled`.

The goroutine that owns the turn is the only writer of `session.Entries`. `Stop` never
appends; it lets the loop reach `StateStopped` and append the marker itself, so entry order
is always the order one goroutine wrote them.

Stop on an idle session is a no-op returning 200.

### 8.5 Token fill accounting

```go
// computeFill estimates tokens currently in context. The last usage report is
// authoritative for everything up to the response it came from; entries added
// since then are estimated at four characters per token.
func (s *Session) computeFill() int {
	if s.LastUsage == nil {
		return s.estimateAll()
	}
	base := s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens
	return base + s.estimateSince(s.lastUsageEntry)/4
}
```

`estimateAll` sums `len(Content) + len(marshalled ToolCalls)` over in-context entries and
divides by 4. `lastUsageEntry` is the index of the assistant entry the usage arrived with.

This matters because a tool result of 16 KiB adds roughly 4000 tokens that no usage report
has counted yet, and the threshold check at the top of the next iteration has to see them.

`NCtx` resolution, best available first:

1. `Prepare(model).NCtx`. Source `"props"`.
2. `Model.CtxHint` with source `"meta"`.
3. `Model.CtxHint` with source `"args"`.
4. 32768 with source `"assumed"`. Automatic compaction stays armed against the assumed
   value, and the meter reads `<fill> / ~32k` so the number is visibly a guess.

**Preparation.** Nothing about a model can be read until it is loaded, because the chat
template is inside the model file, and a model-scoped read carrying `autoload=false` answers
`400 model is not loaded`. So the window and the template arrive together or not at all, and
they arrive when a turn needs them.

`Backend.Prepare(ctx, model)` is that step. It is the only request allowed to name a model
with autoload on. Behind the interface:

- Read `/props` unscoped. A server that does not answer `role: "router"` holds one model
  and it is already up, so that response is the answer.
- On a router, take the model's status from the cached list. Loaded means a read with
  `autoload=false`. Anything else means the same request with autoload on, which loads the
  model and then answers for it.

It returns `llm.Ready{NCtx, Source, Caps}`, and the engine writes all three onto the
session. A backend that keeps everything loaded implements `Prepare` as a read and no
caller can tell the difference.

`run` calls `ensureReady` at the top of every turn and that is the only place it is called
from, so a model is loaded by a message rather than by a choice made in a menu. The session
sits in `loading` while the read runs, bounded by `loadTimeout` because the work is a file
being read into memory on another machine. A read whose model changed under it discards its
result. A message typed meanwhile joins the queue of the turn doing the reading.

Caps decide whether the effort mapping uses the effort column or the budget column and
whether `preserve_reasoning` is sent. Those, the settings and the model itself are all
inputs to the rendered prompt, so a turn that changed one would render a different prompt
from the step before it and the model server would reprocess everything before the change.
`run` reads all three once at the top and holds them for the whole turn. A model chosen
while a turn runs takes effect on the next message.

**Naming.** A session with no typed title asks the model for one, from a request carrying
the opening message and `title.md` alone, with thinking off and a 32 token cap. It runs
before the turn that answers the message, not after it. The model server holds one prompt at
a time, so a naming request made after the turn would evict what the turn left cached and
the next message would reprocess the conversation from the start. Made first, it runs
against a slot holding nothing worth keeping. A failure is not an error: the session keeps
the title cut from the message.

**One agent.** The agent lives in `config.json` and nowhere else. A session records the one
it last ran under, which is a note rather than a choice: `ensureReady` compares it with the
active agent at the top of every turn and repoints the session when they differ. Selecting
or editing an agent calls `Engine.SetAgent`, which repoints every live session and sends
nothing. Naming a model with autoload on costs minutes of another
machine's memory, so a person reading down the model list loads none of them and the first
message that needs one is what asks for it. `session.repoint` sets `session.Model`, clears
`NCtx`, `NCtxSource`, `caps` and `prepared` so the new model is read before it is used, sets
`session.LastUsage = nil` so the next fill is re-estimated from the entries rather than
carried over from a different model's tokenizer. The transcript says nothing about the
change; the model chip already shows which model the session is on. The measurement of
section 8.6 is discarded with the rest, since it was taken against the previous tokenizer. The session is then persisted and the
`Props` probe above runs.

### 8.6 The context breakdown

`computeFill` answers how full the window is. What the window is full of is a separate
question, and the model server answers it with one number for the whole prompt. The two
parts that never change within a session are measured instead:

```go
count := func(schemas []llm.Tool) int {
	res, _ := client.Chat(ctx, llm.ChatRequest{
		Model: model, Messages: []llm.Message{system}, Tools: schemas,
		Effort: llm.EffortOff, Caps: caps, MaxTokens: 1}, nil)
	return res.Usage.PromptTokens
}
bare, armed := count(nil), count(tools.Schemas())
ContextBase{Model: model, System: bare, Tools: armed - bare}
```

Both completions carry the system message and differ only in whether the schemas are
attached, so `bare` is the system cost including whatever scaffolding the template puts
around it, and the difference is the schema cost. The measurement is stored against the
model it was taken against; `repoint` discards it along with `LastUsage` and the caps.

Everything else is then subtraction on a real usage report:

```go
func (s *Session) breakdown() *Breakdown {
	if s.LastUsage == nil || s.ContextBase == nil || s.ContextBase.Model != s.Model {
		return nil
	}
	b := Breakdown{System: s.ContextBase.System, Tools: s.LastToolTokens}
	b.Messages = s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens - b.System - b.Tools
	if b.Messages < 0 {
		return nil
	}
	return &b
}
```

`LastToolTokens` rather than `ContextBase.Tools` because a turn that spends its call budget
sends no schemas, and that turn's prompt did not carry their cost. All three parts are
counts the model server gave, not characters divided by four. A remainder below zero means
the measurement no longer describes the prompt, and reports nothing rather than a number.

`probeContextBase` runs after a turn has streamed rather than before one, since a completion
loads a model and the first chat message is the only request allowed to do that. It runs at
most once per model per session, whether or not it succeeded, so a server that will not
answer it is not asked again every turn. Until it has answered there is no breakdown, and
the meter shows the unsegmented bar.

Against `Qwen3.8-27B Q5` the measurement is 154 tokens of system prompt and 1026 of schemas,
1180 of a 262144-token window.

### 8.7 Effort mapping

| UI level | caps known and `supports_reasoning_effort` | otherwise |
|----------|--------------------------------------------|-----------|
| auto | nothing sent | nothing sent |
| off | `reasoning_budget: 0`, `chat_template_kwargs: {"enable_thinking": false}` | same |
| low | `reasoning_effort: "low"` | `reasoning_budget: 2048` |
| medium | `reasoning_effort: "medium"` | `reasoning_budget: 8192` |
| high | `reasoning_effort: "high"` | `reasoning_budget: -1` |

`reasoning_format: "deepseek"` on every request in every row. The budget constants are Go
constants in `internal/llm`, not configuration.

The model is read at the top of the turn, before the first request is built, so the column
is chosen from what its template actually reports rather than from a default. For
`Gemma4-12B` as captured that is the budget column, because it reports
`supports_reasoning_effort: false`.

### 8.8 Compaction

Triggers:

- Automatic: `computeFill() >= compact_at * NCtx` evaluated in `preflight`, so it can fire
  between two tool calls of the same user turn as well as before the first model call.
- Manual: `POST /api/sessions/{id}/compact` on an idle session.

Procedure:

1. Publish `status: compacting`.
2. Build the request: `messages = contextMessages() + [{role: "user", content:
   prompts.Compact()}]`, `tools` omitted, `Effort: off`, `stream: true` (the stream is read
   but no `delta` events are published; the UI shows a status row instead).
3. Send with the session's normal model. A failure moves the turn to `failed` and leaves the
   history untouched, so a failed compaction never loses context. Stop is refused while this
   runs: `POST /api/sessions/{id}/stop` answers 409 and the request is left to finish.
4. On success, take `res.Content` as the summary. An empty summary is treated as a failure
   with the message `the model returned an empty summary: try again or start a new session`.
5. Under the session lock: set `InContext = false` on every existing entry, then append

   ```go
   store.Entry{
       Kind:      "compaction",
       Role:      "user",
       Content:   "Summary of earlier work:\n" + summary + "\n\nContinue.",
       InContext: true,
       Visible:   true,
       Meta:      map[string]any{"before": fillBefore, "after": fillAfter, "n": session.Compactions + 1},
   }
   ```

   The system entry is regenerated rather than kept: `contextMessages` always emits the
   system prompt first from `prompts.System`, so it is never part of the entry list.
6. `session.Compactions++`, `session.LastUsage = nil` so the next fill is estimated rather
   than carried over from before the compaction, and `lastUsageEntry = len(Entries) - 1`.
7. Persist.
8. Publish `{"type":"compacted","before":<int>,"after":<int>,"summary":"..."}`.
9. A message typed while the summary was being written was queued against the compaction and
   has no turn to join, so it is sent as an ordinary message once the session is idle. A
   compaction that failed or was stopped sends it too.

What the UI keeps versus what the model sees:

- The UI keeps every entry, in order, forever. The `compaction` entry renders as a divider
  labelled `compacted, 96k to 3k tokens` with the summary collapsed underneath it.
- The model sees `contextMessages()`, which is the system prompt followed by every entry
  with `InContext == true`. After a compaction that is one user message.
- `GET /api/sessions/{id}` returns every entry with its `in_context` flag, so a reloaded
  page reconstructs the same view.

`contextMessages` also enforces two invariants before returning:

- Every assistant entry with `ToolCalls` is followed by one tool message per call. Missing
  ones are synthesised as described in 8.3.
- No tool message appears without its assistant. A compaction can only drop whole prefixes,
  so this holds by construction, but the check is cheap and a violation is a bug worth
  catching in a test rather than at the model.

`Reasoning` is never included in `contextMessages`. It is stored, shown, and dropped from
the request.

### 8.9 Persistence points

The session file is written at each of these, and nowhere else:

1. After `Create`.
2. After the user entry is appended in `Send`.
3. After the assistant entry is appended, whether or not it carries tool calls.
4. After each tool entry.
5. After a compaction rewrites the in-context flags.
6. On reaching `final`, `stopped`, `failed` or `limit`.

Streaming deltas are not persisted. A crash mid-stream loses the partial assistant message,
which is the correct outcome because a partial message with no finish reason is not
something to resume from.

Writes go through `store.Sessions.Save`, which serialises per session and uses the atomic
replace. A save failure publishes an `error` event and does not stop the turn.

### 8.10 Concurrent turns

Turns are not serialised. Two sessions can stream at once, and the engine does nothing to
prevent it.

The captured server reports `total_slots: 2` for the loaded model and `--parallel 1` for
four of the six presets. When every slot is busy, llama.cpp queues the request rather than
refusing it: the POST is accepted, headers come back, and no chunk arrives until a slot
frees. The visible effect is a session sitting in `status: streaming` with no `delta`
events, for as long as the other turn takes.

That is the documented behaviour, not a defect to work around. The UI shows the streaming
spinner and the Stop button, and Stop cancels the queued request the same way it cancels a
running one. Nothing is built for this.

---

## 9. internal/store

### 9.1 Entry

```go
package store

// Entry is one row of a session. Kind drives rendering; InContext drives what
// is sent to the model.
type Entry struct {
	Seq       int    `json:"seq"`
	Kind      string `json:"kind"` // user, assistant, tool, compaction, status
	Role      string `json:"role"` // OpenAI role, empty for non-message kinds

	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`

	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
	ExitCode   *int             `json:"exit_code,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`

	Permission *PermissionRecord `json:"permission,omitempty"`

	InContext bool           `json:"in_context"`
	Visible   bool           `json:"visible"`
	At        time.Time      `json:"at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// PermissionRecord is the decision as it was made, kept so a reloaded page
// shows the card in its answered state.
type PermissionRecord struct {
	ID       string `json:"id"`
	Tool     string `json:"tool"`
	Command  string `json:"command,omitempty"`
	Path     string `json:"path,omitempty"`
	Decision string `json:"decision"` // once, always, deny, auto, full, stopped
	Prefix   string `json:"prefix,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
```

`InContext` and `Visible` are written without `omitempty` so their false value is explicit in
the file and a reader never has to know the default.

### 9.2 File format

`~/.quecto-harness/sessions/<id>.json`, mode 0600, id is `sess_` plus 16 base32 characters
from `crypto/rand`.

```json
{
  "id": "sess_k4m9x2q7ta5vb1nc",
  "project": "/Users/jay/Projects/Personal/quecto-harness",
  "agent": "agt_2f7qk3m9x4pd8s1v",
  "model": "Gemma4-12B",
  "title": "list the files here",
  "created": "2026-09-04T12:26:11.412Z",
  "updated": "2026-09-04T12:28:02.907Z",
  "n_ctx": 131072,
  "n_ctx_source": "props",
  "compactions": 1,
  "last_usage": {
    "prompt_tokens": 4193,
    "completion_tokens": 118,
    "total_tokens": 4311,
    "cached_tokens": 3968
  },
  "last_tool_tokens": 1026,
  "context_base": {"model": "Gemma4-12B", "chars": 412, "system": 154, "tools": 1026},
  "entries": [
    {
      "seq": 1, "kind": "user", "role": "user",
      "content": "list the files here",
      "in_context": false, "visible": true,
      "at": "2026-09-04T12:26:11.412Z"
    },
    {
      "seq": 2, "kind": "assistant", "role": "assistant",
      "content": "",
      "reasoning": "The user wants a directory listing. run_shell with ls.",
      "tool_calls": [
        {"id": "call_0", "type": "function",
         "function": {"name": "run_shell", "arguments": "{\"command\":\"ls\"}"}}
      ],
      "in_context": false, "visible": true,
      "at": "2026-09-04T12:26:13.980Z"
    },
    {
      "seq": 3, "kind": "tool", "role": "tool",
      "tool_call_id": "call_0", "tool_name": "run_shell",
      "content": "AGENTS.md\nMakefile\ncmd\ndocs\ninternal\nweb\n\nexit code: 0",
      "exit_code": 0,
      "permission": {
        "id": "perm_7fq2ka9mzx01", "tool": "run_shell", "command": "ls",
        "decision": "always", "prefix": "ls"
      },
      "in_context": false, "visible": true,
      "at": "2026-09-04T12:26:19.204Z"
    },
    {
      "seq": 4, "kind": "compaction", "role": "user",
      "content": "Summary of earlier work:\nListed the repository root. Six entries, Go plus a web directory.\n\nContinue.",
      "in_context": true, "visible": true,
      "at": "2026-09-04T12:27:44.001Z",
      "meta": {"before": 104857, "after": 3120, "n": 1}
    },
    {
      "seq": 5, "kind": "assistant", "role": "assistant",
      "content": "The repository has cmd, internal, web and docs. Nothing is built yet.",
      "in_context": true, "visible": true,
      "at": "2026-09-04T12:28:02.907Z"
    }
  ]
}
```

The system prompt is not in the file. It is regenerated from `prompts.System` on every
request, so a change to the prompt, or to the active agent's instructions, takes effect on
an existing session.

`context_base` records what was measured as well as the numbers: the tokenizer that
counted it and the size of the system message it counted. Both have to still hold, since
the instructions an agent adds are part of that message.

### 9.3 API

```go
// Sessions is the on-disk session set. One file per session, one mutex per
// session id, so two sessions never serialise against each other.
type Sessions struct {
	dir string

	mu    sync.RWMutex
	index map[string]Summary
	locks map[string]*sync.Mutex
}

// Summary is what the sidebar needs, cheap enough to keep for every session.
type Summary struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	Agent    string    `json:"agent"`
	Model    string    `json:"model"`
	Title    string    `json:"title"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages int       `json:"messages"`
	Bytes    int64     `json:"bytes"`
}

func Open(dir string) (*Sessions, error)

func (s *Sessions) List(project string) []Summary // sorted by Updated, newest first
func (s *Sessions) Load(id string) (*Session, error)
func (s *Sessions) Save(sess *Session) error
func (s *Sessions) Delete(id string) error
```

`Open` reads every file in the directory once to build the index. A file that does not parse
is logged (`sessions/<name>: <err>: the file is skipped`) and skipped rather than fatal. A
file whose name does not match `sess_[a-z2-7]{16}\.json` is ignored.

`Title` is the first 60 runes of the first user entry, with newlines collapsed to spaces.

### 9.4 Listing cost

The index is built once at startup and updated on every `Save`, so `List` never touches the
filesystem. With a few hundred sessions the startup scan reads a few megabytes; that is
accepted. If the directory grows past a size where the startup scan is noticeable, the fix
is a single `index.json` written alongside, not a database. That is out of scope now.

### 9.5 Size

The dominant term is `write_file` content and `run_shell` output, both stored verbatim in
the entry. `run_shell` output is already capped at `output_limit_bytes` plus a marker.
`write_file` content is not capped: a 2 MB file written by the model produces a 2 MB entry,
and the file is rewritten in full on the next save.

Mitigation, in this version: the tool result stored for `write_file` is the summary line
(`replaced main.go (2103 bytes, 87 lines)`), not the content. The content only exists inside
the assistant entry's `tool_calls[].function.arguments`, where it is needed for the history
to be valid. A session that writes many large files will produce a large file and a slow
save. The threshold at which that becomes a problem is roughly 20 MB, and the mitigation
then is a new session.

`Save` marshals with `json.MarshalIndent(v, "", "  ")`. The readability is worth the size for
a file a person is expected to be able to open.

---

## 10. internal/server

### 10.1 Construction and middleware

```go
package server

type Server struct {
	cfg    *config.Store
	store  *store.Sessions
	engine *agent.Engine
	perms  *perms.Broker
	pool   *llm.Pool
	hub    *Hub
	token  string
	ui     http.Handler

	mu     sync.Mutex
	probes map[string]*probe
}

// probe is what one address last said about itself and what it holds. Keyed by
// address and key, so two agents at one address share a reading and a changed
// address is a different entry.
type probe struct {
	infoAt    time.Time
	info      llm.Info
	infoErr   error
	modelsAt  time.Time
	models    []llm.Model
	modelsErr error
}

func New(...) *Server
func (s *Server) Handler() http.Handler
```

`Handler` returns `http.NewServeMux` with Go 1.22 method-and-pattern routes, wrapped in one
chain: `recoverPanic -> logRequest -> checkOrigin -> auth -> mux`. `logRequest` writes one
line per request to stderr at `method path status duration`; SSE requests log on close with
the event count.

`recoverPanic` returns 500 with `{"error":"internal error"}` and logs the stack. A panic in
one handler never takes the process down, because a browser bug should not stop a running
turn.

### 10.2 Auth flow

1. The binary prints and opens `http://127.0.0.1:7331/?token=<token>`.
2. `GET /` with a `token` query parameter compares it to the process token with
   `subtle.ConstantTimeCompare`. On a match it sets

   ```
   Set-Cookie: qh=<token>; Path=/; HttpOnly; SameSite=Strict; Max-Age=2592000
   ```

   and replies 303 to `/`, so the token leaves the address bar and is not kept in history.
   `Secure` is not set, because the server is plain HTTP on loopback and a `Secure` cookie
   would never be stored.
3. `GET /` with no token and a valid cookie serves the UI.
4. `GET /` with neither serves a 200 page with one line: `open the URL printed by
   quecto-harness, or run quecto-harness open`. Not a 401, because a person typing the
   address by hand should get an instruction rather than a browser auth dialog.
5. Every `/api/*` request requires either the cookie or `Authorization: Bearer <token>`.
   Failure is 401 with `{"error":"not authorized: open the URL printed by quecto-harness"}`.

The token is read from `~/.quecto-harness/token` and survives a restart, so a tab left open
across a restart, including one caused by a service supervisor, keeps working. The cookie
has a 30 day `Max-Age`, so a tab reopened the next day also works.

A 401 therefore means the token file was deleted and a new one minted, or the cookie
expired. The UI treats it as a full-page state: it stops the event source and shows
`not authorized, run quecto-harness open`.

### 10.3 Origin and CORS

No `Access-Control-Allow-Origin` header is ever sent, so a cross-origin fetch from a page on
another site cannot read a response. `OPTIONS` on `/api/*` returns 405.

In addition, `checkOrigin` rejects with 403 any request that has an `Origin` header whose
value is not `http://<listen host>:<port>`, `http://localhost:<port>` or
`http://127.0.0.1:<port>`. This closes the gap where a cross-site form or an
`EventSource` from another origin would still reach the handler even though it could not
read the reply. `Sec-Fetch-Site: cross-site` is also rejected when present. Requests with no
`Origin` (curl, the browser's own top-level navigation) pass, and are still gated by the
token.

### 10.4 Routes

All request and response bodies are `application/json` unless stated. Errors are
`{"error":"<sentence>"}` with the status codes noted.

#### GET /api/state

```json
{
  "mode": "router",
  "build": "b10812-8b4b3558f",
  "agents": [
    {"id": "agt_2f7qk3m9x4pd8s1v", "name": "Local llama.cpp",
     "url": "http://192.168.2.205:8080", "key": "", "model": "Gemma4-12B",
     "effort": "medium", "preserve_reasoning": true,
     "temperature": 0.35, "top_p": null, "top_k": null, "repeat_penalty": null,
     "max_tokens": null, "seed": null, "instructions": ""}
  ],
  "agent": "agt_2f7qk3m9x4pd8s1v",
  "autoload": true,
  "detect_error": "",
  "detect_kind": "",
  "config": {
    "agent": "agt_2f7qk3m9x4pd8s1v",
    "permissions": "ask",
    "compact_at": 0.8,
    "shell": "/bin/sh",
    "command_timeout_s": 120,
    "output_limit_bytes": 16384,
    "max_tool_calls": 50,
    "listen": "127.0.0.1:7331"
  },
  "config_notes": [],
  "projects": ["/Users/jay/Projects/Personal/quecto-harness"],
  "models": [
    {"id": "Gemma4-12B", "status": "loaded", "ctx_hint": 131072, "ctx_source": "meta",
     "modalities": ["text", "image", "audio"], "failed": false},
    {"id": "Muse Glimmer 30B Q5", "status": "unloaded", "ctx_hint": 131072,
     "ctx_source": "args", "modalities": ["text", "image"], "failed": false}
  ],
  "models_error": "",
  "models_kind": "",
  "os": "darwin",
  "version": "0.1.0"
}
```

`agentJSON` is the one wire shape for an agent, and it carries the key. The harness started
write-only, answering with `key_set` alone, and gave that up so the dialog could offer an
ordinary password field: a field that cannot show what it holds cannot be cleared by
emptying it, and needed a `Remove key` button beside it to say what an empty field already
says. The key is readable by anything that can reach the API, which is the loopback address
the harness binds by default; `config.json` stays 0600.

`mode`, `build`, `autoload`, `detect_*`, `models` and `models_*` describe the **active
agent alone**. Probing every address on a state read would cost one call each, with a five
second timeout, to draw one chip. The agents dialog asks about the others itself, one at a
time, when a row is opened. With no agent selected, `mode` is `unknown`, `models` is empty
and `detect_error` says so.

The reading is cached for 15 seconds, keyed by address and key, so two agents at one
address share it and a moved address is a different entry. `detect_error` and
`models_error` carry the message from the taxonomy when the last attempt failed, and the
rest of the payload is still served, so a dead model server does not blank the UI.

#### GET /api/agents

```json
{"agents": [...], "agent": "agt_2f7qk3m9x4pd8s1v"}
```

#### POST /api/agents

Any subset of the writable fields; only `name` is required, and `url` may be empty for an
agent still being filled in. 201 with the list, the active id and `saved`, the record as
stored. The new agent is selected when nothing was selected.

- 400 `Name the agent: an agent needs a name`
- 409 `An agent named "Local llama.cpp" already exists: choose another name`
- 400 `Address "ftp://x" is not an http or https URL with a host`
- 400 `Effort "extreme" is not auto, off, low, medium or high`
- 400 `Temperature is outside 0 to 2: send null to leave it to the model server`
- 400 `Top p is outside 0 to 1: send null to leave it to the model server`

#### PUT /api/agents/{id}

Partial: a field the body leaves out keeps the value it has. `key` is the exception that
needs three states, and uses the same `patch[T]` generic as the sampling fields. **Absent
leaves the stored key alone**, which is what lets the browser save a form it was never
given the key for; `null` or `""` clears it; a string sets it. 200 with the same body as
POST, or 404 `No agent with that id: it may have been deleted`.

A write that moved the address or the key drops the cached reading for the old pair, calls
`Pool.Retain`, and calls `Engine.Forget`, since the sessions on that agent are holding a
window it may no longer have.

#### DELETE /api/agents/{id}

200 with the list and the active id. Deleting the active agent hands the turn to the first
one left; deleting the last leaves nothing selected, which is the state a first run is in
and what the setup screen answers. No cascade: `Session.Agent` keeps pointing at an id that
is gone, and the next turn moves that session onto the active agent and forgets what the
old one implied.

#### POST /api/agents/{id}/select

200 with the list and the active id. A half-configured agent may be selected; what it is
missing is reported when a message is sent, which is where the model was always reported
missing.

#### POST /api/agents/probe

Request `{"url": "...", "key": "..."}`, or `{"agent": "agt_x", "url": "..."}` to reuse a
stored key the browser was never given.

```json
{"mode": "openai", "build": "", "autoload": false,
 "detect_error": "", "detect_kind": "", "models": [],
 "models_error": "", "models_kind": ""}
```

**200 even when the probe failed**, carrying the message. That is what lets the dialog draw
a bad address inside the form rather than as a failed request, and it is how the model list
is filled for an agent that does not exist yet. 400 only when the URL is not http or https.

#### GET /api/models?refresh=1

The models at the **active** agent's address. It is read on every status event to redraw
the dots, and always wants the agent that is answering, so it takes no agent of its own;
everything else goes through the probe. 502 with the taxonomy message when the call fails,
409 when no agent is selected.

#### GET /api/models/props?id=<model id>

Against the active agent.

```json
{
  "model": "Gemma4-12B",
  "n_ctx": 131072,
  "total_slots": 2,
  "sleeping": false,
  "caps": {
    "known": true,
    "tool_calls": true,
    "parallel_tool_calls": true,
    "reasoning_effort": false,
    "system_role": true,
    "preserve_reasoning": false
  }
}
```

The model id is a query parameter rather than a path segment because ids contain spaces
(`Muse Glimmer 30B Q5`) and, in a Hugging Face deployment, slashes and colons
(`ggml-org/gemma-3-4b-it-GGUF:Q4_K_M`). A slash would split a `{id}` path segment.

The upstream call always carries `autoload=false`. A 4xx from upstream is translated to 409
with `{"error":"model \"<id>\" is not loaded: it loads on the first message"}`, which is a
normal state and not an error row in the UI.

#### GET /api/fs/dirs?path=

```json
{
  "path": "/Users/jay/Projects",
  "parent": "/Users/jay",
  "crumbs": [
    {"name": "/", "path": "/"},
    {"name": "Users", "path": "/Users"},
    {"name": "jay", "path": "/Users/jay"},
    {"name": "Projects", "path": "/Users/jay/Projects"}
  ],
  "dirs": [
    {"name": "Personal", "path": "/Users/jay/Projects/Personal"},
    {"name": "work", "path": "/Users/jay/Projects/work"}
  ]
}
```

Rules:

- Missing or empty `path` means the user's home directory.
- A relative path is rejected with 400 `path must be absolute`.
- The path is `filepath.Clean`ed and then `EvalSymlinks`ed. A path that does not resolve
  returns 404 `no such directory: <path>`.
- Only directories are listed. Entries starting with `.` are excluded. Symlinks that point
  at directories are listed and are shown with their link name; selecting one stores the
  resolved target.
- Entries are sorted case-insensitively.
- A directory that cannot be read returns 403 `cannot read <path>: check permissions`.
- There is no root restriction. The browser is how a project is chosen, so it has to be able
  to reach anywhere the user can.
- The listing is capped at 2000 entries with a `truncated: true` flag.

#### POST /api/projects

Request `{"path": "/Users/jay/src/thing"}`. Response is the updated `projects` array. 400
when the path is not an existing readable directory, 409 when it is already present.

#### DELETE /api/projects

Request `{"path": "/Users/jay/src/thing"}`. Response is the updated array. Deleting by path
rather than by index removes the race where the list changed between the render and the
click. The allowlist entries for that path are left on disk, so re-adding the project
restores its rules. They are reachable and removable through the two allowlist routes below,
which is where a rule kept for a project that no longer exists gets cleaned up.

#### GET /api/allowlist?project=

```json
{"project": "/Users/jay/src/x", "prefixes": ["git status", "go test", "ls"]}
```

Prefixes are returned longest first, the same order `AllowMatch` walks. An unknown project
returns an empty array rather than 404, since a project with no rules and a project that was
removed look the same on disk.

#### DELETE /api/allowlist

Request `{"project": "/Users/jay/src/x", "prefix": "go test"}`. Response is the updated
`{"prefixes": [...]}`. Removing a prefix that is not present is 200 with the unchanged list,
so a double click is harmless. This is the only caller of `config.Store.RemoveAllow`.

#### GET /api/sessions?project=

```json
{"sessions": [
  {"id":"sess_k4m9x2q7ta5vb1nc","project":"/Users/jay/src/x","model":"Gemma4-12B",
   "title":"list the files here","created":"2026-09-04T12:26:11.412Z",
   "updated":"2026-09-04T12:28:02.907Z","messages":5,"bytes":4183}
]}
```

Omitting `project` lists every session.

#### POST /api/sessions

Request `{"project": "/Users/jay/src/x"}`. The session takes the active agent and its
model. 400 when the project is not in the projects list, and 400 with the sentence naming
what is missing when no agent is selected, or the selected one has no address or no model.
Response is the created `Summary` plus `"state": "idle"`.

#### GET /api/sessions/{id}

```json
{
  "id": "sess_k4m9x2q7ta5vb1nc",
  "project": "/Users/jay/src/x",
  "agent": "agt_2f7qk3m9x4pd8s1v",
  "model": "Gemma4-12B",
  "state": "idle",
  "n_ctx": 131072,
  "n_ctx_source": "props",
  "fill": 4311,
  "breakdown": {"system": 154, "tools": 1026, "messages": 3131},
  "compactions": 1,
  "pending": [],
  "entries": [ ... as in section 9.2 ... ],
  "last_seq": 41
}
```

`breakdown` is absent until the parts have been measured, which is section 8.6.

`pending` carries any unanswered `perms.Request` for this session, so a page reload restores
the blocked composer. `question` is present only while a turn waits on `ask_question`, and
carries the id its answer is posted to plus the id of the call that asked; the question
itself is already on that call's row, so it is not repeated here. `last_seq` is the SSE sequence number at the moment the snapshot was
taken; the client opens its event source with `Last-Event-ID: <last_seq>` so nothing is
missed between the fetch and the subscribe.

#### POST /api/sessions/{id}/message

Request `{"text": "list the files here"}`. 202 with `{"state":"preflight"}` on acceptance.
409 `{"error":"the session is busy: stop the current turn first"}` when not idle. 400 on
empty text. The response returns as soon as the turn goroutine is started; everything else
arrives on the event stream.

#### POST /api/sessions/{id}/stop

Empty body. 200 `{"state":"stopped"}` or `{"state":"idle"}` when nothing was running. 409
while the session is compacting, which is the one state a stop does not reach.

#### POST /api/sessions/{id}/compact

Empty body. 202 `{"state":"compacting"}`. 409 when the session is not idle.

#### PUT /api/sessions/{id}

Request `{"title": "Digits of pi"}`. Renames the session. An empty title hands the name back
to the harness, which takes it from the first message. Response:

```json
{"id": "sess_k4m9x2q7ta5vb1nc", "title": "Digits of pi"}
```

The agent is not a session's to change: it is one field in `config.json`, and selecting
another moves every session at once. The `status` entry that records the change
arrives on each session's event stream, so an open transcript shows it without a refetch.

#### DELETE /api/sessions/{id}

Empty body. 204 on success: the file is removed, the live session is dropped from the
engine, and the hub channel for the id is discarded so any attached event source ends. 409
when the session is not idle. 404 when the id is unknown. There is no undo and no trash;
the file is gone.

#### POST /api/permissions/{id}

```json
{"decision": "always", "prefix": "go test"}
```

```json
{"decision": "deny", "reason": "wrong directory"}
```

200 `{"ok": true}`. 404 `{"error":"that decision is no longer pending: the turn moved on"}`
when the id is unknown, which is what a second click produces. 400 when the prefix does not
match the command being decided.

#### POST /api/questions/{id}

```json
{"options": [1], "text": ""}
```

```json
{"options": [], "text": "neither, drop the cache"}
```

Answers the question a turn is blocked on. `options` are indexes into the options the call
offered; `text` is an answer nobody offered. 200 `{"ok": true}`. 404 when the id is unknown,
which is what a second click produces. 400 for an index that is not on the question, for
more than one index on a question that is not `multi_select`, and for an answer that is
neither an index nor any text.

#### PUT /api/config

Request carries only the fields being changed:

```json
{"permissions": "allowlist", "compact_at": 0.7, "max_tool_calls": 25}
```

Response is the same shape as `state.config`. This route holds what belongs to the harness
rather than to one connection: `permissions`, `compact_at`, `shell`, `command_timeout_s`,
`output_limit_bytes`, `max_tool_calls` and `listen`. The address, the key, the model, the
sampling and the thinking are an agent's and go through the agent routes. Changing `listen`
is accepted and persisted but takes effect on the next start; the response includes
`"note": "Listen changes take effect on restart"`.

#### GET /api/events?session=<id>

`Content-Type: text/event-stream`, `Cache-Control: no-store`,
`X-Accel-Buffering: no`, `Connection: keep-alive`. Each event is written as

```
id: 128
event: delta
data: {"type":"delta","seq":128,"text":"Hello"}

```

`event:` and the `type` field carry the same word, so a client can use either
`addEventListener` or a single `onmessage` with a switch. A comment line `:ping` is written
every 20 seconds to keep intermediaries and idle-timeout logic from closing the connection.

Event payloads:

```json
{"type":"status","seq":120,"state":"streaming"}
{"type":"delta","seq":121,"text":"Reading"}
{"type":"reasoning","seq":122,"text":"The user wants"}
{"type":"tool_call","seq":123,"index":0,"id":"call_0","name":"run_shell","arguments":"{\"comm"}
{"type":"permission","seq":124,"id":"perm_7fq2ka9mzx01","tool":"run_shell","command":"ls","prefix":"ls","args":"{\"command\":\"ls\"}"}
{"type":"tool_output","seq":125,"index":0,"id":"call_0","name":"run_shell","output":"AGENTS.md\n...","exit_code":0,"truncated":false}
{"type":"usage","seq":126,"prompt_tokens":4193,"completion_tokens":118,"cached_tokens":3968,"fill":4311,"n_ctx":131072,"breakdown":{"system":154,"tools":1026,"messages":3131}}
{"type":"compacted","seq":127,"before":104857,"after":3120,"summary":"Listed the repository root."}
{"type":"status_entry","seq":127,"content":"Stopped"}
{"type":"error","seq":128,"kind":"model_loading","message":"model \"Gemma4-12B\" is loading: send the message again in a moment"}
{"type":"done","seq":129,"finish_reason":"stop","iterations":2}
{"type":"resync","seq":130}
```

`tool_call` carries the argument fragment from one chunk, so the UI can show arguments
arriving. The complete call is also implied by the `tool_output` event that follows.

`index` is the join key, not `id`. The accumulated id is empty on the first fragments of a
call and some builds never send one at all, in which case the server substitutes
`call_<index>` only when the stream ends. A client that matched on `id` would open a second
card the moment the id appeared. `tool_output` carries both `index` and `id` so the same
match works and the id is available for display.

### 10.5 The SSE hub

```go
// Hub fans one session's events out to every attached browser and keeps a
// short replay buffer so a reconnect does not lose the middle of a turn.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*channel
}

type channel struct {
	seq    int64
	ring   []Event // capacity 512, oldest overwritten
	base   int64   // seq of ring[0]
	subs   map[int64]*subscriber
	nextID int64
}

type subscriber struct {
	ch     chan Event // buffered, capacity 256
	dropped bool
}

func (h *Hub) Publish(sessionID string, ev Event)
func (h *Hub) Subscribe(sessionID string, from int64) (<-chan Event, func(), []Event)
```

`Publish` assigns `seq`, appends to the ring, and does a non-blocking send to each
subscriber. A subscriber whose buffer is full is marked `dropped` and gets one `resync`
event; it is not disconnected, because a slow tab should recover rather than reload. The
next event it reads is `resync`, and the client responds by refetching
`GET /api/sessions/{id}` and reopening the stream with the returned `last_seq`.

`Subscribe(sessionID, from)` returns the replay slice for `(from, seq]` when `from >= base`,
or a single `resync` event when the requested sequence has already been overwritten. The
replay is written to the socket before the live channel is read, and the two cannot
interleave because `Subscribe` holds the lock while it snapshots.

Reconnect uses the standard `Last-Event-ID` request header, which `EventSource` sends
automatically after a dropped connection. An explicit `?from=<seq>` query parameter is also
accepted and takes precedence, which is what the client uses for the first connection after
a snapshot fetch.

Ring size 512 at roughly one event per token fragment covers a few seconds of streaming.
That is enough for a network blip and not enough for a laptop that slept for an hour, which
is exactly the case `resync` exists for.

The hub is per session. A browser watching two sessions opens two event sources. A session
with no subscribers still has its events published into the ring, which is what makes a
reconnect after `done` show the tail of the turn.

### 10.6 The UI handler and embed fallback

```go
package web

//go:embed all:dist
var dist embed.FS

// Handler serves the built UI, or a page explaining that it is not built.
func Handler() http.Handler
func Built() bool
```

`Built` reports whether `dist/index.html` exists in the embedded filesystem. The repository
keeps `internal/web/dist/.gitkeep` so `all:dist` always has a directory to embed and a
checkout without Node compiles.

When built: `http.FileServerFS(sub)` with a rewrite that serves `dist/index.html` for any
path that has no extension and does not start with `/api/`, so a client-side route survives
a reload. Assets under `/assets/` get `Cache-Control: public, max-age=31536000, immutable`
because Vite fingerprints them; `index.html` gets `Cache-Control: no-store`.

When not built: every UI path returns 200 with a self-contained page, no external assets:

```
quecto harness is running.

The web UI is not built. Run:

    make web

then restart, or use the JSON API at /api/state.
```

---

## 11. internal/service

### 11.1 Interface

```go
package service

type Spec struct {
	Binary string // resolved absolute path of the running executable
	Home   string // ~/.quecto-harness
	LogDir string // <Home>/log
	Path   string // PATH to give the unit
	Listen string // only written when it differs from the default
}

type Status struct {
	Installed bool
	Running   bool
	Unit      string // absolute path of the unit file
	Detail    string // raw first line from launchctl or systemctl
	URL       string // contents of <Home>/url, empty when absent
}

func Install(s Spec) error
func Uninstall(home string) error
func Report(home string) (Status, error)

// UnitText renders the unit without touching the filesystem, so its exact
// text can be asserted on any OS.
func UnitText(s Spec) (path string, body string, err error)
```

`UnitText` switches on `runtime.GOOS` and is the only place either format is written.
Two build-tag-free helpers, `plistText` and `systemdText`, are both compiled on both
platforms and both are exercised by the test suite, which is what allows the systemd path to
be verified on a Mac.

`Spec.Binary` comes from `os.Executable` followed by `filepath.EvalSymlinks`, so a binary
invoked through a `PATH` symlink records its real location.

`Spec.Path` is `/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin` with the directory of the
binary prepended when it is not already present. A service-managed process inherits almost
no environment, and `run_shell` needs a usable `PATH`.

### 11.2 launchd

Path: `~/Library/LaunchAgents/io.quecto.harness.plist`, mode 0644.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.quecto.harness</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Users/jay/go/bin/quecto-harness</string>
		<string>--no-open</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>WorkingDirectory</key>
	<string>/Users/jay/.quecto-harness</string>
	<key>StandardOutPath</key>
	<string>/Users/jay/.quecto-harness/log/harness.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/jay/.quecto-harness/log/harness.err</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>/Users/jay</string>
		<key>PATH</key>
		<string>/Users/jay/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
</dict>
</plist>
```

`--listen <addr>` is appended to `ProgramArguments` when `Spec.Listen` is non-empty.
Every string is escaped for XML with `xml.EscapeText`, so a home directory containing an
ampersand does not produce an invalid plist.

### 11.3 systemd

Path: `~/.config/systemd/user/quecto-harness.service`, mode 0644.

```ini
[Unit]
Description=quecto harness
After=network-online.target

[Service]
Type=simple
ExecStart=/home/jay/go/bin/quecto-harness --no-open
WorkingDirectory=/home/jay/.quecto-harness
Environment=PATH=/home/jay/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
Restart=on-failure
RestartSec=3
StandardOutput=append:/home/jay/.quecto-harness/log/harness.log
StandardError=append:/home/jay/.quecto-harness/log/harness.err

[Install]
WantedBy=default.target
```

A path containing a space is quoted with `systemd-escape` rules by wrapping the whole
`ExecStart` value in double quotes and escaping embedded quotes and backslashes.

### 11.4 Install algorithm

1. `os.Executable`, then `EvalSymlinks`. A binary under a temporary directory (a path with
   `/var/folders/` or `/tmp/` prefix) is refused: `the binary is at <path>: install it
   somewhere permanent first, for example /usr/local/bin`.
2. `paths.EnsureDirs()`, then `os.MkdirAll` on the unit's parent directory.
3. `UnitText(spec)`, then `config.WriteAtomic(path, []byte(body), 0644)`.
4. macOS:
   - `launchctl bootout gui/$UID/io.quecto.harness` and ignore its exit status, so a
     reinstall over a loaded agent works.
   - `launchctl enable gui/$UID/io.quecto.harness`. Enable acts on the service identifier
     and outlives a bootout, so it comes first; running it after bootstrap on a service that
     a previous `bootout` left disabled means bootstrap has already refused.
   - `launchctl bootstrap gui/$UID <plist path>`. A non-zero exit returns the combined
     output as the error: `load the launch agent: <output>`.
   - `launchctl kickstart gui/$UID/io.quecto.harness` to start it now.
5. Linux:
   - `systemctl --user daemon-reload`.
   - `systemctl --user enable --now quecto-harness`.
   - Print `if you want this running without a login session: loginctl enable-linger $USER`.
     This is a printed note, never executed, because it needs privileges the harness does
     not assume.
6. Poll `<Home>/url` for up to 5 seconds. When it appears, print
   `installed and running, open <url>`. When it does not, print
   `installed, but no URL appeared: check <LogDir>/harness.err`.

`$UID` is `os.Getuid()`, formatted, not read from the environment.

### 11.5 Uninstall algorithm

1. macOS: `launchctl bootout gui/$UID/io.quecto.harness`, ignoring an exit status that means
   "not loaded". Then remove the plist.
2. Linux: `systemctl --user disable --now quecto-harness`, ignoring "not loaded". Then remove
   the unit and `systemctl --user daemon-reload`.
3. Remove `<Home>/url`. Leave `token`, `config.json`, `allowlist.json`, `sessions/` and
   `log/` alone, and say so: `removed the unit. config, token and sessions are still in
   ~/.quecto-harness`.

### 11.6 Status algorithm

1. `os.Stat` on the unit path sets `Installed`.
2. macOS: `launchctl print gui/$UID/io.quecto.harness`. Exit 0 sets `Running` when the output
   contains a `pid = ` line; the `state = ` line becomes `Detail`.
3. Linux: `systemctl --user is-active quecto-harness`. Output `active` sets `Running`; the
   trimmed output becomes `Detail`.
4. Read `<Home>/url` into `URL`.

Printed form:

```
unit      /Users/jay/Library/LaunchAgents/io.quecto.harness.plist
state     running (pid 4711)
url       http://127.0.0.1:7331/?token=Vq3n8...
token     /Users/jay/.quecto-harness/token (delete to rotate)
logs      /Users/jay/.quecto-harness/log/harness.log
```

The URL is printed in full, token included, because the point of the command is to hand it
to a browser. The token line is there so that rotating it does not require reading this
document.

Exit code 0 when running, 3 when installed and not running, 4 when not installed, so the
command is usable from a shell script.

### 11.7 Logs

`~/.quecto-harness/log/harness.log` and `harness.err`. Neither is rotated by the harness.
The server writes one line per request and one line per turn to stderr, so growth is bounded
by use rather than by time; a note in the README says to truncate them if they matter. Both
are created by the init system, not by the binary.

---

## 12. Frontend

### 12.1 Files

```
web/
  index.html
  package.json
  vite.config.ts        build.outDir = "../internal/web/dist", emptyOutDir
  tsconfig.json         strict, noUncheckedIndexedAccess
  src/
    main.ts
    App.svelte
    app.css             @theme inline with the proxy palette, Poppins 400/500
    lib/api.ts          the Api interface and the fetch implementation
    lib/mock.ts         the mock implementation
    lib/events.ts       the ServerEvent union and the stream client
    stores/app.svelte.ts
    stores/session.svelte.ts
    components/Sidebar.svelte
    components/TopBar.svelte
    components/Transcript.svelte
    components/Composer.svelte
    components/FolderPicker.svelte
    components/PermissionCard.svelte
    components/ToolCard.svelte
    components/AgentChip.svelte
    components/Agents.svelte        the dialog
    components/AgentPanel.svelte    the parameters, shared with setup
    components/AgentModelField.svelte
    components/Modal.svelte
    components/ContextMeter.svelte
    components/SettingsPanel.svelte
    lib/overlay.ts        one Escape stack for every overlay
```

### 12.2 Event types

`src/lib/events.ts` mirrors section 10.4 exactly. The discriminant is `type`.

```ts
export type TurnState =
  | "idle" | "preflight" | "compacting" | "streaming"
  | "deciding" | "executing" | "final" | "stopped" | "failed" | "limit";

export type ServerEvent =
  | { type: "status";      seq: number; state: TurnState }
  | { type: "delta";       seq: number; text: string }
  | { type: "reasoning";   seq: number; text: string }
  | { type: "tool_call";   seq: number; index: number; id: string; name: string;
                           arguments: string }
  | { type: "tool_output"; seq: number; index: number; id: string; name: string;
                           output: string; exit_code: number | null; truncated: boolean }
  | { type: "permission";  seq: number; id: string; tool: string; command?: string;
                           path?: string; prefix: string; args: string }
  | { type: "usage";       seq: number; prompt_tokens: number; completion_tokens: number;
                           cached_tokens: number; fill: number; n_ctx: number;
                           breakdown?: { system: number; tools: number; messages: number } }
  | { type: "compacted";   seq: number; before: number; after: number; summary: string }
  | { type: "error";       seq: number; kind: string; message: string }
  | { type: "done";        seq: number; finish_reason: string; iterations: number }
  | { type: "resync";      seq: number };
```

### 12.3 Store shapes

```ts
// stores/app.svelte.ts
export type ModelRow = {
  id: string;
  status: "unloaded" | "loading" | "loaded" | "sleeping" | "downloading" | string;
  ctx_hint: number;
  ctx_source: CtxSource;
  modalities: string[];
  failed: boolean;
};

export type CtxSource = "props" | "meta" | "args" | "assumed" | "";

export type Agent = {
  id: string;
  name: string;
  url: string;
  /** Empty is no key. */
  key: string;
  model: string;
  effort: "auto" | "off" | "low" | "medium" | "high";
  preserve_reasoning: boolean;
  temperature: number | null;
  top_p: number | null;
  top_k: number | null;
  repeat_penalty: number | null;
  max_tokens: number | null;
  seed: number | null;
  instructions: string;
};

export type AppConfig = {
  /** The id of the agent answering. Empty when none is chosen. */
  agent: string;
  permissions: "ask" | "allowlist" | "full";
  compact_at: number;
  max_tool_calls: number;
  shell: string;
  command_timeout_s: number;
  output_limit_bytes: number;
  listen: string;
};

export class AppStore {
  mode = $state<"router" | "single" | "openai" | "unknown">("unknown");
  build = $state("");
  detectError = $state("");
  config = $state<AppConfig | null>(null);
  configNotes = $state<string[]>([]);
  agents = $state<Agent[]>([]);
  agentsOpen = $state(false);
  projects = $state<string[]>([]);
  models = $state<ModelRow[]>([]);
  modelsError = $state("");
  selectedProject = $state<string | null>(null);
  sessions = $state<SessionSummary[]>([]);
  authExpired = $state(false);
}
```

```ts
// stores/session.svelte.ts
export type EntryKind = "user" | "assistant" | "tool" | "compaction" | "status";

export type Entry = {
  seq: number;
  kind: EntryKind;
  content: string;
  reasoning: string;
  reasoningOpen: boolean;         // UI only
  toolCalls: { index: number; id: string; name: string; arguments: string }[];
  toolCallId: string;
  toolName: string;
  exitCode: number | null;
  truncated: boolean;
  permission: PermissionRecord | null;
  inContext: boolean;
  at: string;
  streaming: boolean;             // UI only, true while deltas are arriving
};

export type Pending = {
  id: string; tool: string; command?: string; path?: string;
  prefix: string; args: string;
};

export class SessionStore {
  id = $state("");
  project = $state("");
  model = $state("");
  state = $state<TurnState>("idle");
  entries = $state<Entry[]>([]);
  pending = $state<Pending | null>(null);
  fill = $state(0);
  nCtx = $state(0);
  nCtxSource = $state<CtxSource>("");
  cached = $state(0);
  compactions = $state(0);
  lastSeq = $state(0);
  connected = $state(false);

  get ratio() { return this.nCtx > 0 ? this.fill / this.nCtx : 0; }
  get ctxApprox() { return this.nCtxSource === "assumed"; }
  get busy()  { return this.state !== "idle"; }
  get blocked() { return this.pending !== null; }
}
```

Only one `SessionStore` is live at a time. Switching sessions closes the event source,
fetches the snapshot, and rebuilds the store.

### 12.4 The api interface

```ts
export interface Api {
  state(): Promise<StateResponse>;
  models(refresh?: boolean): Promise<ModelRow[]>;
  modelProps(id: string): Promise<PropsResponse>;
  dirs(path?: string): Promise<DirsResponse>;
  addProject(path: string): Promise<string[]>;
  removeProject(path: string): Promise<string[]>;
  allowlist(project: string): Promise<string[]>;
  removeAllow(project: string, prefix: string): Promise<string[]>;
  listSessions(project?: string): Promise<SessionSummary[]>;
  createSession(project: string): Promise<SessionSummary>;
  getSession(id: string): Promise<SessionSnapshot>;
  deleteSession(id: string): Promise<void>;
  send(id: string, text: string): Promise<void>;
  stop(id: string): Promise<void>;
  compact(id: string): Promise<void>;
  decide(id: string, d: Decision): Promise<void>;
  putConfig(patch: Partial<AppConfig> & { server?: { url?: string; token?: string } }): Promise<AppConfig>;
  events(sessionId: string, from: number, on: (e: ServerEvent) => void): Stream;
}

export interface Stream { close(): void; }
```

`lib/api.ts` exports `httpApi: Api`. `lib/mock.ts` exports `mockApi: Api`, which serves a
scripted session including a tool call, a permission prompt and a compaction, driven by a
timer. `main.ts` picks between them with `import.meta.env.VITE_MOCK === "1"`, so the whole UI
can be built and clicked through before the Go server exists. The swap is a single
assignment; no component imports `fetch` directly.

Every fetch sends `credentials: "same-origin"` and treats 401 by setting
`app.authExpired = true` and closing the stream.

### 12.5 Reconnection

`events()` wraps `EventSource`:

1. Open `/api/events?session=<id>&from=<lastSeq>`.
2. `onopen` sets `connected = true` and resets the backoff.
3. `onmessage` parses the payload, updates `lastSeq` from `seq`, and dispatches.
4. `onerror` sets `connected = false`. `EventSource` retries on its own with the interval
   from the last `retry:` field; the harness does not send one, so the browser default (3
   seconds) applies. The reconnect carries `Last-Event-ID` automatically, which the server
   uses when `from` is absent. Since `from` is in the URL from the first open, it is
   refreshed by closing and reopening with the new `lastSeq` after five consecutive errors.
5. A `resync` event closes the stream, refetches `GET /api/sessions/{id}`, replaces the
   entries, and reopens with the snapshot's `last_seq`.

A disconnect shows a one-line bar above the composer: `reconnecting`. It does not block
typing, but Send is disabled while `connected` is false.

### 12.6 Transcript rendering

| event | effect |
|-------|--------|
| `status` | sets `session.state`; drives the spinner in the top bar and the Stop button |
| `delta` | appends to the last entry when it is a streaming assistant, otherwise creates one with `streaming: true` |
| `reasoning` | appends to `reasoning` of the same entry; the block renders collapsed with a caret and a token count computed as `Math.ceil(reasoning.length / 4)` |
| `tool_call` | matches `toolCalls[]` on `index`, creating the entry on first sight, appends the argument fragment, and updates `id` and `name` from the event; renders the arguments as they arrive in a mono block |
| `permission` | sets `session.pending` and appends a `PermissionCard` inline at the end of the transcript |
| `tool_output` | finds the tool card on `index`, fills its output, exit code badge and truncation notice |
| `usage` | updates `fill`, `nCtx`, `cached` and the breakdown; the context meter recolours at 60 and 80 percent, and its popover shows one bar segmented into system, tools and messages once the breakdown arrives |
| `compacted` | appends a divider entry; everything above it is dimmed and marked `out of context`, matching the server's `in_context` flags |
| `status_entry` | appends a plain row for something the harness did rather than the model: the stopped marker and the tool call limit |
| `error` | appends a red row with the message; does not clear the transcript |
| `done` | sets `streaming: false` on the last assistant entry, `state` returns to `idle` |
| `resync` | triggers the snapshot refetch of 12.5 |

Assistant content is rendered as plain text with fenced code blocks detected by a small
regex and put in a mono block. There is no markdown library.

Tool cards: header `run_shell`, the arguments as JSON, the output in a mono block with a
`max-height` and a scroll, an exit code badge (`0` green, non-zero amber, `killed` grey), and
a footer line `output truncated, 96 KB omitted` when `truncated` is true.

### 12.7 The permission card and the composer

`PermissionCard` renders inside the transcript at the point the request arrived. It shows the
tool name, the command or path in mono, and three actions:

- **Allow once** posts `{"decision":"once"}`.
- **Allow always** shows an editable text input pre-filled with the server's `prefix`, and
  posts `{"decision":"always","prefix":"<value>"}`. A 400 from the server puts the message
  under the input and keeps the card open.
- **Deny** reveals an optional single-line reason and posts
  `{"decision":"deny","reason":"<value>"}`.

While `session.pending !== null`:

- The composer textarea is `disabled` and shows the placeholder
  `waiting for your decision above`.
- Enter does nothing.
- The Send button is disabled.
- The Stop button stays enabled, because stopping is the way out of a decision the user does
  not want to make.

The Stop button is drawn while a turn runs and not while a compaction does, which the harness
refuses to stop. A compaction leaves the send button in place, and what it takes joins the
queue the way a message typed against a turn does.

Once the POST returns 200, the card renders its answered state (`allowed once`,
`allowed always: go test`, `denied: wrong directory`) and `pending` is cleared locally
without waiting for a server event, so the composer unblocks immediately.

### 12.8 The agents dialog, the chip and the context meter

`Agents.svelte` is the list of agents on the left and that agent's parameters on the right,
880 by 620, each pane scrolling on its own. The height is fixed because one that tracked its
content would jump on every row click. The left rail is the one dark surface in a modal and
takes the console's palette rather than the page's, for the same reason the console has one:
the list reads as a place rather than as another panel.

Choosing a row selects it for editing and nothing else, so the dialog can be opened, read
and closed without the harness switching underneath. The row that answers carries a lit dot
and every other a flat one, which is honest about an address nobody has asked. Making an
agent answer is `Use this agent` in the footer, absent on the one already answering: it acts
at once, so it sits apart from the two buttons that act on the record.

Nothing reaches the harness until Save. The dialog holds a draft of the selected agent, Save
is dead until the draft differs from what is stored, and moving to another row or closing
with something typed asks before dropping it. Delete sits beside Save.

The right pane is one card per group of fields: connection, thinking, sampling and extra
instructions. Group names and the agent's name in the header are the page's own type scale,
not a heavier one, so the dialog reads as part of the app rather than as a form. Effort is a
segmented control rather than a menu, because the pane scrolls and a scroll container clips
a popover that runs past its edge.

The name is a labelled field at the top of the connection card and the header only shows it.
An in-place title reads as a heading, and a heading is not somewhere people look for a field.

The API key is an ordinary password field carrying the stored key, so it is the same control
people meet everywhere else and clearing it is selecting and deleting. It reaches the
browser to do that, which is a change from the write-only shape the harness started with;
the file is still 0600 and the key still goes nowhere but the address it opens.

The model field takes free text with the address's list beside it, because an endpoint will
often answer for a model it never named. The list is read when a row is opened, one address
at a time, through `POST /api/agents/probe` carrying the address and the key typed beside
it, and one line under the field says which of the states the address is in.

`AgentPanel` is the same component in the dialog and on the setup screen, in a compact form
there that leaves out what means nothing before an agent exists. The setup screen shows
while `agents` is empty and for no other reason: an address that stops answering is a
reason to switch agents, which is inside the app.

`AgentChip` on the composer shows the agent's name, since that is what the reader chose and
model ids run long; the id is the second line of every row in its popover, which ends in
`Manage agents`. Nothing is loaded by the choice. The first message that needs the model
loads it inside its turn, where the transcript shows the wait and the chip's dot goes amber
until the read lands.

The wait row is the only thing on the page while a model loads, so it carries a clock. It
appears under the message that started the turn, reads `Working on it`, counts from the
moment the wait began, and shows the seconds from two on, which keeps the number off a turn
that answers at once. Nothing in the row names the step: a load, a first token and a title
are all the harness waiting on the model. A compaction is the exception, since it rewrites
the conversation.

Effort, sampling and preserve reasoning are the agent's and are set in the dialog. The
composer row is the add menu, the permission chip and the agent chip.

`Modal` is the scrim, the title bar, the focus and the tab wrap. `overlay.ts` is one stack
and one key listener, so Escape reaches the topmost overlay alone: a menu inside a dialog
closes itself and leaves the dialog standing, and the tab wrap steps aside while a confirm,
which mounts outside the dialog's markup, is on top.

The meter reads `42k / 131k` with a filled bar, green below 60 percent, amber to 80, red
above. When `n_ctx_source` is `assumed` it reads `42k / ~32k`, with the tilde as the only
signal that the denominator is a guess, and the bar behaves the same way. `n_ctx` is never
zero, so there is no unknown state to render.

### 12.9 Settings and session removal

`SettingsPanel` edits `listen`, `compact_at`, `shell` and `command_timeout_s` through
`PUT /api/config`, and shows `config_notes` above the form when the server clamped
anything. It names the agent answering and carries a way into the agents dialog, since that
is where people look for a connection.

It also shows the allowlist for the currently selected project, fetched with
`GET /api/allowlist?project=`, one row per prefix in mono with a remove control that calls
`DELETE /api/allowlist`. This is the only way to withdraw an "allow always", and the only
way to clear rules left behind by a project that was removed from the list.

Each session row in the sidebar carries a remove control that calls
`DELETE /api/sessions/{id}` behind a confirmation, since there is no undo. A 409 shows
`stop the current turn first` on the row.

---

## 13. Testing

### 13.1 Per package

| package | tests |
|---------|-------|
| `config` | defaults resolution; every clamp in the table of 3.3 with its note; absent file creates defaults; unknown keys ignored; `Config.File()` round trip; `WriteAtomic` leaves no temp file on a write error and never truncates on failure; allowlist prefix match table including `git status` versus `git statusfoo` and `rm` versus `rmdir`; concurrent `Update` under `-race` |
| `llm` | The probe: a server answering `/props` with a build is llama.cpp, one that 404s it is OpenAI-compatible, a 401 is neither and stays an error; `Detect` against the router fixture and against a synthesised single-model body; `Models` `CtxHint` from `meta` and from `args` for every entry in the fixture; `Props` decoding including `chat_template_caps`; `modelURL` always carries `autoload=false`; `buildLlamaBody` golden JSON for each of the four effort levels crossed with caps known and unknown; the OpenAI body carrying `reasoning_effort` and none of llama.cpp's fields, and its model list read for `max_model_len`; the SSE state machine table; tool-call accumulation, including a stream whose fragments never carry an id; malformed chunk skipping; EOF with a finish reason, EOF without one (`KindUpstream`, partial content preserved) and EOF with nothing decoded (`KindMalformed`); error taxonomy for each status |
| `prompts` | every embedded file is non-empty; `System` renders with both fields substituted and leaves no `{{`; the tool descriptions match the schema descriptions by name |
| `tools` | path guard table (section 13.3); `read_file` numbering, offset, limit, byte cap, empty file, directory, binary; `write_file` creates parents, preserves mode, atomic replace leaves the old content on a failed write; `run_shell` exit code, merged streams, timeout kill, process group kill of a forked child, truncation head and tail, `Truncate` boundary cases |
| `perms` | `Evaluate` matrix over tool times auto times allowlist times operators, with a case per entry of the operator list and a case proving `${` does not prompt; `Ask` and `Resolve` happy path; `Resolve` on an unknown id; cancel while blocked; double resolve; `always` writes the allowlist before returning; denial content string |
| `agent` | the state machine transitions; the loop against the replay server for a plain reply, a single tool call, two tool calls, a denial, a stop mid-command, 50 iterations; `Get` on a live id, on an id only on disk, and on an unknown id; `computeFill` before and after a usage report; the `assumed` fallback arms auto compaction; effort mapping into the recorded request body; `SetAgent` repoints every live session, resetting `NCtx`, caps, `LastUsage` and the context measurement, and asks the model server nothing; opening a session asks it nothing either; a session that was not live takes the model at its next turn, and the turn asks the model it started with; preparation inside the turn reads a loaded model with `autoload=false` and loads an unloaded one, the first request of a session is already shaped by what came back, a message sent while it runs joins the queue, and the caps a turn was started with shape every request in it; the naming request carries the opening message alone, runs before the reply, and happens once per session; the context measurement sends the system message twice, once with the schemas and once without, and runs once per model per session; the breakdown subtracts the measured parts from a real usage report; compaction rewrites `in_context` and leaves `visible` alone; the unanswered-tool-call repair |
| `store` | round trip of a session with every entry kind; index built at `Open`; a corrupt file is skipped and named; `List` ordering; concurrent `Save` of two sessions under `-race` |
| `server` | auth: token to cookie, bearer, 401 without either, constant-time compare, a token file present at start is reused rather than replaced, a missing one is created 0600; origin rejection; every route's happy path and its documented error status, including `PUT /api/sessions/{id}` (200 idle, 409 busy, 400 unknown model), `DELETE /api/sessions/{id}` (204, 409 busy, 404 unknown, hub channel dropped), `GET /api/allowlist` (empty array for an unknown project) and `DELETE /api/allowlist` (200 with the unchanged list for a prefix that is absent); `tool_call` and `tool_output` payloads carry `index`; the folder browser rules including hidden exclusion and a non-absolute path; SSE replay from `Last-Event-ID`, from `?from=`, and the `resync` path when the ring has rolled; a slow subscriber gets `resync` and is not disconnected; embed fallback page when `Built()` is false |
| `service` | `UnitText` golden output for both platforms, asserted on whichever platform the test runs on; XML escaping of a home directory containing `&`; a binary under `/tmp` is refused; the install command sequence recorded by a stub `launchctl` placed first on `PATH`, asserted in order so that `enable` precedes `bootstrap`; `Report` parses a recorded `launchctl print` and a recorded `systemctl is-active` output |

### 13.2 Fixtures

`internal/llm/testdata/` receives copies of the three captures:

- `props-router.json` from `GET /props`. Drives the `ModeRouter` test and the assertion that
  `default_generation_settings.n_ctx == 0` does not become a context size.
- `v1-models.json` from `GET /v1/models`. Drives `Models`, all six entries, including the
  `meta.n_ctx` path for the loaded `Gemma4-12B` and the `--ctx-size 262144 --parallel 2`
  arithmetic that also gives 131072, and the ids containing spaces.
- `props-model.json` from `GET /props?model=Gemma4-12B&autoload=false`. Drives `Props`,
  `total_slots: 2`, `n_ctx: 131072`, and the caps decode where
  `supports_reasoning_effort` is false and `supports_tool_calls` is true. That last pair is
  the case the effort mapping test uses for the budget column.

`internal/agent/testdata/` receives recorded SSE streams as plain text files, one frame per
blank-line-separated block, replayed verbatim:

- `stream_plain.txt`: content deltas, finish `stop`, usage, `[DONE]`.
- `stream_tool.txt`: reasoning deltas, one tool call streamed across four fragments with the
  arguments split mid-string, finish `tool_calls`, usage, `[DONE]`.
- `stream_two_tools.txt`: two calls with interleaved fragments at indices 0 and 1.
- `stream_malformed.txt`: a valid chunk, a chunk with a truncated JSON object, a chunk that
  is not JSON at all, then valid chunks and `[DONE]`.
- `stream_error.txt`: two content chunks then a chunk carrying an `error` object.

A source comment in each file records the build the shape came from. The bodies are edited
copies of live responses, not invented from the README.

### 13.3 Path guard table

Root is a temp directory with `sub/`, `link_in -> sub`, `link_out -> /etc`, `file.txt`.

| argument | outcome |
|----------|---------|
| `file.txt` | allowed |
| `sub/new.txt` | allowed, parent exists |
| `sub/deep/new.txt` | allowed, parent created by write_file |
| `./file.txt` | allowed |
| `../outside.txt` | refused |
| `sub/../../outside.txt` | refused |
| `/etc/passwd` | refused |
| `link_in/x.txt` | allowed, resolves inside |
| `link_out/passwd` | refused |
| `sub/link_out/passwd` | refused |
| `` (empty) | refused, `path is empty` |
| `a\x00b` | refused |
| root itself, `.` | refused by read_file as a directory, refused by write_file as a directory |

The same table runs with the root under `/tmp` on macOS, which only passes because the root
is symlink-resolved at construction.

### 13.4 The replay server

```go
// replay serves a recorded SSE body and records the request that asked for it.
type replay struct {
	Frames   []string      // one entry per SSE frame, without the blank line
	Delay    time.Duration // between frames
	Status   int           // non-200 short-circuits before any frame
	Requests []recorded    // path, headers, body
}

func newReplay(t *testing.T, fixture string) *replay
func (r *replay) Server() *httptest.Server
func (r *replay) LastBody() map[string]any
```

Design:

- `Server()` returns an `httptest.Server` whose handler reads and records the body, then
  writes `Content-Type: text/event-stream` and each frame followed by `\n\n`, flushing after
  every write with `http.Flusher`. Without the flush the whole body arrives at once and the
  incremental path is not exercised.
- `Delay` defaults to zero. One test sets it to 5 ms and cancels the context after the second
  frame, which is how cancellation mid-stream is covered.
- The handler also answers `GET /props`, `GET /props?model=`, and `GET /v1/models` from the
  captured fixtures, so an `agent` test can run a whole turn including the post-first-response
  `Props` call against one server.
- `LastBody` decodes the recorded request body into a map, which is how the effort mapping,
  the tool schemas, the absence of `reasoning_content` in replayed history, and the presence
  of `stream_options.include_usage` are asserted.
- A `?model=` request without `autoload=false` returns 500 with a message naming the rule, so
  a regression on that fails loudly in every test that touches it rather than silently
  loading a model in production.

An `agent` end-to-end test wires a real `perms.Broker`, a real `tools.Runner` over a temp
directory, a real `store.Sessions` over a temp directory, and the replay server, and drives
one turn that reads a file, is denied a command, writes a file, and is then forced to
compact by setting `compact_at` to 0.01. Assertions: the file exists on disk with the right
content, the denial reached the model as `denied by user: <reason>`, the second request body
is smaller than the first, the session file has `compactions: 1`, and every entry before the
divider has `in_context: false` and `visible: true`.

### 13.5 Live smoke checklist

Run against `http://192.168.2.205:8080`. Steps 1 to 3 are read-only and load nothing.

1. `curl -s http://192.168.2.205:8080/v1/models | jq -r '.data[] | "\(.id) \(.status.value)"'`
   and keep the output. This is the before picture.
2. Start `quecto-harness`. `GET /api/state` reports `"mode":"router"`, the build string, and
   six models with the same statuses as step 1.
3. Repeat step 1. Nothing changed. This is the assertion that the harness performs no
   model-scoped GET without `autoload=false`.
4. Ask the user before this step, because it loads a model. Add a project with the `+`
   button, select `Gemma4-12B`, send `list the files here`. Approve the `ls` once. Output
   appears with `exit code: 0`.
5. Send another listing command, approve `always`, and check that `~/.quecto-harness/allowlist.json`
   gained the prefix under the project key.
6. Turn auto on and send a command that prefix-matches. No card appears. Send one containing
   `|`. A card appears.
7. Send a write task. The file appears on disk with the right content and the transcript
   shows `wrote <name>`.
8. Set `compact_at` to 0.01 through the settings, send a message, and confirm a compaction
   divider, a `compacted` event with a smaller `after`, and a smaller `prompt_tokens` on the
   next turn's usage event.
9. Set effort to off and confirm that no `reasoning` event arrives and the request body
   carried `reasoning_budget: 0`.
10. Ask the model to run `sleep 60`, approve it, press Stop. The process is gone
    (`pgrep -f 'sleep 60'` finds nothing), the transcript shows the stop marker, the state
    returns to idle.
11. Kill the server with SIGINT while a turn is streaming. It exits within 5 seconds and the
    session file on disk ends at the last persisted entry. Start it again without touching
    the browser: the same tab keeps working, and `~/.quecto-harness/url` carries the same
    token as before. Delete `~/.quecto-harness/token`, restart, and the tab now gets a 401
    and the `not authorized` page.
12. Pick another model. The chip shows it in every open transcript and nothing is sent:
    `/props` is not asked for it until you send a message, and that turn shows
    `Working on it` for as long as the router takes to load it. The meter denominator
    changes and the turn's usage event reports against the new model. Switch project and confirm the picker
    still reads the model you chose. Pick one during a turn and confirm the running turn
    finishes on the model it started with.
13. Remove an "allow always" rule from the settings panel, then send the same command again
    with auto on. The permission card comes back.
14. Delete a session from the sidebar. The file is gone from
    `~/.quecto-harness/sessions/` and the transcript pane is empty.
15. `quecto-harness service install`, then
    `launchctl print gui/$UID/io.quecto.harness | head -5` shows a pid.
    `quecto-harness open` opens the UI. `quecto-harness service status` exits 0 and prints the
    URL and the token path. `quecto-harness service uninstall` removes the plist and the
    process.
16. `gofmt -l . && go vet ./... && go test -race ./...` clean.

---

## 14. Decisions

The fourteen questions raised at review, and their answers. Each is settled and is already
reflected in the sections above.

1. **Token lifetime.** The token is persisted in `~/.quecto-harness/token`, mode 0600,
   created on first start and reused across restarts, so a tab survives a supervisor
   restart. Deleting the file rotates it on the next start, and that is the only revocation
   mechanism. Sections 1.2, 1.5, 3.1, 10.2, 11.5, 11.6.

2. **The "off" effort row.** Off sends both `reasoning_budget: 0` and
   `chat_template_kwargs: {"enable_thinking": false}`, regardless of what the caps report.
   A template that does not know the keyword ignores it, and the budget covers the case
   where it does. Sections 4.5, 8.7.

3. **Parallel tool calls.** A batch of calls in one assistant message is executed
   sequentially, one permission card at a time, in the order the model listed them.
   `parallel_tool_calls` is not sent in either direction. Sections 4.5, 8.3.

4. **`max_tokens`.** Not sent. The iteration limit and the compaction threshold are the
   bounds on a runaway turn. Section 4.5.

5. **Manual compaction.** Compact stays idle-only. A user watching a turn that needs
   compacting presses Stop first. This keeps the rewrite away from
   a history whose tool results have not been appended yet. Sections 8.8, 10.4.

6. **What Stop reaches.** The session, and not the model server. The turn is cancelled, the
   command is killed, the history stops where it stood and the connection is closed, which is
   the only signal the HTTP API has. A llama.cpp slot part way through a prompt is writing
   nothing, so it does not see the close: measured against a router holding Gemma4-12B, a
   request cancelled two seconds into a 100k token prompt left the slot processing that same
   task for another half minute, and the next request queued behind it for 13 seconds where
   an idle server answers in 50 milliseconds. Nothing in the protocol cancels work already
   started. Sections 8.8, 8.10.

7. **Stopping a compaction.** Refused, with `ErrCompacting`. A turn stopped part way has
   output on the page and tool calls it will not now make, so stopping it changes what
   happens next. A compaction has neither: the request would run on the server for the same
   length of time and the session would come back to a context that still needs compacting.
   The composer offers no Stop while it runs. Sections 8.8, 12.7.

   No deadline bounds it either. The one thing a timeout could cap is the prefill of a full
   context, which is exactly the part with no honest number: it grows with the window, the
   hardware and whatever else is queued on the server, and cutting it off would throw away
   work that was nearly done. A session left compacting by a server that answers neither way
   is cleared by restarting the harness, which loses nothing, since the rewrite is applied
   only after the summary arrives.

8. **The operator rejection list.** It is `;`, `&`, `|`, `>`, `<`, backtick, `$(`, `\n`,
   `\r`. Bare `&` subsumes `&&` and catches backgrounding, which would otherwise escape the
   process group wait. Bare `|` subsumes `||`. `${` is not on the list, since parameter
   expansion cannot start a second command. Section 7.4.

9. **Concurrent turns.** Not serialised. llama.cpp queues a request when every slot is busy,
   so a second session sits in `status: streaming` with no deltas until a slot frees. This
   is documented behaviour and nothing is built for it. Section 8.10.

10. **One agent.** `config.agent` is the only choice of what answers. Selecting or editing
   one points every session at it: each clears `NCtx`, caps and `LastUsage` so nothing
   measured against the previous tokenizer is carried over, and the transcript records
   nothing. A session that was not live takes it at the top of its next turn. A turn already
   running holds the agent it started with, and the backend that agent resolved to, so it
   finishes against the address it began on. Sections 3.2, 8.1, 8.5, 10.4, 12.8.

11. **Unknown context size.** The last resort is 32768 with source `"assumed"`. Automatic
   compaction stays armed against it, and the meter renders `42k / ~32k` so the guess is
   visible. `n_ctx` is therefore never zero. Sections 8.5, 12.3, 12.8.

12. **Large tool arguments in the session file.** Accepted as it is. There is no cap on
    stored `tool_calls[].function.arguments`, because a truncated argument makes the stored
    history unusable for a resend. A session that writes several large files produces a
    large file. Section 9.5.

13. **Single-model detection.** Confirmed: `role == "router"` selects router mode and the
    absence of `role` selects single mode. `model_alias` is not a discriminator, since the
    router returns it too. Section 4.2.

14. **Allowlist entries for a removed project.** They stay on disk, so re-adding the project
    restores its rules. Two routes make them reachable: `GET /api/allowlist?project=` and
    `DELETE /api/allowlist`. The settings panel lists the current project's rules with a
    remove control, which is the only caller of `config.Store.RemoveAllow`. Sections 10.4,
    12.4, 12.9.

15. **Deleting a session.** `DELETE /api/sessions/{id}` returns 204 on success, 409 when the
    session is busy, and 404 when the id is unknown. It removes the file, the live engine
    entry and the hub channel. The sidebar exposes it behind a confirmation, since there is
    no undo. Sections 8.1, 10.4, 12.9.

16. **Reasoning dropped from the history.** Noted, no action. Reasoning is stored and shown
    but never replayed to the model, so a template reporting
    `supports_preserve_reasoning: true` will not receive the trace it is built for. Section
    8.7.

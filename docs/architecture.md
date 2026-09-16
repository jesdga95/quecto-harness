# Quecto harness: architecture summary

Executive overview of the full implementation spec. Each section gives a chapter's shape,
its key decisions, and its invariants. Type signatures, wire bodies and full tables are
omitted; the complete spec is in git history.

**Fixtures** (`docs/fixtures/`): `props-router.json`, `v1-models.json`,
`props-model.json`, `props-reasoning.json` (live router, build b10812) and
`openai-models.json`.

## 1. Overview and process model

A single static Go binary (`CGO_ENABLED=0`) with the Svelte build embedded via
`go:embed` in `internal/web`. Subcommands: `open`,
`service install|uninstall|status` and `version`. A bare invocation serves; there is no
`serve` subcommand, though the error on an unknown one names it.

**Startup**

- Resolve paths and directories (`0700`); open `config.json` and `allowlist.json`. A
  first run writes both with defaults.
- Scan the sessions directory into an index, and open the attachment and paste stores.
- Load or mint the token: 32 random bytes, mode `0600`, stable across restarts. Deleting
  the file is the only way to rotate it.
- Build the model-server client pool.
- Bind the port **before printing anything**, so a port conflict emits no URL.
- Start the engine, permission broker, server and SSE hub; probe the active agent in a
  goroutine (`mode: unknown` until it answers, retried after 15 s).
- Write and print the URL file, then open a browser.

**Shutdown** (SIGINT / SIGTERM): cancel the root context → release pending decisions →
wait up to 3 s for turns → shut the server down over 5 s → remove the URL file → exit 0.
A second signal exits 130 immediately. Shell commands are killed by process group and
SIGKILLed after 2 s.

Because the token outlives the process, an open browser tab survives a supervisor restart.

## 2. Package map

`cmd` fans out to twelve packages with no import cycles. The boundaries exist so that each
concern is testable in isolation:

| package  | responsibility                                                    |
| -------- | ----------------------------------------------------------------- |
| `config` | owns the config files and the "optional on read" rule             |
| `llm`    | the protocol layer; knows nothing of sessions                     |
| `search` | the search provider layer; one interface, Exa first               |
| `tools`  | executes; enforces only the path guard                            |
| `perms`  | decides, never executes; the loop blocks on a channel while HTTP resolves it |
| `agent`  | owns the turn order; the only session mutator                     |
| `store`  | persists and lists, without knowing what a turn is                |
| `diff`   | line hunks from two versions of a file, on `go-udiff`             |
| `server` | the HTTP and SSE transport                                        |
| `prompts`| embedded markdown; a missing file fails the build                 |
| `web`    | `go:embed all:dist` and the fallback page                         |
| `service`| renders units as a pure function of paths, testable on any OS     |

## 3. internal/config

Paths live under `~/.quecto-harness` (overridable with `$QUECTO_HARNESS_HOME`):
`config.json`, `allowlist.json`, `sessions/`, `files/`, `uploads/`, `log/`, `token`,
`url`.

- **Two shapes.** `File` is what is on disk: every field a pointer or slice, so *absent*
  and *zero* stay distinct. `Config` is what the program sees: resolved, defaulted
  and validated.
- **Resolve** folds the file over the defaults, clamping out-of-range values back and
  emitting a note rather than failing startup. Notes print once and surface in
  `GET /api/state` as `config_notes`. `Config.File()` writes every field, so a saved file
  is complete and self-heals into the current shape.
- **Agent ids are minted**, not taken from the name (`agt_` + 16 base32 chars), so a
  rename moves nothing. The id is the identity: duplicate names are refused by the API but
  tolerated in a hand-edited file.
- **`WriteAtomic`** temp-files in the same directory and renames, so a failure never
  leaves a half-written file, which is the property that matters for a config.
- **`Store`** (an `RWMutex`) is the sole writer of the two config files and copies the
  agent list so a config read before a write does not change with it. `AddProject`
  resolves and symlinks the path, which the path guard and the allowlist both key on.
- `allowlist.json` maps a project root to command prefixes, deduped and sorted
  longest-first.
- **No version field, no migration code**: unknown keys are ignored; missing keys take
  their default until the file is next written.
- **The search block** names a provider (empty is off) and keeps one address-and-key
  account per known provider, so naming another and coming back does not cost the first
  one its key. A first run writes the block with the default provider account and an empty
  key, and resolution backfills a row for any provider added in a later build, which is
  how a second provider arrives without a migration.

## 4. internal/llm

One `Backend` interface (`Detect`, `Models`, `Prepare`, `Props`, `Chat`) over three
protocols: `llamacpp`, `chat` (OpenAI-compatible) and `responses`.

- **The protocol is told, not detected.** The chat and responses endpoints sit at the same
  address and answer the same model list, so nothing can tell them apart; the agent names
  which one it speaks.
- **`Pool`** keys a client on `(url, key, protocol)`. That key *is* the whole
  invalidation rule. `Retain` drops the clients no agent points at.
- The HTTP client has **no global timeout** (a stream can run minutes); deadlines come from
  the caller's context. Compression is off on the transport, and a 60 s
  `ResponseHeaderTimeout` bounds the wait for the first byte.
- **Effort** is translated per backend. llama.cpp reads the template's capabilities: with
  `supports_reasoning_effort` it sends `reasoning_effort`, otherwise a `reasoning_budget`
  of 2048, 8192 or -1. `off` sends budget 0 plus `enable_thinking: false`; `auto` sends
  nothing. `reasoning_format: deepseek` goes on every llama.cpp request. The chat backend
  has no such branch: it always sends `reasoning_effort`, with `none` for off.
  `tool_choice: auto` goes on every request whatever the protocol, and `tools` is omitted
  when empty, which is what the compaction request sends.
- **`Chat`** streams SSE into content, reasoning, tool_call, finish, usage and timings
  events, assembled into a `Result`. A 2xx that is not `text/event-stream` is decoded whole.
- The **SSE parser** is a line-oriented state machine with a 1 MiB frame cap; malformed
  chunks are dropped and counted, never fatal. Tool-call fragments accumulate per index,
  with id and name deduped and arguments appended raw; an empty id is synthesised.
  Arguments are validated in `agent`, not here.
- **Usage** is authoritative for token accounting; timings are surfaced to the UI but not
  persisted.
- **Errors** carry a kind taxonomy (`unreachable`, `auth`, `not_found`, `bad_request`,
  `model_loading`, `upstream`, `malformed`, `canceled`) with messages written for the
  reader, not the log.

## 5. internal/prompts

Embedded markdown in three groups: ten tool descriptions, six templates (`system.md`,
`compaction.md`, `message.md`, `attachment.md`, `image.md`, `title.md`) and one plain file,
`compact.md`. Each file is named individually in the `go:embed` directive, so a deletion
fails the build, and a template parse error panics at init, which the package test catches.

`System` renders the project, the OS, whether `ask_question`, `send_message` and
`web_search` were sent, the agents this session may hand work to and the handle of the one
that launched it. It then appends the agent's instructions as a **value, never parsed**:
a stray `{{` survives, the agent's text wins where it disagrees with the built-in
guidance, and an empty value renders byte-for-byte unchanged.

## 6. internal/tools

The package holds ten tool schemas. A `Runner` bound to a symlink-resolved project root
executes six of them; `agent` executes `write_todos`, `ask_question`, `run_agent` and
`send_message`, whose effect is on the session rather than the machine.

- **`read_file`** numbers lines, takes an offset and a limit, caps bytes, detects binary
  content, and marks what it left with `… n more lines`.
- **`find`** searches inside the project, on the same guard `read_file` uses.
- **`write_file`** creates parent directories, replaces atomically, preserves the existing
  file mode, and hands the file as it stood to `Baseline`, which is what the change panel
  compares against.
- **`replace_in_file`** does the same, for an edit rather than a whole file.
- **`run_shell`** starts a fresh shell per call, merges stdout and stderr, kills the
  process group with SIGKILL after 2 s, and adds an exit-code line and timeout/stop notes.
- **`web_search`** reaches the provider the config names through `internal/search`
  (Exa is the first and only one), the query's whole character budget shared across its
  results, a hit with no address dropped and counted, and the answer truncated to
  `output_limit_bytes` like every other tool. It is the one tool with no path to guard.

Unknown argument fields are **tolerated** (a small model invents one and the call should
still run); a missing required field is a one-line tool error.

**Path guard.** The argument is resolved to an absolute path and the existing prefix is
symlinked; anything that falls outside the root is refused with **one identical message for
every cause**, so a probing model learns nothing from the difference. An inner symlink
pointing out is refused; a write through a final-component symlink follows the target and
checks it; the check-then-open race is accepted (the user's own machine, the user's chosen
directory). Output is truncated to a head and a tail with a marker; `looksBinary` triggers
on a NUL byte or invalid UTF-8 above 1 % of an 8 KiB sniff, and UTF-16 reads as binary,
accepted.

## 7. internal/perms

The broker **decides and blocks**. `Evaluate` applies the static policy without blocking;
`Ask` registers a pending request, emits it to the UI, and blocks on a one-slot buffered
channel until `Resolve` or the turn is cancelled, so a resolve racing a cancel either wins
or reports `ErrNoSuchRequest`, and never blocks the HTTP handler. `Pending` replays
unanswered requests after a browser reconnect.

- **Policy by mode.** `read_file`, `find`, `write_todos`, `ask_question`, `run_agent` and
  `send_message` are always allowed: the first two are confined by the path guard, and the
  rest touch the session rather than the machine. `full` allows everything before the tool
  is even named. In `allowlist` mode, `write_file`, `replace_in_file` and `web_search` run
  free and `run_shell` runs when it prefix-matches the project allowlist **and** contains
  no shell operator. `web_search` prompts in `ask` mode: the query leaves the machine and
  the call is billed, and the allowlist holds shell prefixes, which have nothing to say
  about a query, so "allow always" is not offered for it.
- **Operators** are a substring scan, not a parse: `; & | > <` backtick, `$(`, newline,
  CR. A quoted redirect still prompts. Over-prompting is the intended direction;
  `${` is not on the list.
- **Prefix matching** normalises whitespace, matches whole-word (so `git status` does not
  allow `git statusfoo`), is per project, and is longest-first. The card offers the first
  two fields as an editable default.
- **An "allow always"** writes the allowlist before the command runs, and downgrades to
  *once* with a note if the write fails.
- **A denial** returns to the model exactly `denied by user` plus an optional reason,
  keeping the `tool_call_id` so the history stays valid, and the loop continues. A call
  stopped by the user gets `stopped by user` and the turn ends.

## 8. internal/agent

The `Engine` owns live sessions and **never evicts them**, so there is exactly one
`*Session` per id and the per-session mutex is meaningful. `Send` appends the user entry,
persists, starts the turn goroutine and returns. A message sent while a turn is running is
held as a steer: the next preflight absorbs it into the running turn, and one that arrives
too late to be picked up starts a turn of its own.

**Turn states:** `idle → preflight → streaming → deciding → executing → final`, with
`loading`, `compacting`, `stopped` and `failed` as the branches. Every change publishes
`status`, which drives the spinner and the Stop button.

- **The loop** reads the model once at turn start and holds it for the whole turn, so a
  config edit cannot change the prompt mid-turn. Then, with no iteration cap: preflight
  (absorb a queued message, then check fill against `compact_at × n_ctx`), stream, and for
  each tool call decide, then execute, persisting after every entry. What bounds a turn is
  `max_tool_calls`, 50 by default: once it is spent the tool schemas are dropped from the
  request and a `status` entry says so, so the turn ends in prose rather than being cut off.
- **Token fill** treats the last usage report as authoritative and estimates entries added
  since at four characters per token, so a 16 KiB tool result counts before the next usage.
  The window is the agent's `max_context` override, else what the server reported, else
  zero. Zero is no limit: nothing compacts against it and the meter has no denominator.
- **`Prepare`** is the only request allowed to autoload a model, and it is called from
  `ensureReady`, so a model loads on the first message that needs it, never from the menu.
- **Context breakdown.** The system and schema costs are measured with two one-token
  completions (with and without the schemas), once per model per session; the message cost
  is the remainder of a real usage report. A negative remainder reports nothing.
- **Titles.** A session with no typed title asks the model for one from the opening message
  alone, before the first reply, so it does not evict the turn's cache. A subagent run is
  titled from its task and asks nothing.
- **One agent.** Selecting or editing the active agent repoints every live conversation and
  clears `n_ctx`, caps and the measurement; a turn already running finishes on the agent it
  started with, and a subagent run stays on the agent it was launched onto.
- **Compaction** fires when fill crosses the threshold, or manually on an idle session
  (Stop is refused while it runs). On success it flips every prior entry to *not
  in-context* and appends one compaction entry carrying the summary.
- **What the model sees.** The UI keeps every entry forever (dimming what dropped out); the
  model sees only `contextMessages()`, which regenerates the system prompt, includes only
  in-context entries, synthesises tool messages for any unanswered assistant call, and
  carries each assistant entry's reasoning as `reasoning_content`. The agent's
  `preserve_reasoning`, on by default, asks the template to keep it in the prompt.
- **Persistence** happens after create, after each user/assistant/tool entry, after a
  compaction, and on a terminal state. Streaming deltas are not persisted.
- **Concurrent turns** are not serialised. llama.cpp queues a request when every slot is
  busy, so a second session sits in `streaming` with no deltas until a slot frees. That is
  documented behaviour, not a defect to work around.
- **Subagents.** `run_agent` creates a session on the named agent and returns its handle
  without waiting. A subagent runs its own agent, is skipped by `SetAgent`, is offered
  neither `run_agent` nor `ask_question` (depth is one), reports to its launcher, has its
  permission card drawn in the launcher's conversation, rolls its file writes up onto the
  launcher, and is stopped and deleted with it. An agent's `concurrency` bounds how many
  sessions it answers at once, and one with no room left is left off the list the prompt
  offers.

## 9. internal/store

An **`Entry`** is one transcript row: seq, kind, role, content, reasoning, tool calls with
their id and name, exit code, a truncation flag, a permission record, attachments, the
agent a message came from, an `edited` flag, a timestamp, free-form meta, and explicit
`in_context` / `visible` flags. `in_context` drives what is sent to the model; `visible`
drives what the UI keeps. The permission record preserves the decision, so a reloaded page
shows the card in its answered state.

- A session is **one file at `0600`**, beside a sidecar folder holding the baseline copies,
  with one mutex per id so sessions never serialise against each other. The header carries
  project, agent, parent, model, title, whether the title was typed, times, `n_ctx` and its
  source, compactions, last usage and its tool share, the measured context base, the todo
  list and the baselines. The system prompt is **regenerated, not stored**.
- `Open` builds an index from a one-time scan, so `List` never touches the filesystem; a
  corrupt file is skipped and named, not fatal. `Save` atomic-replaces with indented JSON.
- **Size.** Tool output is capped at `output_limit_bytes`, but `write_file` content lives in
  the assistant's arguments, because a truncated argument makes the history unusable for a
  resend. A session that writes many large files grows large; the mitigation is a new
  session.

## 10. internal/server

One method-and-pattern `ServeMux`, wrapped by `recoverPanic → logRequest → checkOrigin →
auth`. A panic becomes a 500 with a stack in the log; it never takes the process down.

- **Auth.** The binary prints and opens a `?token=` URL; a match sets an `HttpOnly`
  `SameSite` cookie for 30 days and 303s to `/`, so the token leaves the address bar. Every
  `/api/*` request needs the cookie or a Bearer token; a 401 is a full-page state.
- **Origin.** No CORS headers are sent, and `checkOrigin` rejects a cross-origin `Origin`,
  closing the gap a form or `EventSource` on another origin would otherwise reach.
- **Routes** cover: `state` (config, agents, the active agent's probe and models, projects,
  OS, version; the probe is cached 15 s, keyed by address and key, and describes the active
  agent alone); agent CRUD with `select` and a `probe` that returns 200 even on failure;
  `search` (the block, with one account per provider and the key in plaintext as the agent
  key is; `PUT` validates before writing and answers the resolved block) and
  `search/probe` (one cheap billed request against an address and key that may not be saved
  yet; it returns 200 even on failure and is never cached, since it costs money);
  `models` and `props` (the id is a **query parameter** because ids contain spaces and
  slashes; an unloaded model is a 409 normal state); `fs/pick`, which opens the host's own
  chooser rather than listing directories, with `fs/paste` and `files/{id}` for
  attachments; project and allowlist routes; session list, get, changes, message, resend,
  stop, compact, rename (a `PUT` on the session) and delete; permission and question
  answers (a second click is 404); and `config` (a `listen` change takes effect on
  restart).
- **SSE hub.** Per session, with a 512-event replay ring. Streamed text is held and
  coalesced into windows before it goes out, so 512 events cover a turn rather than a few
  hundred deltas. `Publish` assigns a seq and does a non-blocking send per subscriber; a
  slow subscriber gets one `resync` and refetches the snapshot rather than disconnecting. Reconnect uses `Last-Event-ID` or a `from` parameter,
  and the replay is written before the live channel, under the lock.
- **Events:** `status`, `delta`, `reasoning`, `tool_call`, `permission`, `permission_done`,
  `question`, `tool_output`, `usage`, `compacted`, `status_entry`, `message`, `queued`,
  `title`, `todos`, `error`, `done`, `child`, `changed`, `resync`. The join key is the call
  **index**, not the id, which may be empty on early fragments.

## 11. internal/service

`UnitText` renders the launchd plist or the systemd unit from a `Spec` (resolved binary
path, home, log dir, `PATH`, optional listen) **without touching the filesystem**, switching
on `runtime.GOOS`. Both formats are compiled and asserted on both platforms, so the systemd
path is verifiable on a Mac. A fixed `PATH` with the binary's directory prepended, because
a service-managed process inherits almost none.

- **Install** refuses a binary under a temp directory, writes the unit atomically, then on
  macOS bootouts (ignoring status), enables, bootstraps and kickstarts; on Linux it
  daemon-reloads and enables now, *printing* rather than running `loginctl enable-linger`.
  It then polls the URL file for 5 s.
- **Uninstall** removes the unit and the URL file but leaves config, token and sessions.
- **Status** prints unit, state, URL, token path and logs, and exits **0 / 3 / 4** for
  running, installed-not-running and not installed, which a shell script can read.

## 12. Frontend

Svelte and Vite in `web/` build into `internal/web/dist`, which Vite empties. The embed is
`go:embed all:dist`, so the directory has to exist for the binary to compile, and a
`dist/.gitkeep` is what is meant to keep it there. That file is **absent from the
repository**, so a fresh checkout does not compile: `pattern all:dist: no matching files
found`.

- **`lib/api/types.ts`** declares the `HarnessApi` interface, **`lib/api/http.ts`**
  implements it, and **`lib/api/mock.ts`** is a scripted implementation (a tool call, a
  permission prompt and a compaction on a timer). `lib/api/index.ts` chooses between them
  on `VITE_MOCK=1`, so the UI is clickable before the Go server exists. No component calls
  `fetch` directly.
- State is Svelte 5 runes under `lib/state/`: `app.svelte.ts` for the harness, with
  `confirm` and `toast` beside it. `events()` wraps `EventSource`: open with a `from`
  parameter, track seq, and on `resync` close, refetch the snapshot and reopen with its
  `last_seq`. A disconnect shows *reconnecting* and disables Send, and a 401 becomes a
  full-page state.
- **Transcript.** The store reduces the event union: deltas append to the streaming
  assistant entry; `tool_call` matches on index; `permission` sets `pending` and blocks the
  composer (Stop stays enabled); `usage` updates the context meter with its segmented
  system/tools/messages popover; `compacted` appends a divider and dims what dropped out;
  `done` returns to idle. Assistant content goes through `lib/markdown.ts`, built on
  `marked` with `highlight.js` and `katex`: markdown, TeX, highlighted code, and SVG
  figures and HTML pages mounted as elements. Half-written output stays literal text rather
  than throwing.
- **Permission card** renders inline where the request arrived: *Allow once*, *Allow always*
  (editable prefix, offered for `run_shell` alone, since the allowlist holds shell
  prefixes), *Deny* (optional reason). A 200 clears `pending` locally, without
  waiting for a server event.
- **Dialogs.** `Dialog` is the shell both dialogs share: backdrop, focus restore, tab
  trap, overlay stack. `SplitDialog` wraps it in the two-pane geometry, and on top of it
  the agents dialog is a rail of records, one draft per row, while the settings dialog is a
  rail of four categories over one draft for the whole dialog: a category is a view of it,
  so moving off one never asks, and closing with unsaved edits does. The search pane names
  a provider, keeps the others' keys in the draft untouched, and tests an unsaved key
  against `search/probe`.
- **Agents dialog** is a draft until Save, dead until the draft differs, and asks before
  dropping unsaved edits. `protocol` sits with the connection, because an address alone does
  not say how to speak to it. `effort` and `protocol` are an `OptionPicker` each; `model` is
  an `AgentModelField`, free text beside the address's list, read one address at a time. The
  composer carries the add menu, the permission chip and the agent chip **by name**, and
  nothing loads on a selection. A subagent's runs are read through
  the agents dock, which follows one run's stream at a time.

## 13. Testing

Per-package suites, each tied to a fixture or a recorded stream:

- **config**: defaults, every clamp with its note, round trip, `WriteAtomic`, allowlist
  matching, concurrent `Update` under `-race`.
- **llm**: the probe, `Detect`, `Models` context hints, `Props` and caps, `buildLlamaBody`
  golden JSON across effort and caps, the SSE state machine, tool-call accumulation, the EOF
  cases and the error taxonomy.
- **prompts**: every embedded file present and non-empty; `System` renders cleanly.
- **tools**: the path-guard table; read, find, write, replace and run semantics;
  `Truncate` boundaries.
- **diff**: hunk shaping, the cap, and honest counts for what the cap left off.
- **perms**: the `Evaluate` matrix; `Ask`/`Resolve` with cancel while blocked; *always*
  writes the allowlist before returning.
- **agent**: the state transitions and the loop against the replay server: a plain reply,
  a tool call, a denial, a stop, a spent call budget; `computeFill`; an unknown window;
  `SetAgent`; preparation; the naming request; the context measurement; compaction; the
  unanswered-tool-call repair; and, in files of their own, delegation, steering, questions,
  the change list and resend.
- **store**: round trip of every entry kind; a corrupt file skipped and named; concurrent
  `Save` under `-race`.
- **server**: auth, origin, every route's happy path and documented error, SSE replay and
  `resync`, the embed fallback.
- **service**: `UnitText` golden output for both platforms; escaping; the install command
  sequence asserted in order.

**The replay server** serves a recorded SSE body with a flush after every frame, answers the
model endpoints from the fixtures, and fails loudly on any model read without
`autoload=false`. An end-to-end test wires a real broker, runner and store to it and drives
a turn that reads, is denied, writes and compacts.

**Live smoke checklist** (against the real router): a before-and-after model-status
comparison proving no model-scoped GET without `autoload=false`; then a full turn, an
allowlist round trip, an operator prompt, a write, a compaction, effort off, a stop of
`sleep 60`, a SIGINT mid-stream, model and project switching, a session delete, the service
install/status/uninstall, and a clean `gofmt`, `go vet` and `go test -race`.

## 14. Decisions

The fourteen review questions, each settled and already folded into the sections above:

| # | decision |
|---|----------|
| 1  | The token persists across restarts; deleting the file is the only rotation. |
| 2  | Effort `off` sends both `reasoning_budget: 0` and `enable_thinking: false` on llama.cpp, and `reasoning_effort: none` on the chat backend. |
| 3  | Parallel tool calls run sequentially, one card at a time, in model order. |
| 4  | `max_tokens` is sent when the agent sets one, and by the title and measurement requests. There is no iteration limit; a spent `max_tool_calls` drops the tool schemas instead. |
| 5  | Manual compaction is idle-only; Stop the turn first. |
| 6  | Stop cancels the session, not the model server; nothing in the HTTP protocol cancels work already started (a llama.cpp slot keeps going). |
| 7  | Stopping a compaction is refused, with no deadline, since the prefill of a full context has no honest bound. |
| 8  | The operator list is `; & \|` redirects backtick `$(` newline CR; `${` is not one. |
| 9  | Concurrent turns are not serialised; llama.cpp queues. |
| 10 | The active agent is what answers a conversation; selecting or editing it repoints every live conversation and clears what the previous tokenizer implied. A subagent run keeps the agent it was launched onto. |
| 11 | An unknown context size is zero, and zero means no limit: nothing compacts against it and the meter has no denominator. |
| 12 | Tool arguments are stored whole, since a truncated argument breaks a resend. |
| 13 | The protocol is named, not detected. |
| 14 | `role == "router"` means router mode; an absent role means single. `model_alias` is not a discriminator. |

Also settled: allowlist entries for a removed project stay on disk and are reachable via the
two allowlist routes; deleting a session is 204, 409 when busy, 404 when unknown, and removes
the file, the live entry, the hub channel and the subagent runs under it behind a
confirmation (there is no undo); and reasoning is stored, shown and replayed to the model,
which `preserve_reasoning` asks the template to keep.

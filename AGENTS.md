# Working in this repo

## Prose

Comments, prompts, docs and commit messages state what is true and stop. Cut:

- Aphorisms, and closing flourishes on a sentence already finished.
- A point made once and then restated as a better image.
- Passages arguing that a design is good.
- Anything describing the agent or how it works.

Keep the register of a manual page.

No em dashes anywhere, least of all in user-facing copy. Restructure the sentence
instead.

## Casing

Anything a person reads starts with a capital letter. Buttons, labels,
headings, placeholders, dialog copy, toasts, terminal output, and the message
inside an error response. Sentence case, not title case: "Remove and delete",
not "Remove And Delete". There is no lowercase house style here.

Write the string that way. Do not add code that capitalises text on its way to
the screen.

Two exceptions, both because the reader is not a person. Tool results and
denials in `internal/tools` and `internal/perms` are read by the model. And an
error string that exists to be wrapped stays lowercase, since "open %s: Check
the permissions" reads wrong in the middle of a sentence.

## Comments

Comments are stateless. They explain what the code does and why it is that way,
for someone reading only this file.

- State a constraint, not where the constraint came from. No pointers to other
  files, to documents outside the code, to phases of work, or to earlier
  implementations.
- Two or three lines. A file or package header may run to five.
- If a comment needs more than that, the code needs a better name or a smaller
  function.

## Prompts

Text written to steer the model lives in `internal/prompts` as markdown, never as a
string literal in the code that sends it. That includes tool descriptions, so wording
can be read and rewritten in one place.

Each file is embedded by name, so a missing one fails the build rather than the turn.
What takes values is a template with a render function beside it; JSON schemas stay in
Go, since they are structure rather than prose.

A prompt says what the tool does, what its arguments mean and when to reach for it.
No worked examples, no reasons a decision was made that way, nothing about the model
itself.

## The model endpoint

Never send a model-scoped GET to a router without `autoload=false`. With autoload on,
naming a model in a query parameter loads it on the remote machine, so a passive read
becomes a write. Every model-scoped request goes through the one helper in
`internal/llama` that adds the parameter. The only request allowed to load a model is
the user's first chat message to it.

## Dependencies

A new dependency is close to a no. The backend is standard library only, and part of
the point is that there is very little here to patch. A dependency has to do something
the standard library genuinely cannot. The frontend list is Svelte, Vite, Tailwind,
one font package and one icon package; adding to it is the same conversation.

## Compatibility

There are no released versions and no state files in the wild, so a stored
shape can change without anything having to read the old one. When a field in
`config.json`, `allowlist.json` or a session document changes, change it and
delete the local file. Do not write a reader for the old shape, a migration
step, a field kept for one more version, or a default whose only job is to
cover a file an earlier build wrote.

Validation is separate and stays. A hand-edited value outside the accepted set
still falls back to the default and produces a note, and a field missing from a
model endpoint response is still treated as missing.

This section goes away at the first release.

## Tests

Critical core code gets a test. Critical means the protocol, the session log,
credential checks, and anything that parses a response from the model endpoint.

Better than a unit test is one that exercises a running instance. The endpoint
lies in ways a fixture written from documentation will not reproduce, so capture
fixtures from real responses. Behaviour that only appears in a live process gets
checked against one: startup refusals, signal handling, crash recovery, and limits
that must report themselves without dropping a connection.

Run before committing:

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

## Build

```sh
make web     # npm ci, vite build into internal/web/dist
make build   # web first, then the binary that embeds it
make test
```

The UI is embedded with `go:embed`, which resolves at compile time. After editing
anything under `web/` you must rebuild it before the binary, or you get a successful
build serving the previous UI with no warning. `make build` does both in order.

Backend-only work needs no Node. A checkout without a built UI compiles and runs; `/`
returns a page saying the UI is not built.

## Style

Standard Go. Errors say what failed and what the caller can do about it, in plain
sentences.

Every field decoded from the model endpoint is optional and every shape is a
guess. Treat a missing field as missing rather than as a zero value, and never let
one malformed response take down more than the request it arrived on.

Nothing is written into a project folder. Configuration, the allowlist and sessions
live under `~/.quecto-harness`.

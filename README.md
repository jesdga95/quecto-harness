# Quecto Harness

A local coding agent that talks to a model server you host. One Go binary with the
web UI compiled in. The transcript, the tools and the state stay on your machine.

## Run

You need a reachable model server: a `llama-server`, a llama.cpp router, or anything
that speaks the OpenAI chat API. The harness works out which on the first request.

```sh
make build
./quecto-harness
```

It binds `127.0.0.1:7331`, prints a URL carrying a session token and opens a
browser at it. The token is the only access control, so keep the bind on
loopback.

```
--listen host:port   Bind elsewhere, for this process only
--no-open            Do not open a browser after binding
--home dir           State directory, default ~/.quecto-harness

quecto-harness open                Open the browser at the saved URL
quecto-harness service install     Write and load the launchd or systemd unit
quecto-harness service uninstall   Unload and remove it
quecto-harness service status      Report unit presence, run state, URL
quecto-harness version             Print version, commit, Go version
```

The first run writes `~/.quecto-harness/config.json`. Set the server URL, pick a
model and add your project folders in settings, or edit that file. A session is
scoped to one project folder and tool calls cannot leave it. Every write and
every command is asked about until you change the permission mode to
`allowlist` or `full`.

Configuration, the allowlist, sessions and logs live under `~/.quecto-harness`.
Nothing is written into a project folder.

## Build from source

Needs Go 1.24 and Node. `make build` runs the Vite build into
`internal/web/dist` and then the binary that embeds it, in that order: editing
anything under `web/` without rebuilding the UI leaves the previous page
compiled in. `make check` runs gofmt, vet and the race tests.

For UI work, run `make run` for a backend-only binary and `npm run dev` in
`web/`, which serves the page on Vite's port and proxies `/api` to the harness.

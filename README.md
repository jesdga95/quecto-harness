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

The first run writes `~/.quecto-harness/config.json`. The setup screen asks for
one agent: name it, give it the server address and pick a model. Add your
project folders in settings, or edit that file. A session is scoped to one
project folder and tool calls cannot leave it. Every write and every command is
asked about until you change the permission mode to `allowlist` or `full`.

Configuration, the allowlist, sessions and logs live under `~/.quecto-harness`.
Nothing is written into a project folder.

## Features

- **Agents.** Any number of named agents, one selected at a time. Each carries
  its own address, key, protocol (llama.cpp, OpenAI chat or OpenAI responses),
  model, reasoning effort, sampling, context and instructions. Picking a model
  loads nothing: it loads on the first message that needs it.
- **Plan and execute.** A session runs in one of the two modes. Plan offers no
  tool that writes a file: the agent reads, searches and asks, then submits a
  plan. The card offers to approve it and switch to execute, or to keep
  planning.
- **Tools.** `read`, `grep`, `write`, `edit`, `bash`,
  `todo_write`, `ask_user_question`, `web_search`, `subagent` and `send_message`,
  plus `write_plan` and `exit_plan_mode` in plan mode.
- **Permissions.** The card offers Allow once, Allow always and Deny, the
  denial carrying an optional reason back to the model. An always on a shell
  command writes its prefix to the project's allowlist, and a command with a
  shell operator is asked even when its prefix is listed.
- **Delegation.** `subagent` starts a subagent run on a named agent, hands it
  the task and carries on while it works; `send_message` reaches it. Runs go
  one level deep, ask the person nothing, and report to their launcher. An
  agent names the agents it may hand work to, and its concurrency bounds the
  runs it answers at once.
- **Web search.** `web_search` asks the provider named in settings, Exa being
  the first. A query leaves the machine and the call is billed, so in ask mode
  each search is asked.
- **Attachments.** A message can carry files picked from the host and text
  pasted from anywhere; an image goes to the model as data.
- **Context.** The meter shows how full the window is. Past the threshold the
  session compacts itself into a summary, and a compact can also be run by
  hand on an idle session. Any message can be resent from where it stands in
  the transcript, and a message sent while a turn is running is folded into
  the next step of it.
- **Transcript and changes.** Replies render as markdown with highlighted code
  and TeX, and an SVG figure or HTML page the agent writes shows in the page.
  The changes panel lists the files the session has written, each as a diff
  against what it held before.

## Build from source

Needs Go 1.24 and Node. `make build` runs the Vite build into
`internal/web/dist` and then the binary that embeds it, in that order: editing
anything under `web/` without rebuilding the UI leaves the previous page
compiled in. `make check` runs gofmt, vet and the race tests.

For UI work, run `make run` for a backend-only binary and `npm run dev` in
`web/`, which serves the page on Vite's port and proxies `/api` to the harness.

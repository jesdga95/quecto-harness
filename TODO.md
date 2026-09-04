# Quecto Harness: state of play

Ordered by size. Take from the top.

## Next, large

1. **Diff view.** Show what `write_file` changed rather than the summary line.
   Needs the old content kept before the write, a diff, and a view for it. The
   quectogram diff view is the reference.
2. **Git view.** Status and diff for the project, either by shelling out to git
   or by a small set of read-only routes. Decide which before starting.
3. **Image attachments.** Multimodal request shape, storage, a picker, and a
   view. Three of the six models report vision, so the caps are there.

## Done

- A tool call budget, set from the plus button and rotating to no limit at all.
  A spent budget takes the tools out of the next request rather than ending the
  turn, so it finishes with an answer.
- A compaction opens and shows the summary it kept. Expandable step rows carry
  a caret.
- Syntax highlighting for fenced code, from highlight.js core with the
  languages registered by hand and a six colour palette of the page's own.
- The model names the session after its first turn, continuing the session's
  own messages so the prompt stays cached.
- Shell steps open as a console: the command behind a prompt, output in mono,
  and how the run ended under it.
- Text file attachments, picked in the host dialog and inlined into the message
  with their path.
- A plan the model keeps with write_todos, shown as a panel above the composer
  and as a step in the transcript.
- Go backend: config, llama client with SSE parsing, three tools, permission
  broker, agent loop, session store, HTTP server, launchd and systemd units.
- Svelte UI embedded in the binary. One 9.3 MB file, no runtime, no network.
- Real HTTP and SSE wiring. The mock still runs under `VITE_MOCK=1 npm run dev`.
- Markdown and LaTeX for assistant output, with raw HTML escaped and links
  restricted.
- Setup screen: address, key, probe, and the model list, as a split panel.
- Native folder dialog for adding a project, with the path cleaned before it is
  stored.
- A new session picks a loaded model on its own.
- Toasts for errors. A lost connection and an expired token stay until they
  clear.
- Confirmation dialog as an atom. Removing a project deletes its conversations
  and says how many first.
- Rename and delete for sessions, remove for projects, from a persistent menu.
- Fixed the crash that made compacted sessions render nothing: a decided
  permission was written with an empty id, and a repeated key aborts a keyed
  list in Svelte.
- Fixed the freeze after a stop: a stopped card is now closed on the stream
  instead of blocking the composer until a reload, and ids minted for live
  items can no longer collide with ids built from stored entries.

## Known, not scheduled

- `web/src/app.css` declares `.textarea` twice. Harmless, worth merging.
- Sessions accumulate on disk with no size limit.
- `write_file` content lives in the assistant entry's tool call arguments, so a
  session that writes large files produces a large session file.

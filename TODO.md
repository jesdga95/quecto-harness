# Quecto Harness: state of play

Ordered by size. Take from the top.

## Next, small

1. **Expand a compaction.** The summary is already in the entry the divider is
   built from, so this is a caret and a collapsed block. No server work.
2. **Syntax highlighting.** One dependency and a renderer override in
   `markdown.ts`. Pick a small library and a theme that reads in both schemes.
   The language label is already shown.
3. **Automatic session titles.** The first user message is already the fallback
   title. Either keep that and trim it better, or ask the model for a short
   title after the first turn, which costs a second request.

## Medium

4. **Shell output as a shell.** One compact card per command: the command on a
   prompt line, output in mono, exit code as a badge, collapsed by default when
   it is long. Look at how the quectogram android client presents the three
   tools before starting.
5. **Text file attachments.** A picker, a size cap, and the file inlined into
   the message with its path. The read path already exists in `read_file`, so
   the work is the composer and the request shape.
6. **Todo lists inside the harness.** A tool the model calls to keep a plan, and
   a panel that renders it. Needs a fourth tool, a place in the session file,
   and an event.

## Large

7. **Diff view.** Show what `write_file` changed rather than the summary line.
   Needs the old content kept before the write, a diff, and a view for it. The
   quectogram diff view is the reference.
8. **Git view.** Status and diff for the project, either by shelling out to git
   or by a small set of read-only routes. Decide which before starting.
9. **Image attachments.** Multimodal request shape, storage, a picker, and a
   view. Three of the six models report vision, so the caps are there.

## Done

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

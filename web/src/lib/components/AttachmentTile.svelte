<script lang="ts">
  import Play from "phosphor-svelte/lib/PlayIcon";
  import X from "phosphor-svelte/lib/XIcon";
  import type { PreviewKind } from "../attachments";
  import { sheetText } from "../attachments";
  import { bytes, fileKind, fileName } from "../format";
  import { render } from "../markdown";
  import { thumbFrame } from "../thumb";

  let {
    path,
    kind,
    size = 0,
    src = "",
    text,
    refused = "",
    small = false,
    selected = false,
    onopen,
    onremove,
    onfail,
  }: {
    path: string;
    kind: PreviewKind;
    size?: number;
    /** Where an image, a video or a PDF is served from. */
    src?: string;
    /** The file's text, for a text or markdown file. */
    text?: string;
    refused?: string;
    /** The strip in the panel, which draws a smaller tile with no name. */
    small?: boolean;
    selected?: boolean;
    onopen?: () => void;
    onremove?: () => void;
    /** Told when the file will not load, so the caller can stop asking. */
    onfail?: () => void;
  } = $props();

  const name = $derived(fileName(path));
  const media = $derived((kind === "image" || kind === "video") && !!src);
  // A page in miniature: the file's own first lines under its name.
  const paper = $derived(
    (kind === "pdf" && !!src) || ((kind === "text" || kind === "markdown") && text !== undefined),
  );
  const title = $derived(refused ? `Not sent: ${refused}` : path);

  function key(event: KeyboardEvent) {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    onopen?.();
  }
</script>

<!-- The tile is a button only when it opens something. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div
  class="tile"
  class:tile-shot={media}
  class:tile-paper={paper}
  class:tile-doc={!media && !paper}
  class:is-small={small}
  class:is-on={selected}
  class:is-refused={!!refused}
  class:is-open={!!onopen}
  role={onopen ? "button" : undefined}
  tabindex={onopen ? 0 : undefined}
  aria-label={onopen ? `Show ${name}` : undefined}
  {title}
  onclick={onopen}
  onkeydown={onopen ? key : undefined}
>
  {#if media && kind === "image"}
    <img {src} alt={name} onerror={onfail} />
  {:else if media}
    <video use:thumbFrame {src} preload="metadata" muted playsinline onerror={onfail}></video>
    <span class="tile-play"><Play weight="fill" /></span>
  {:else if paper}
    {#if kind === "pdf"}
      <div class="tile-sheet is-pdf" aria-hidden="true">
        <iframe src="{src}#toolbar=0&navpanes=0&scrollbar=0&view=FitH" tabindex="-1" title=""></iframe>
      </div>
    {:else if kind === "markdown"}
      <!-- render escapes and allowlists everything it emits. -->
      <div class="tile-sheet is-md" aria-hidden="true">{@html render(sheetText(text ?? ""))}</div>
    {:else}
      <div class="tile-sheet" aria-hidden="true">{sheetText(text ?? "")}</div>
    {/if}
    <span class="tile-plate">
      <span class="tile-kind">
        {fileKind(path)}
        {#if refused}<span class="tile-size">Not sent</span>
        {:else if size > 0}<span class="tile-size">{bytes(size)}</span>{/if}
      </span>
      <span class="tile-name">{name}</span>
    </span>
  {:else}
    <span class="tile-kind">
      {fileKind(path)}
      {#if refused}<span class="tile-size">Not sent</span>
      {:else if size > 0}<span class="tile-size">{bytes(size)}</span>{/if}
    </span>
    <span class="tile-name">{name}</span>
  {/if}
  {#if onremove}
    <button
      class="tile-x"
      aria-label="Remove {name}"
      onclick={(event) => {
        event.stopPropagation();
        onremove?.();
      }}
    >
      <X />
    </button>
  {/if}
</div>

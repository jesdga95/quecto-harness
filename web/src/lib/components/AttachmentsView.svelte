<script lang="ts">
  import ArrowLeft from "phosphor-svelte/lib/ArrowLeftIcon";
  import ArrowSquareOut from "phosphor-svelte/lib/ArrowSquareOutIcon";
  import CaretLeft from "phosphor-svelte/lib/CaretLeftIcon";
  import CaretRight from "phosphor-svelte/lib/CaretRightIcon";
  import Code from "phosphor-svelte/lib/CodeIcon";
  import DownloadSimple from "phosphor-svelte/lib/DownloadSimpleIcon";
  import Eye from "phosphor-svelte/lib/EyeIcon";
  import FileDoc from "phosphor-svelte/lib/FileDocIcon";
  import MagnifyingGlassMinus from "phosphor-svelte/lib/MagnifyingGlassMinusIcon";
  import MagnifyingGlassPlus from "phosphor-svelte/lib/MagnifyingGlassPlusIcon";
  import Pause from "phosphor-svelte/lib/PauseIcon";
  import Play from "phosphor-svelte/lib/PlayIcon";
  import SpeakerHigh from "phosphor-svelte/lib/SpeakerHighIcon";
  import SpeakerSlash from "phosphor-svelte/lib/SpeakerSlashIcon";
  import CopyButton from "./CopyButton.svelte";
  import FileIcon from "./FileIcon.svelte";
  import Prose from "./Prose.svelte";
  import { previewKind } from "../attachments";
  import { bytes, fileKind, fileName, timeOf } from "../format";
  import { escapeHtml, languageOfPath, sourceHtml } from "../highlight";
  import { app, attachmentUrl, openAttachment, sessionAttachments, type SessionAttachment } from "../state/app.svelte";
  import { thumbFrame } from "../thumb";

  const groups = $derived(sessionAttachments());
  const all = $derived(groups.flatMap((g) => g.files));
  // One file is shown when one is asked for. Otherwise the list.
  const shown = $derived(all.find((f) => f.key === app.attachment));
  const at = $derived(shown ? all.indexOf(shown) : -1);
  const kind = $derived(shown ? previewKind(shown.path, shown.kind) : "other");
  const url = $derived(shown ? attachmentUrl(shown) : "");
  const name = $derived(shown ? fileName(shown.path) : "");

  // Markdown is read rendered until the code is asked for.
  let code = $state(false);
  $effect(() => {
    void shown?.key;
    code = false;
    zoom = "fit";
    facts = "";
  });

  // Measured off the file once it loads: an image's size, a video's length.
  let facts = $state("");

  function about(file: SessionAttachment): string {
    const parts = [fileKind(file.path)];
    if (file.bytes > 0) parts.push(bytes(file.bytes));
    if (file.refused) parts.push("Not sent");
    return parts.join(" · ");
  }

  // An image is fitted to the panel until zoomed, and a zoom is a scale of
  // its own pixels.
  const stops = [0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4];
  let zoom = $state<"fit" | number>("fit");
  let stage = $state<HTMLDivElement | null>(null);
  let img = $state<HTMLImageElement | null>(null);

  function fitted(): number {
    if (!stage || !img || !img.naturalWidth) return 1;
    return Math.min((stage.clientWidth - 40) / img.naturalWidth, (stage.clientHeight - 40) / img.naturalHeight, 1);
  }
  function zoomBy(dir: number) {
    const now = zoom === "fit" ? fitted() : zoom;
    const next = dir > 0 ? stops.find((s) => s > now + 0.01) : [...stops].reverse().find((s) => s < now - 0.01);
    if (next) zoom = next;
  }
  function wheel(event: WheelEvent) {
    if (!event.ctrlKey && !event.metaKey) return;
    event.preventDefault();
    zoomBy(-event.deltaY);
  }

  let video = $state<HTMLVideoElement | null>(null);
  let paused = $state(true);
  let muted = $state(false);
  let now = $state(0);
  let length = $state(0);

  function clock(t: number): string {
    if (!Number.isFinite(t)) return "0:00";
    return `${Math.floor(t / 60)}:${String(Math.floor(t % 60)).padStart(2, "0")}`;
  }
  function metadata() {
    if (!video) return;
    length = Number.isFinite(video.duration) ? video.duration : 0;
    facts = `${video.videoWidth} × ${video.videoHeight}${length ? ` · ${clock(length)}` : ""}`;
    video.currentTime = length > 0 ? Math.min(length * 0.1, 3) : 0.1;
  }
  function toggle() {
    if (!video) return;
    if (video.paused) void video.play();
    else video.pause();
  }
  function seek(event: PointerEvent) {
    const track = event.currentTarget as HTMLElement;
    const box = track.getBoundingClientRect();
    const to = (x: number) => {
      if (video && length) video.currentTime = Math.max(0, Math.min(1, (x - box.left) / box.width)) * length;
    };
    to(event.clientX);
    const move = (e: PointerEvent) => to(e.clientX);
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", () => window.removeEventListener("pointermove", move), { once: true });
  }

  const text = $derived(shown?.content ?? "");
  const lines = $derived(text === "" ? 0 : text.replace(/\n$/, "").split("\n").length);
  const gutter = $derived(Array.from({ length: lines }, (_, i) => i + 1).join("\n"));
  // A file with no known language is shown as it is, not guessed at: prose
  // pasted as text reads as a language often enough to be coloured wrong.
  const markup = $derived(
    !shown || !(kind === "text" || (kind === "markdown" && code))
      ? ""
      : languageOfPath(shown.path)
        ? sourceHtml(text, shown.path)
        : escapeHtml(text),
  );
  const sub = $derived.by(() => {
    if (!shown) return "";
    const parts = [about(shown)];
    if (kind === "text" || kind === "markdown") parts.push(`${lines} lines`);
    if (facts) parts.push(facts);
    return parts.join(" · ");
  });

  // Text is saved from what the session holds. A stored file is fetched from
  // the harness under its own name.
  function download(file: SessionAttachment) {
    const link = document.createElement("a");
    const href = attachmentUrl(file);
    let blob = "";
    if (href) link.href = href;
    else {
      blob = URL.createObjectURL(new Blob([file.content ?? ""], { type: "text/plain" }));
      link.href = blob;
    }
    link.download = fileName(file.path);
    link.click();
    if (blob) setTimeout(() => URL.revokeObjectURL(blob), 1000);
  }

  function step(by: number) {
    if (at < 0) return;
    openAttachment(all[(at + by + all.length) % all.length].key);
  }

  // While a file is shown, the arrow keys step through the rest and Escape
  // goes back to the list, unless something is being typed.
  function keys(event: KeyboardEvent) {
    if (!shown) return;
    const target = event.target as HTMLElement | null;
    if (target?.closest("input, textarea, [contenteditable]")) return;
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") step(event.key === "ArrowRight" ? 1 : -1);
    else if (event.key === "Escape") openAttachment("");
    else return;
    event.preventDefault();
  }
</script>

<svelte:window onkeydown={keys} />

{#if !shown}
  <div class="att-list">
    {#if all.length === 0}
      <p class="artifact-wait">Nothing has been attached in this session.</p>
    {/if}
    {#each groups as group (group.id)}
      <div class="att-group">
        <span class="att-who">Your message</span>
        {#if group.at}<span class="att-time num">{timeOf(group.at)}</span>{/if}
      </div>
      {#each group.files as file (file.key)}
        {@const look = previewKind(file.path, file.kind)}
        {@const src = attachmentUrl(file)}
        <div class="att-row" title={file.path}>
          <button class="att-open" onclick={() => openAttachment(file.key)}>
            <span class="att-thumb">
              {#if look === "image" && src}
                <img {src} alt="" />
              {:else if look === "video" && src}
                <video use:thumbFrame {src} preload="metadata" muted playsinline></video>
                <span class="tile-play"><Play weight="fill" /></span>
              {:else}
                <FileIcon path={file.path} size={18} />
              {/if}
            </span>
            <span class="att-txt">
              <span class="att-name">{fileName(file.path)}</span>
              <span class="att-sub num">{about(file)}</span>
            </span>
          </button>
          {#if src || file.content}
            <button class="icon-btn att-dl" aria-label="Download {fileName(file.path)}" title="Download" onclick={() => download(file)}>
              <DownloadSimple />
            </button>
          {/if}
        </div>
      {/each}
    {/each}
  </div>
{:else}
  <div class="panel-path att-head">
    <button class="icon-btn" aria-label="All attachments" title="All attachments" onclick={() => openAttachment("")}>
      <ArrowLeft />
    </button>
    <div class="att-meta">
      <span class="att-name" title={shown.path}>{name}</span>
      <span class="att-sub num">{sub}</span>
    </div>
    <div class="flex shrink-0 items-center gap-0.5">
      {#if kind === "image" && url}
        <button class="icon-btn" aria-label="Zoom out" title="Zoom out" onclick={() => zoomBy(-1)}>
          <MagnifyingGlassMinus />
        </button>
        <button class="att-zoom num" title="Fit to the panel" onclick={() => (zoom = "fit")}>
          {zoom === "fit" ? "Fit" : `${Math.round(zoom * 100)}%`}
        </button>
        <button class="icon-btn" aria-label="Zoom in" title="Zoom in" onclick={() => zoomBy(1)}>
          <MagnifyingGlassPlus />
        </button>
      {/if}
      {#if kind === "markdown"}
        <button
          class="icon-btn"
          aria-label={code ? "Show the preview" : "Show the code"}
          title={code ? "Preview" : "Code"}
          onclick={() => (code = !code)}
        >
          {#if code}<Eye />{:else}<Code />{/if}
        </button>
      {/if}
      {#if (kind === "text" || kind === "markdown") && text}
        <CopyButton {text} label="Copy" />
      {/if}
      {#if url && kind !== "other"}
        <a class="icon-btn" href={url} target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab" title="Open in a new tab">
          <ArrowSquareOut />
        </a>
      {/if}
      {#if url || text}
        <button class="icon-btn" aria-label="Download" title="Download" onclick={() => download(shown)}>
          <DownloadSimple />
        </button>
      {/if}
      {#if all.length > 1}
        <span class="att-sep"></span>
        <button class="icon-btn" aria-label="Previous attachment" title="Previous" onclick={() => step(-1)}>
          <CaretLeft />
        </button>
        <span class="att-step num">{at + 1}/{all.length}</span>
        <button class="icon-btn" aria-label="Next attachment" title="Next" onclick={() => step(1)}>
          <CaretRight />
        </button>
      {/if}
    </div>
  </div>

  {#if shown.refused}
    <p class="artifact-note">Not sent: {shown.refused}</p>
  {/if}

  <div class="artifact-body">
    {#if kind === "image" && url}
      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
      <div
        class="att-image"
        class:is-zoomed={zoom !== "fit"}
        bind:this={stage}
        onclick={() => (zoom = zoom === "fit" ? 1 : "fit")}
        onwheel={wheel}
      >
        <img
          bind:this={img}
          src={url}
          alt={name}
          style:width={zoom === "fit" || !img ? undefined : `${img.naturalWidth * zoom}px`}
          onload={() => img && (facts = `${img.naturalWidth} × ${img.naturalHeight}`)}
        />
      </div>
    {:else if kind === "video" && url}
      <div class="att-video" class:is-playing={!paused}>
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
        <div class="att-frame" onclick={toggle}>
          <!-- svelte-ignore a11y_media_has_caption -->
          <video
            bind:this={video}
            src={url}
            preload="metadata"
            playsinline
            bind:paused
            bind:muted
            bind:currentTime={now}
            onloadedmetadata={metadata}
          ></video>
          <span class="att-bigplay"><Play weight="fill" /></span>
        </div>
        <div class="att-bar">
          <button class="icon-btn" aria-label={paused ? "Play" : "Pause"} title={paused ? "Play" : "Pause"} onclick={toggle}>
            {#if paused}<Play weight="fill" />{:else}<Pause weight="fill" />{/if}
          </button>
          <span class="num">{clock(now)}</span>
          <div class="att-track" role="slider" tabindex="-1" aria-label="Seek" aria-valuemin={0} aria-valuemax={length} aria-valuenow={now} onpointerdown={seek}>
            <span class="att-fill" style:width="{length ? (now / length) * 100 : 0}%"></span>
            <span class="att-knob" style:left="{length ? (now / length) * 100 : 0}%"></span>
          </div>
          <span class="num">{clock(length)}</span>
          <button class="icon-btn" aria-label={muted ? "Unmute" : "Mute"} title={muted ? "Unmute" : "Mute"} onclick={() => (muted = !muted)}>
            {#if muted}<SpeakerSlash />{:else}<SpeakerHigh />{/if}
          </button>
        </div>
      </div>
    {:else if kind === "pdf" && url}
      <iframe class="att-pdf" src="{url}#view=FitH" title={name}></iframe>
    {:else if kind === "markdown" && !code && text}
      <div class="artifact-plan">
        <Prose {text} />
      </div>
    {:else if (kind === "text" || kind === "markdown") && text}
      <div class="artifact-source">
        <div class="artifact-source-scroll">
          <div class="source-rows">
            <pre class="source-gutter num" aria-hidden="true">{gutter}</pre>
            <!-- sourceHtml escapes the text before it colours it. -->
            <pre class="source-code"><code>{@html markup}</code></pre>
          </div>
        </div>
      </div>
    {:else}
      <div class="dock-empty">
        <div class="dock-empty-icon"><FileDoc size={21} /></div>
        <h3>No preview for {fileKind(shown.path)} files</h3>
        <p>It was sent to the model as it is. Download it to open it in the app that made it.</p>
        {#if url}
          <button class="att-btn" onclick={() => download(shown)}><DownloadSimple size={14} />Download</button>
        {/if}
      </div>
    {/if}
  </div>
{/if}

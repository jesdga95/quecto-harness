<script lang="ts">
  import Code from "phosphor-svelte/lib/Code";
  import { codeHtml } from "../highlight";

  let { source }: { source: string } = $props();

  // A page over this size stays code. Nothing that large is a page worth
  // looking at in a message.
  const LIMIT = 512 * 1024;

  // The frame has no origin of its own, so the page reaches neither this
  // document nor any storage, and the policy blocks every request it could
  // make. Both are needed: the sandbox alone would still let it call out.
  const POLICY =
    `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; ` +
    `script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:">`;

  // The page says how tall it wants to be. That number comes from a page that
  // is not trusted, so it is clamped rather than believed.
  const REPORT = `<script>
const post = () => parent.postMessage(document.documentElement.scrollHeight, "*");
addEventListener("load", post);
new ResizeObserver(post).observe(document.documentElement);
<\/script>`;

  const big = $derived(source.length > LIMIT);
  let asCode = $state(false);
  let height = $state(260);
  let frame = $state<HTMLIFrameElement | null>(null);

  const page = $derived(POLICY + REPORT + source.replace(/^\s*<!doctype[^>]*>/i, ""));

  function onmessage(event: MessageEvent) {
    if (!frame || event.source !== frame.contentWindow) return;
    const wanted = Number(event.data);
    if (wanted > 0) height = Math.min(Math.max(wanted, 120), 640);
  }
</script>

<svelte:window {onmessage} />

<div class="md-page">
  {#if !big}
    <button
      class="icon-btn md-page-code"
      aria-label={asCode ? "Show the page" : "Show the source"}
      onclick={() => (asCode = !asCode)}
    >
      <Code />
    </button>
  {/if}
  {#if asCode || big}
    <div class="md-code"><pre><code>{@html codeHtml(source, "xml")}</code></pre></div>
  {:else}
    <iframe
      bind:this={frame}
      title="Page"
      sandbox="allow-scripts"
      srcdoc={page}
      style="height: {height}px"
    ></iframe>
  {/if}
</div>

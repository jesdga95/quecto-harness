<script lang="ts">
  import ArrowDown from "phosphor-svelte/lib/ArrowDown";
  import ArrowsLeftRight from "phosphor-svelte/lib/ArrowsLeftRight";
  import Copy from "phosphor-svelte/lib/Copy";
  import ImageIcon from "phosphor-svelte/lib/Image";
  import Paperclip from "phosphor-svelte/lib/Paperclip";
  import Warning from "phosphor-svelte/lib/Warning";
  import PermissionCard from "./PermissionCard.svelte";
  import Prose from "./Prose.svelte";
  import StepRow from "./StepRow.svelte";
  import { app } from "../state/app.svelte";
  import { bytes, fileName } from "../format";

  let scroller = $state<HTMLDivElement | null>(null);
  let atBottom = $state(true);
  const count = $derived(app.items.length);
  const tail = $derived(app.items.at(-1));

  // Follows the stream while the reader is at the end, and stops following the
  // moment they scroll up.
  $effect(() => {
    void count;
    void (tail && tail.kind === "assistant" ? tail.text.length : 0);
    if (scroller && atBottom) scroller.scrollTop = scroller.scrollHeight;
  });

  function onscroll() {
    if (!scroller) return;
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 40;
  }

  function toBottom() {
    atBottom = true;
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  }

  function copy(text: string) {
    void navigator.clipboard?.writeText(text);
  }
</script>

<div class="relative min-h-0 flex-1">
  <div bind:this={scroller} {onscroll} class="h-full overflow-y-auto">
    <div class="mx-auto flex w-full max-w-[820px] flex-col gap-1.5 px-6 py-6">
      {#each app.items as item (item.id)}
        {#if item.kind === "user"}
          <div class="mt-4 flex flex-col items-end gap-1">
            <div class="bubble">
              {#if item.files.length > 0}
                <div class="bubble-files">
                  {#each item.files as file (file.path)}
                    {#if file.kind === "image" && file.file}
                      <a
                        class="shot"
                        href="/api/files/{file.file}"
                        target="_blank"
                        rel="noopener noreferrer"
                        title={file.path}
                      >
                        <img src="/api/files/{file.file}" alt={fileName(file.path)} />
                      </a>
                    {:else}
                      <span class="bubble-file" title={file.path}>
                        {#if file.kind === "image"}
                          <ImageIcon />
                        {:else}
                          <Paperclip />
                        {/if}
                        <span class="truncate">{fileName(file.path)}</span>
                        {#if file.bytes > 0}
                          <span class="text-muted">{bytes(file.bytes)}</span>
                        {/if}
                      </span>
                    {/if}
                  {/each}
                </div>
              {/if}
              {#if item.text}
                <p class="bubble-text">{item.text}</p>
              {/if}
            </div>
            {#if item.text}
              <button class="icon-btn" aria-label="Copy message" onclick={() => copy(item.text)}>
                <Copy />
              </button>
            {/if}
          </div>
        {:else if item.kind === "assistant"}
          <div class="my-2">
            <Prose text={item.text} streaming={item.streaming} />
          </div>
        {:else if item.kind === "status"}
          <div class="flex items-center gap-2 px-2 py-1 text-[13px] text-muted">
            <ArrowsLeftRight size={15} class="shrink-0" />
            <span class="truncate">{item.text}</span>
          </div>
        {:else if item.kind === "error"}
          <div class="flex items-center gap-2 px-2 py-1 text-[13px] text-crit">
            <Warning size={15} class="shrink-0" />
            <span class="truncate">{item.message}</span>
          </div>
        {:else if item.kind === "permission" && item.outcome === null}
          <div class="my-2">
            <PermissionCard {item} />
          </div>
        {:else}
          <StepRow {item} />
        {/if}
      {/each}
    </div>
  </div>

  {#if !atBottom}
    <button
      class="round absolute bottom-4 left-1/2 -translate-x-1/2 shadow-lg"
      aria-label="Scroll to the end"
      onclick={toBottom}
    >
      <ArrowDown />
    </button>
  {/if}
</div>

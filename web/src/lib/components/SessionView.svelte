<script lang="ts">
  import Composer from "./Composer.svelte";
  import ContextChip from "./ContextChip.svelte";
  import Transcript from "./Transcript.svelte";
  import { activeSession, app } from "../state/app.svelte";
  import { shortPath } from "../format";

  const session = $derived(activeSession());
</script>

<header class="flex h-[56px] shrink-0 items-baseline gap-3 px-6 pt-4">
  <h1 class="max-w-[420px] truncate text-[15px] font-medium">{session?.title ?? "Session"}</h1>
  <span class="truncate text-[12.5px] text-muted">{shortPath(app.projectPath)}</span>
  {#if app.usage.n_ctx > 0}
    <div class="ml-auto self-center">
      <ContextChip />
    </div>
  {/if}
</header>

<Transcript />

<div class="shrink-0 px-6 pt-2 pb-5">
  <div class="mx-auto w-full max-w-[820px]">
    <Composer placeholder="Message the agent" />
  </div>
</div>

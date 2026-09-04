<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Popover from "./Popover.svelte";
  import { app, compact, turnStats } from "../state/app.svelte";
  import { k, level, pct } from "../format";

  let open = $state(false);

  const fill = $derived(app.usage.prompt_tokens + app.usage.completion_tokens);
  const ratio = $derived(app.usage.n_ctx > 0 ? fill / app.usage.n_ctx : 0);
  const percent = $derived(pct(fill, app.usage.n_ctx));
  const tone = $derived(level(ratio));
  const stats = $derived(turnStats());
  const running = $derived(app.status !== "idle");

  // Circumference of the r=7 track, so the dash offset is a percentage of it.
  const circumference = 2 * Math.PI * 7;
  const dash = $derived((percent / 100) * circumference);
</script>

<div class="relative">
  <button
    class="chip chip-solid !gap-1.5 !py-1 !pr-2.5 !pl-1.5"
    class:is-warn={tone === "warn"}
    class:is-crit={tone === "crit"}
    onclick={() => (open = !open)}
    title="Context window"
  >
    <svg viewBox="0 0 18 18" class="!h-[18px] !w-[18px] shrink-0 -rotate-90">
      <circle cx="9" cy="9" r="7" fill="none" stroke="var(--border)" stroke-width="2.5" />
      <circle
        cx="9"
        cy="9"
        r="7"
        fill="none"
        stroke="currentColor"
        stroke-width="2.5"
        stroke-linecap="round"
        stroke-dasharray="{dash} {circumference}"
      />
    </svg>
    <span class="num text-[12px]">{percent}%</span>
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" width={248}>
      <div class="px-2.5 py-2">
        <div class="flex items-baseline justify-between">
          <span class="text-[13px] font-medium">Context</span>
          <span class="num text-[12px] text-muted">{k(fill)} / {k(app.usage.n_ctx)}</span>
        </div>

        <div class="ctxtrack mt-2" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"}>
          <span style="width:{percent}%"></span>
        </div>

        <dl class="mt-3 space-y-1 text-[12px]">
          <div class="flex justify-between">
            <dt class="text-muted">Prompt</dt>
            <dd class="num">{k(app.usage.prompt_tokens)}</dd>
          </div>
          <div class="flex justify-between">
            <dt class="text-muted">Completion</dt>
            <dd class="num">{k(app.usage.completion_tokens)}</dd>
          </div>
          {#if app.usage.cached_pct}
            <div class="flex justify-between">
              <dt class="text-muted">Cache hit</dt>
              <dd class="num">{app.usage.cached_pct}%</dd>
            </div>
          {/if}
          {#if app.usage.tok_per_s}
            <div class="flex justify-between">
              <dt class="text-muted">Speed</dt>
              <dd class="num">{app.usage.tok_per_s} tok/s</dd>
            </div>
          {/if}
          <div class="flex justify-between">
            <dt class="text-muted">Turns</dt>
            <dd class="num">{stats.turns} &middot; {stats.steps} steps</dd>
          </div>
        </dl>

        <button
          class="chip chip-plain mt-3 w-full justify-center"
          onclick={() => {
            open = false;
            void compact();
          }}
          disabled={running}
        >
          <ArrowsIn /> Compact now
        </button>
      </div>
    </Popover>
  {/if}
</div>

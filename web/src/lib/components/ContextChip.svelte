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

  // The parts are measured against the whole window, so each segment is its
  // own share of it and the tail of the bar is what is still free. They arrive
  // once the first turn has been measured, so the bar is plain until then.
  const parts = $derived(app.usage.breakdown);
  const share = (n: number) => pct(n, app.usage.n_ctx);

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

        {#if parts}
          <div class="ctxparts" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"}>
            <div class="ctxsplit mt-2">
              <span class="part-system" style="width:{share(parts.system)}%"></span>
              <span class="part-tools" style="width:{share(parts.tools)}%"></span>
              <span style="width:{share(parts.messages)}%"></span>
            </div>

            <dl class="mt-3 space-y-1 text-[12px]">
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted">
                  <span class="ctxdot part-system"></span> System prompt
                </dt>
                <dd class="num">{k(parts.system)}</dd>
              </div>
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted">
                  <span class="ctxdot part-tools"></span> Tools
                </dt>
                <dd class="num">{k(parts.tools)}</dd>
              </div>
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted">
                  <span class="ctxdot"></span> Messages
                </dt>
                <dd class="num">{k(parts.messages)}</dd>
              </div>
            </dl>
          </div>
        {:else}
          <div class="ctxtrack mt-2" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"}>
            <span style="width:{percent}%"></span>
          </div>
        {/if}

        <dl class="mt-3 space-y-1 text-[12px]">
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

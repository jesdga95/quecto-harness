<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Popover from "./Popover.svelte";
  import { app, compact, turnStats } from "../state/app.svelte";
  import { duration, k, level, pct } from "../format";

  let open = $state(false);

  const fill = $derived(app.usage.prompt_tokens + app.usage.completion_tokens);
  // A window nothing reported and nothing overrode is no limit, so the chip
  // counts the tokens rather than a share of a window.
  const limited = $derived(app.usage.n_ctx > 0);
  const ratio = $derived(limited ? fill / app.usage.n_ctx : 0);
  const percent = $derived(pct(fill, app.usage.n_ctx));
  const tone = $derived(level(ratio));
  const running = $derived(app.status !== "idle");

  // The parts are scaled against the whole window, so each segment is its
  // own share of it and the tail of the bar is what is still free. They
  // arrive once the first turn has run, so the bar is plain until then.
  // With no window they are scaled against the fill, so the bar reads as
  // the composition of the prompt with nothing left free.
  // The system, agents and tools lines are character-count estimates, so
  // they carry a tilde; messages is the remainder of a real usage report.
  const parts = $derived(app.usage.breakdown);
  const share = (n: number) => pct(n, limited ? app.usage.n_ctx : fill);

  // The session so far, summed from what each finished turn recorded. A turn
  // stored before a field was counted adds nothing to it, and a figure no turn
  // has is left out rather than shown as zero.
  const sums = $derived.by(() => {
    const s = {
      turns: 0,
      output: 0,
      input: 0,
      inputSeen: false,
      cached: 0,
      cachedSeen: false,
      llm: 0,
      tool: 0,
      timed: false,
      first: 0,
      firstN: 0,
      gen: 0,
    };
    for (const item of app.items) {
      if (item.kind !== "assistant" || !item.cost) continue;
      const c = item.cost;
      s.turns += 1;
      s.output += c.tokens;
      if (c.input !== undefined) {
        s.input += c.input;
        s.inputSeen = true;
      }
      if (c.cached !== undefined) {
        s.cached += c.cached;
        s.cachedSeen = true;
      }
      if (c.llmMs !== undefined) {
        s.llm += c.llmMs;
        s.tool += c.toolMs ?? 0;
        s.gen += c.genMs ?? 0;
        s.timed = true;
      }
      if (c.firstMs !== undefined && c.requests > 0) {
        s.first += c.firstMs;
        s.firstN += c.requests;
      }
    }
    return s;
  });

  const stats = $derived(turnStats());
  const tps = $derived(sums.gen > 0 ? Math.round(sums.output / (sums.gen / 1000)) : 0);
  const total = $derived(sums.input + sums.output);
  const hit = $derived(
    sums.cachedSeen && sums.input > 0 ? Math.round((sums.cached / sums.input) * 100) : undefined,
  );

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
    <span class="num text-[12px]">{limited ? `${percent}%` : k(fill)}</span>
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" width={288}>
      <div class="px-2.5 py-2">
        <div class="flex items-baseline justify-between">
          <span class="text-[13px] font-medium">Context</span>
          <span class="num text-[12px] text-muted">
            {#if limited}{k(fill)} / {k(app.usage.n_ctx)}{:else}{k(fill)} · No limit{/if}
          </span>
        </div>

        {#if parts}
          <div class="ctxparts" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"}>
            <div class="ctxsplit mt-2">
              <span class="part-system" style="width:{share(parts.system)}%"></span>
              <span class="part-rules" style="width:{share(parts.rules)}%"></span>
              <span class="part-tools" style="width:{share(parts.tools)}%"></span>
              <span style="width:{share(parts.messages)}%"></span>
            </div>

            <dl class="mt-3 space-y-1 text-[12px]">
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted">
                  <span class="ctxdot part-system"></span> System prompt
                </dt>
                <dd class="num">~{k(parts.system)}</dd>
              </div>
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted" title="AGENTS.md in the project root">
                  <span class="ctxdot part-rules"></span> Agents
                </dt>
                <dd class="num">~{k(parts.rules)}</dd>
              </div>
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted">
                  <span class="ctxdot part-tools"></span> Tools
                </dt>
                <dd class="num">~{k(parts.tools)}</dd>
              </div>
              <div class="flex items-center justify-between">
                <dt class="flex items-center gap-1.5 text-muted">
                  <span class="ctxdot"></span> Messages
                </dt>
                <dd class="num">{k(parts.messages)}</dd>
              </div>
            </dl>
          </div>
        {:else if limited}
          <div class="ctxtrack mt-2" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"}>
            <span style="width:{percent}%"></span>
          </div>
        {/if}

        {#if sums.turns > 0}
          <div class="ctx-session">
            <div class="stat-title">This session</div>
            <div class="stat-tiles">
              <div><span>Tokens</span><b class="num">{k(total)}</b></div>
              {#if hit !== undefined}
                <div><span>From cache</span><b class="num">{hit}%</b></div>
              {:else}
                <div><span>Generated</span><b class="num">{k(sums.output)}</b></div>
              {/if}
              {#if tps > 0}
                <div><span>Speed</span><b class="num">{tps}<small> tok/s</small></b></div>
              {/if}
              {#if sums.firstN > 0}
                <div><span>First token</span><b class="num">{duration(sums.first / sums.firstN)}</b></div>
              {/if}
            </div>
            <p class="stat-note">
              {stats.turns} {stats.turns === 1 ? "turn" : "turns"}, {stats.steps} steps{#if sums.timed}. {duration(sums.llm)} on the model, {duration(sums.tool)} on tools{/if}
            </p>
          </div>
        {/if}

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

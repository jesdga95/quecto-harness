<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import ChartPie from "phosphor-svelte/lib/ChartPie";
  import type { TurnCost } from "../api";
  import { app, compact, contextUse, turnStats } from "../state/app.svelte";
  import { duration, k, level } from "../format";

  const use = $derived(contextUse());
  const tone = $derived(level(use.ratio));
  const running = $derived(app.status !== "idle");
  const compactAt = $derived(app.config?.compact_at ?? 0.8);
  const hasTick = $derived(use.limited && compactAt > 0 && compactAt < 1);
  const threshold = $derived(hasTick ? Math.round(app.usage.n_ctx * compactAt) : 0);

  // The parts are measured against the whole window, so the rest of the ring
  // is what is still free. Without a window they are shares of the fill.
  // System, agents and tools are estimates from their text, so they carry a
  // tilde.
  const parts = $derived(app.usage.breakdown);
  const whole = $derived(use.limited ? app.usage.n_ctx : use.fill);
  const share = (n: number) => (whole > 0 ? n / whole : 0);
  const rows = $derived(
    parts
      ? [
          { label: "System prompt", cls: "seg-system", n: parts.system, estimate: true },
          { label: "Agents", cls: "seg-rules", n: parts.rules, estimate: true },
          { label: "Tools", cls: "seg-tools", n: parts.tools, estimate: true },
          { label: "Messages", cls: "seg-msg", n: parts.messages, estimate: false },
        ]
      : [],
  );

  // The ring, clockwise from the top. A window with no measured parts draws
  // its fill alone.
  const RING = 62;
  const CIRC = 2 * Math.PI * RING;
  const arcs = $derived.by(() => {
    const list = parts
      ? rows.map((r) => ({ cls: r.cls, label: `${r.label}, ${r.estimate ? "~" : ""}${k(r.n)}`, f: share(r.n) }))
      : [{ cls: "seg-plain", label: `${k(use.fill)} tokens`, f: use.limited ? Math.min(1, use.ratio) : 1 }];
    let at = 0;
    return list.map((a) => {
      const arc = { ...a, at };
      at += a.f;
      return arc;
    });
  });

  // How many times this conversation has been summarised to make room.
  const compactions = $derived(app.items.filter((i) => i.kind === "compaction").length);
  const compacted = $derived(
    compactions === 0 ? "Never compacted" : compactions === 1 ? "Compacted once" : `Compacted ${compactions} times`,
  );

  // Every finished turn, oldest first, with what it recorded.
  const turns = $derived(
    app.items.flatMap((i) => (i.kind === "assistant" && i.cost ? [i.cost] : [])) as TurnCost[],
  );
  const last = $derived(turns.at(-1));

  // Each run keeps its context in a window of its own. The first three are
  // named, oldest first, and the rest share one part.
  const SHADES = ["seg-a1", "seg-a2", "seg-a3"];
  const windows = $derived.by(() => {
    const measured = [...app.runs].reverse().filter((r) => (r.fill ?? 0) > 0);
    if (measured.length === 0) return null;
    const named = measured.slice(0, SHADES.length).map((r, i) => ({
      id: r.id,
      label: r.title || r.agent || "Subagent",
      n: r.fill ?? 0,
      cls: SHADES[i],
    }));
    const rest = measured.slice(SHADES.length);
    if (rest.length > 0) {
      named.push({
        id: "rest",
        label: `${rest.length} more`,
        n: rest.reduce((s, r) => s + (r.fill ?? 0), 0),
        cls: "seg-a4",
      });
    }
    const subs = named.reduce((s, r) => s + r.n, 0);
    return { runs: named, subs, total: use.fill + subs };
  });

  const sums = $derived.by(() => {
    const s = { output: 0, input: 0, inputSeen: false, cached: 0, cachedSeen: false, requests: 0, ms: 0 };
    const t = { timed: false, llm: 0, gen: 0, tool: 0, first: 0, firstN: 0 };
    for (const c of turns) {
      s.output += c.tokens;
      s.requests += c.requests;
      s.ms += c.ms;
      if (c.input !== undefined) {
        s.input += c.input;
        s.inputSeen = true;
      }
      if (c.cached !== undefined) {
        s.cached += c.cached;
        s.cachedSeen = true;
      }
      if (c.llmMs !== undefined) {
        t.timed = true;
        t.llm += c.llmMs;
        t.gen += c.genMs ?? 0;
        t.tool += c.toolMs ?? 0;
      }
      if (c.firstMs !== undefined && c.requests > 0) {
        t.first += c.firstMs;
        t.firstN += c.requests;
      }
    }
    return { ...s, ...t };
  });
  const stats = $derived(turnStats());
  const tps = $derived(sums.gen > 0 ? Math.round(sums.output / (sums.gen / 1000)) : 0);
  const hit = $derived(
    sums.cachedSeen && sums.input > 0 ? Math.round((sums.cached / sums.input) * 100) : undefined,
  );
  const spent = $derived(sums.first + sums.gen + sums.tool);
  const requests = $derived(turns.flatMap((c) => c.perRequest ?? []));
  const rates = $derived(requests.filter((r) => r.genMs > 0 && r.tokens > 0).map((r) => r.tokens / (r.genMs / 1000)));
  const firsts = $derived(requests.filter((r) => r.firstMs > 0).map((r) => r.firstMs));

  // A line through a series, scaled to a 100 by 28 box with a little room.
  function spark(values: number[]): string {
    const lo = Math.min(...values);
    const hi = Math.max(...values);
    const span = hi - lo || 1;
    return values.map((v, i) => `${(i * 100) / (values.length - 1)},${26 - ((v - lo) / span) * 24}`).join(" ");
  }

  // Round up to a 1, 2, 2.5, 5 or 10 of a power of ten, so the axis is clean.
  const niceCeil = (v: number) => {
    const mag = Math.pow(10, Math.floor(Math.log10(Math.max(v, 1))));
    for (const m of [1, 2, 2.5, 5, 10]) if (m * mag >= v) return m * mag;
    return 10 * mag;
  };

  // The context after every request, turn by turn. A turn that kept no
  // requests of its own adds its last one. The scale is the window where one
  // is known, so the headroom and the compaction line read true.
  let width = $state(0);
  const W = $derived(Math.max(width, 240));
  const H = 150;
  const TOP = 10;
  const BASE = 124;
  const growth = $derived.by(() => {
    const points: { turn: number; req: number; v: number }[] = [];
    turns.forEach((c, i) => {
      const each = (c.perRequest ?? []).flatMap((r) => (r.window !== undefined ? [r.window] : []));
      const vs = each.length > 0 ? each : c.window !== undefined ? [c.window] : [];
      vs.forEach((v, j) => points.push({ turn: i + 1, req: j + 1, v }));
    });
    if (points.length < 2) return null;
    const right = W - 36;
    const max = use.limited ? app.usage.n_ctx : niceCeil(Math.max(...points.map((p) => p.v)));
    const x = (i: number) => 4 + (i * (right - 4)) / (points.length - 1);
    const y = (v: number) => BASE - (Math.min(v, max) / max) * (BASE - TOP);
    const at = points.map((p, i) => ({ ...p, x: x(i), y: y(p.v) }));
    const line = at.map((p) => `${p.x},${p.y}`).join(" ");

    // Where each turn starts and ends along the line.
    const spans: { turn: number; from: number; to: number }[] = [];
    for (const p of at) {
      const span = spans.at(-1);
      if (span && span.turn === p.turn) span.to = p.x;
      else spans.push({ turn: p.turn, from: p.x, to: p.x });
    }
    const step = Math.ceil(spans.length / 6);
    const labels = spans
      .filter((_, i) => i % step === 0 || i === spans.length - 1)
      .map((s) => ({ x: (s.from + s.to) / 2, text: spans.length <= 4 ? `Turn ${s.turn}` : String(s.turn) }));
    const dividers = spans.slice(1).map((s, i) => (spans[i].to + s.from) / 2);

    // The labels on the right, the latest value first. One that would sit on
    // top of a label already placed is left out.
    const end = at[at.length - 1];
    const marks: { y: number; text: string; cls: string }[] = [{ y: end.y, text: k(end.v), cls: "is-end" }];
    if (threshold > 0) marks.push({ y: y(threshold), text: k(threshold), cls: "is-warn" });
    if (use.limited) marks.push({ y: y(max), text: k(max), cls: "" });
    const placed = marks.filter((m, i) => marks.slice(0, i).every((o) => Math.abs(o.y - m.y) >= 11));

    return {
      at,
      line,
      area: `${at[0].x},${BASE} ${line} ${end.x},${BASE}`,
      right,
      labels,
      dividers,
      placed,
      tick: threshold > 0 ? y(threshold) : null,
      top: use.limited ? y(max) : null,
    };
  });

  let hover = $state<number | null>(null);
  function track(e: PointerEvent) {
    if (!growth) return;
    const box = (e.currentTarget as SVGRectElement).ownerSVGElement!.getBoundingClientRect();
    const sx = ((e.clientX - box.left) / box.width) * W;
    let best = 0;
    for (let i = 1; i < growth.at.length; i++) {
      if (Math.abs(growth.at[i].x - sx) < Math.abs(growth.at[best].x - sx)) best = i;
    }
    hover = best;
  }
  const point = $derived(growth && hover !== null ? growth.at[hover] : null);

  // Each turn's time, waiting, generating and in tools, newest turns last.
  // Only the latest are drawn where there are more than fit.
  const MOST = 16;
  const timed = $derived(
    turns
      .map((c, i) => ({ c, n: i + 1 }))
      .filter((t) => t.c.llmMs !== undefined)
      .slice(-MOST),
  );
  const bars = $derived.by(() => {
    if (timed.length === 0) return null;
    const base = 108;
    const peak = Math.max(...timed.map((t) => (t.c.firstMs ?? 0) + (t.c.genMs ?? 0) + (t.c.toolMs ?? 0)), 1);
    const slot = W / timed.length;
    const w = Math.min(56, slot * 0.62);
    const h = (ms: number) => (ms / peak) * (base - 18);
    // Every turn is numbered while a bar's slot holds its number.
    const step = Math.max(1, Math.ceil(24 / slot));
    return {
      base,
      cols: timed.map((t, i) => {
        const x = slot * i + (slot - w) / 2;
        let top = base;
        const segs = [
          { cls: "seg-tools", ms: t.c.toolMs ?? 0 },
          { cls: "seg-msg", ms: t.c.genMs ?? 0 },
          { cls: "seg-system", ms: t.c.firstMs ?? 0 },
        ]
          .filter((s) => s.ms > 0)
          .map((s) => {
            const hh = Math.max(h(s.ms), 2);
            const seg = { cls: s.cls, y: top - hh, h: hh };
            top = seg.y - 1.5;
            return seg;
          });
        return { n: t.n, c: t.c, x, w, segs, top, label: (timed.length - 1 - i) % step === 0 };
      }),
    };
  });

  function turnLine(n: number, c: TurnCost): string {
    const bits = [`Turn ${n} took ${duration(c.ms)}`];
    if (c.window !== undefined) bits.push(`context ${k(c.window)}`);
    if (c.cached !== undefined && c.input) bits.push(`${Math.round((c.cached / c.input) * 100)}% cached`);
    bits.push(`${k(c.tokens)} generated`);
    if (c.tokPerS > 0) bits.push(`${Math.round(c.tokPerS)} tok/s`);
    bits.push(`${c.requests} ${c.requests === 1 ? "request" : "requests"}`);
    return bits.join(", ");
  }
</script>

{#if !use.measured}
  <div class="panel-path">
    <div class="panel-crumbs"><span class="crumb">Not measured</span></div>
  </div>
  <div class="dock-empty">
    <div class="dock-empty-icon"><ChartPie size={21} /></div>
    <h3>Nothing measured yet</h3>
    <p>How the context fills, and what each turn cost, shows here once the first message has gone.</p>
  </div>
{:else}
  <div class="artifact-body">
    <div class="ctxv min-h-0 flex-1 overflow-y-auto">
      <section>
        <h4 class="ctxv-who">
          <span class="ctxv-who-name">
            {#if last?.agent}<b>{last.agent}</b>{/if}
            {#if last?.model}<span>{last.model}</span>{/if}
          </span>
          <span class="ctxv-who-side">{compacted}</span>
        </h4>
        <div class="ctxv-gauge-row">
          <svg
            class="ctxv-gauge"
            viewBox="-80 -80 160 160"
            role="img"
            aria-label={use.limited ? `${use.percent}% of the window used` : `${k(use.fill)} tokens`}
          >
            <circle r={RING} class="ctxv-ring-track" />
            {#each arcs as arc (arc.cls)}
              <circle
                r={RING}
                class="ctxv-arc {arc.cls}"
                transform="rotate(-90)"
                stroke-dasharray="{arc.f * CIRC} {CIRC}"
                stroke-dashoffset={-arc.at * CIRC}
              >
                <title>{arc.label}</title>
              </circle>
            {/each}
            {#if hasTick}
              <line class="ctxv-ring-tick" x1="0" y1="-73" x2="0" y2="-51" transform="rotate({compactAt * 360})" />
            {/if}
            <text class="ctxv-ring-big" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"} y="6" text-anchor="middle">
              {use.limited ? `${use.percent}%` : k(use.fill)}
            </text>
            <text class="ctxv-ring-small" y="26" text-anchor="middle">
              {use.limited ? `of ${k(app.usage.n_ctx)}` : "tokens"}
            </text>
          </svg>
          <dl class="ctxv-stats">
            <div><dt><span class="ctxdot seg-msg"></span>Used</dt><dd class="num">{k(use.fill)}</dd></div>
            {#if use.limited}
              <div>
                <dt><span class="ctxdot is-free"></span>Free</dt>
                <dd class="num">{k(Math.max(0, app.usage.n_ctx - use.fill))}</dd>
              </div>
            {/if}
            {#if threshold > 0}
              <div>
                <dt><span class="ctxv-tick-glyph"></span>{threshold > use.fill ? "Compacts in" : "Compacts at"}</dt>
                <dd class="num">{k(threshold > use.fill ? threshold - use.fill : threshold)}</dd>
              </div>
            {/if}
          </dl>
        </div>
        {#if parts}
          <div class="ctxv-fill">
            {#each rows as row (row.label)}
              <div>
                <span class="ctxdot {row.cls}"></span>{row.label}
                <b class="num">{row.estimate ? "~" : ""}{k(row.n)}</b>
                <i class="num">{(share(row.n) * 100).toFixed(1)}%</i>
              </div>
            {/each}
          </div>
        {/if}
      </section>

      {#if windows}
        <section>
          <h4>All windows<span class="num">{k(windows.total)}</span></h4>
          <div class="ctxv-split" role="img" aria-label="This session {k(use.fill)}, subagents {k(windows.subs)}">
            <span class="seg-msg" style="flex:{use.fill}" title="This session, {k(use.fill)}"></span>
            {#each windows.runs as run (run.id)}
              <span class={run.cls} style="flex:{run.n}" title="{run.label}, {k(run.n)}"></span>
            {/each}
          </div>
          <div class="ctxv-rows">
            <div class="ctxv-row">
              <span class="ctxv-row-name"><span class="ctxdot seg-msg"></span><span>This session</span></span>
              <span class="num">{k(use.fill)}</span>
              <span class="num">{((use.fill / windows.total) * 100).toFixed(1)}%</span>
            </div>
            <div class="ctxv-row">
              <span class="ctxv-row-name"><span class="ctxdot seg-rules"></span><span>Subagents</span></span>
              <span class="num">{k(windows.subs)}</span>
              <span class="num">{((windows.subs / windows.total) * 100).toFixed(1)}%</span>
            </div>
            {#each windows.runs as run (run.id)}
              <div class="ctxv-row is-sub">
                <span class="ctxv-row-name">
                  <span class="ctxdot {run.cls}"></span><span>{run.label}</span>
                </span>
                <span class="num">{k(run.n)}</span>
                <span></span>
              </div>
            {/each}
          </div>
        </section>
      {/if}

      {#if growth}
        <section>
          <h4>Growth<span>By request</span></h4>
          <div class="ctxv-chart-wrap" bind:clientWidth={width}>
            <svg class="ctxv-chart" viewBox="0 0 {W} {H}" width={W} height={H} role="img" aria-label="Context after each request">
              <line x1="4" x2={growth.right} y1={BASE} y2={BASE} stroke="var(--hair)" />
              {#if growth.top !== null}
                <line x1="4" x2={growth.right} y1={growth.top} y2={growth.top} stroke="var(--hair)" />
              {/if}
              {#if growth.tick !== null}
                <line x1="4" x2={growth.right} y1={growth.tick} y2={growth.tick} class="ctxv-threshold" />
              {/if}
              {#each growth.dividers as dx (dx)}
                <line x1={dx} x2={dx} y1={TOP} y2={BASE} stroke="var(--hair)" stroke-dasharray="2 3" />
              {/each}
              {#each growth.labels as l (l.x)}
                <text x={l.x} y={BASE + 15} text-anchor="middle">{l.text}</text>
              {/each}
              <polygon points={growth.area} fill="var(--c-msg)" opacity="0.14" />
              <polyline points={growth.line} fill="none" stroke="var(--c-msg)" stroke-width="2" stroke-linejoin="round" />
              {#each growth.placed as m (m.cls)}
                <text x={growth.right + 6} y={m.y + 3} class={m.cls}>{m.text}</text>
              {/each}
              <circle
                cx={growth.at[growth.at.length - 1].x}
                cy={growth.at[growth.at.length - 1].y}
                r="4"
                class="ctxv-point"
              />
              {#if point}
                <line x1={point.x} x2={point.x} y1={TOP} y2={BASE} class="ctxv-cross" />
                <circle cx={point.x} cy={point.y} r="4" class="ctxv-point" />
              {/if}
              <rect
                x="0"
                y={TOP}
                width={growth.right + 4}
                height={BASE - TOP}
                fill="transparent"
                role="presentation"
                onpointermove={track}
                onpointerleave={() => (hover = null)}
              />
            </svg>
            {#if point}
              <div class="ctxv-tip" style="left:{Math.min(Math.max(point.x, 60), W - 60)}px">
                Turn {point.turn}, request {point.req} <b>{k(point.v)}</b>
              </div>
            {/if}
          </div>
        </section>
      {/if}

      {#if turns.length > 0}
        <section>
          <h4>This session<span>{stats.turns} {stats.turns === 1 ? "turn" : "turns"}, {stats.steps} steps</span></h4>
          <div class="ctxv-cards">
            {#if hit !== undefined}
              <div class="ctxv-card">
                <span class="ctxv-card-label">From cache</span>
                <div class="ctxv-card-pic">
                  <div class="ctxv-mini"><span class="seg-msg" style="flex:{hit}"></span><span class="is-rest" style="flex:{100 - hit}"></span></div>
                </div>
                <b class="num">{hit}%</b>
                <span class="ctxv-card-foot">{k(sums.cached)} of {k(sums.input)} input</span>
              </div>
            {/if}
            {#if tps > 0}
              <div class="ctxv-card">
                <span class="ctxv-card-label">Speed</span>
                <div class="ctxv-card-pic">
                  {#if rates.length > 1}
                    <svg class="ctxv-spark" viewBox="0 0 100 28" preserveAspectRatio="none" aria-hidden="true">
                      <polyline points={spark(rates)} />
                    </svg>
                  {/if}
                </div>
                <b class="num">{tps} tok/s</b>
                <span class="ctxv-card-foot">{rates.length > 1 ? "Per request" : "While generating"}</span>
              </div>
            {/if}
            {#if sums.firstN > 0}
              <div class="ctxv-card">
                <span class="ctxv-card-label">First token</span>
                <div class="ctxv-card-pic">
                  {#if firsts.length > 1}
                    <svg class="ctxv-spark" viewBox="0 0 100 28" preserveAspectRatio="none" aria-hidden="true">
                      <polyline points={spark(firsts)} />
                    </svg>
                  {/if}
                </div>
                <b class="num">{duration(sums.first / sums.firstN)}</b>
                <span class="ctxv-card-foot">{firsts.length > 1 ? "Per request" : "Average"}</span>
              </div>
            {/if}
            <div class="ctxv-card">
              <span class="ctxv-card-label">Generated</span>
              <div class="ctxv-card-pic">
                <div class="ctxv-mini">
                  <span class="seg-msg" style="flex:{Math.max(sums.output, 1)}"></span><span class="is-rest" style="flex:{sums.input}"></span>
                </div>
              </div>
              <b class="num">{k(sums.output)}</b>
              <span class="ctxv-card-foot">
                {sums.input + sums.output > 0 ? ((sums.output / (sums.input + sums.output)) * 100).toFixed(1) : "0"}% of
                {k(sums.input + sums.output)} tokens
              </span>
            </div>
            <div class="ctxv-card">
              <span class="ctxv-card-label">Requests</span>
              <div class="ctxv-card-pic">
                <div class="ctxv-reqs" title="Requests by turn">
                  {#each turns.slice(-MOST) as c, i (i)}
                    <i style="height:{(c.requests / Math.max(...turns.slice(-MOST).map((t) => t.requests), 1)) * 100}%"></i>
                  {/each}
                </div>
              </div>
              <b class="num">{sums.requests}</b>
              <span class="ctxv-card-foot">Over {turns.length} {turns.length === 1 ? "turn" : "turns"}</span>
            </div>
            {#if sums.timed && spent > 0}
              <div class="ctxv-card">
                <span class="ctxv-card-label">Took</span>
                <div class="ctxv-card-pic">
                  <div class="ctxv-mini" title="Waiting {duration(sums.first)}, generating {duration(sums.gen)}, tools {duration(sums.tool)}">
                    <span class="seg-system" style="flex:{sums.first}"></span>
                    <span class="seg-msg" style="flex:{sums.gen}"></span>
                    <span class="seg-tools" style="flex:{sums.tool}"></span>
                  </div>
                </div>
                <b class="num">{duration(sums.ms)}</b>
                <span class="ctxv-card-foot">{Math.round((sums.tool / spent) * 100)}% in tools</span>
              </div>
            {/if}
          </div>
        </section>
      {/if}

      {#if bars}
        <section>
          <h4>
            Time by turn<span class="num">{timed.length < turns.length ? `Last ${timed.length} turns` : duration(sums.ms)}</span>
          </h4>
          <svg class="ctxv-chart" viewBox="0 0 {W} {bars.base + 32}" width={W} height={bars.base + 32} role="img" aria-label="Time by turn">
            <line x1="0" x2={W} y1={bars.base} y2={bars.base} stroke="var(--hair)" />
            {#each bars.cols as col (col.n)}
              <g>
                <title>{turnLine(col.n, col.c)}</title>
                {#each col.segs as seg, j (j)}
                  <rect class={seg.cls} x={col.x} y={seg.y} width={col.w} height={seg.h} rx="2" />
                {/each}
                <rect x={col.x} y="0" width={col.w} height={bars.base} fill="transparent" />
              </g>
              {#if bars.cols.length <= 8}
                <text x={col.x + col.w / 2} y={col.top - 5} text-anchor="middle" class="is-end">{duration(col.c.ms)}</text>
              {/if}
              {#if col.label}
                <text x={col.x + col.w / 2} y={bars.base + 14} text-anchor="middle">
                  {bars.cols.length <= 4 ? `Turn ${col.n}` : col.n}
                </text>
              {/if}
              {#if bars.cols.length <= 4}
                <text x={col.x + col.w / 2} y={bars.base + 26} text-anchor="middle">
                  {col.c.window !== undefined ? `${k(col.c.window)} · ` : ""}{col.c.requests} req
                </text>
              {/if}
            {/each}
          </svg>
          <div class="ctxv-legend">
            <span><span class="ctxdot seg-system"></span>Waiting for the first token<b class="num">{duration(sums.first)}</b></span>
            <span><span class="ctxdot seg-msg"></span>Generating<b class="num">{duration(sums.gen)}</b></span>
            <span><span class="ctxdot seg-tools"></span>Tools<b class="num">{duration(sums.tool)}</b></span>
          </div>
        </section>
      {/if}
    </div>

    <div class="ctxv-foot">
      <button
        class="ctxv-compact"
        onclick={() => void compact()}
        disabled={running || !app.usage.compactable}
        title={!running && !app.usage.compactable ? "Not enough content to compact yet" : undefined}
      >
        <ArrowsIn size={15} />Compact now
      </button>
    </div>
  </div>
{/if}

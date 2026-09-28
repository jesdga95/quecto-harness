<script lang="ts">
  import type { Component } from "svelte";
  import CheckCircle from "phosphor-svelte/lib/CheckCircle";
  import Database from "phosphor-svelte/lib/Database";
  import FileCode from "phosphor-svelte/lib/FileCode";
  import ListNumbers from "phosphor-svelte/lib/ListNumbers";
  import Question from "phosphor-svelte/lib/Question";
  import SealCheck from "phosphor-svelte/lib/SealCheck";
  import Target from "phosphor-svelte/lib/Target";
  import TestTube from "phosphor-svelte/lib/TestTube";
  import Warning from "phosphor-svelte/lib/Warning";
  import { escapeHtml } from "../highlight";
  import type { Plan } from "../plan";
  import { app } from "../state/app.svelte";

  let { plan }: { plan: Plan } = $props();

  // Backticks are the one piece of markdown a plan line uses, for a name or a
  // command. The rest is escaped.
  function inline(text: string): string {
    return escapeHtml(text).replace(/`([^`]+)`/g, "<code>$1</code>");
  }

  // A code or test line names its file first, as "path: what changes", and
  // the path is drawn apart from the words.
  function split(line: string): { path: string; rest: string } {
    const at = line.indexOf(": ");
    if (at <= 0 || at > 120 || /\s/.test(line.slice(0, at))) return { path: "", rest: line };
    return { path: line.slice(0, at), rest: line.slice(at + 2) };
  }

  // While execute carries the plan out, its todo list is these steps, so a
  // step shows where the work has got to.
  const status = $derived.by(() => {
    const out = new Map<string, string>();
    if (app.workMode !== "execute") return out;
    for (const todo of app.todos) out.set(todo.content, todo.status);
    return out;
  });

  const lists: { key: "code" | "tests" | "validation"; label: string; icon: Component; paths: boolean }[] = [
    { key: "code", label: "Code", icon: FileCode, paths: true },
    { key: "tests", label: "Tests", icon: TestTube, paths: true },
    { key: "validation", label: "Validation", icon: SealCheck, paths: false },
  ];
  const none = $derived(/^none\.?$/i.test(plan.migration));
</script>

{#snippet head(Icon: Component, label: string, count?: number, tone = "")}
  <div class="plan-head-row">
    <span class="plan-icon {tone}"><Icon size={15} /></span>
    <span class="plan-title">
      <span class="plan-label">{label}</span>
      {#if count}<span class="num text-[12px] text-muted">{count}</span>{/if}
    </span>
  </div>
{/snippet}

<div class="plan-view">
  {#if plan.goal}
    <section>
      {@render head(Target, "Goal")}
      <p class="plan-body">{@html inline(plan.goal)}</p>
    </section>
  {/if}

  {#each lists as sec (sec.key)}
    {#if plan[sec.key].length > 0}
      <section>
        {@render head(sec.icon, sec.label, plan[sec.key].length)}
        <ul class="plan-body plan-list">
          {#each plan[sec.key] as line, i (i)}
            {@const parts = sec.paths ? split(line) : { path: "", rest: line }}
            <li>
              {#if parts.path}<span class="plan-path">{parts.path}</span>{/if}
              <span>{@html inline(parts.rest)}</span>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/each}

  {#if plan.migration}
    <section>
      {@render head(Database, "Migration")}
      <p class="plan-body" class:text-muted={none}>{@html inline(plan.migration)}</p>
    </section>
  {/if}

  {#if plan.risks.length > 0}
    <section>
      {@render head(Warning, "Risks", plan.risks.length, "is-warn")}
      <ul class="plan-body plan-list">
        {#each plan.risks as line, i (i)}
          <li><span>{@html inline(line)}</span></li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if plan.steps.length > 0}
    <section>
      {@render head(ListNumbers, "Steps", plan.steps.length)}
      <ol class="plan-body plan-steps">
        {#each plan.steps as step, i (i)}
          {@const state = status.get(step) ?? ""}
          <li class:is-done={state === "completed"} class:is-doing={state === "in_progress"}>
            <span class="plan-step-mark">
              {#if state === "completed"}<CheckCircle size={15} weight="fill" />{:else}{i + 1}{/if}
            </span>
            <span>{@html inline(step)}</span>
          </li>
        {/each}
      </ol>
    </section>
  {/if}

  {#if plan.open.length > 0}
    <section>
      {@render head(Question, "Open", plan.open.length)}
      <ul class="plan-body plan-list">
        {#each plan.open as line, i (i)}
          <li><span>{@html inline(line)}</span></li>
        {/each}
      </ul>
    </section>
  {/if}
</div>

<script lang="ts">
  import { untrack } from "svelte";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import type { TranscriptItem } from "../api";
  import { decide } from "../state/app.svelte";

  let { item }: { item: Extract<TranscriptItem, { kind: "permission" }> } = $props();

  let prefix = $state(untrack(() => item.prefix));
  let reason = $state("");
  let asking = $state(false);

  // What the card says the call is about. The mono block below shows the
  // command, the query or the path, which is what the call is of. A subagent
  // asking is named; the conversation's own agent is the one the card is in.
  function cardTitle(tool: string, agent: string): string {
    const action =
      tool === "web_search"
        ? "search the web"
        : tool === "write" || tool === "edit"
          ? "change a file"
          : "run a command";
    return agent ? `${agent} wants to ${action}` : `Wants to ${action}`;
  }
</script>

<div class="card overflow-hidden">
  <div class="flex items-center gap-2 px-4 pt-3 text-[13px]">
    <ShieldCheck size={15} class="text-accent" />
    <span class="font-medium">
      {cardTitle(item.tool, item.agent ?? "")}
    </span>
    {#if item.agent}
      <span class="text-[12px] text-muted">Working for you</span>
    {/if}
  </div>

  <pre class="mono-block mx-4 mt-2.5">{item.command}</pre>

  <div class="flex flex-wrap items-center gap-2 px-4 py-3">
    <button class="chip chip-brand" onclick={() => decide(item, { decision: "once" })}>
      Allow once
    </button>
    {#if item.tool === "bash"}
      <button
        class="chip"
        onclick={() => decide(item, { decision: "always", prefix: prefix.trim() })}
      >
        Allow always
      </button>
      <input class="input !w-[180px] !rounded-full" bind:value={prefix} aria-label="Allowed prefix" />
    {/if}
    <button
      class="chip chip-plain ml-auto text-muted"
      onclick={() =>
        asking ? decide(item, { decision: "deny", reason: reason.trim() }) : (asking = true)}
    >
      Deny
    </button>
  </div>

  {#if asking}
    <div class="flex items-center gap-2 px-4 pb-3">
      <input
        class="input !rounded-full"
        placeholder="Reason, optional"
        bind:value={reason}
        onkeydown={(e) =>
          e.key === "Enter" && decide(item, { decision: "deny", reason: reason.trim() })}
      />
      <button
        class="chip"
        onclick={() => decide(item, { decision: "deny", reason: reason.trim() })}
      >
        Send
      </button>
    </div>
  {/if}

  {#if item.tool === "bash"}
    <p class="px-4 pb-3 text-[11.5px] text-muted">
      Always adds the prefix to the allowlist for this project
    </p>
  {/if}
</div>

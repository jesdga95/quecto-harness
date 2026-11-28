<script lang="ts">
  import { untrack } from "svelte";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheckIcon";
  import type { PermissionDecision, TranscriptItem } from "../api";
  import { app, decide, NO_FOLDER, pickFolder, typedPaths } from "../state/app.svelte";
  import { asksForFolder, mcpOf } from "../steps";

  let {
    item,
    keys = false,
  }: {
    item: Extract<TranscriptItem, { kind: "permission" }>;
    /** Enter and Escape answer this card, so it says so. */
    keys?: boolean;
  } = $props();

  let prefix = $state(untrack(() => item.prefix));
  let reason = $state("");
  let typed = $state(untrack(() => typedPaths.get(item.id) ?? ""));
  let asking = $state(false);
  // Set from the first answer until the harness has it, so a second click
  // cannot send another.
  let sent = $state(false);

  function send(decision: PermissionDecision): Promise<boolean> {
    if (sent) return Promise.resolve(false);
    sent = true;
    return decide(item, decision).finally(() => (sent = false));
  }

  // What the card says the call is about. The mono block below shows the
  // command, the query or the path, which is what the call is of. A subagent
  // asking is named; the conversation's own agent is the one the card is in.
  const mcp = $derived(mcpOf(item.tool));

  // request_directory without a folder asks the user to pick one, which is
  // its own card: the dialog, a typed path, or no folder at all.
  const picking = $derived(asksForFolder(item));
  // A prefix is allowed per project, and a conversation without one has none.
  const canAlways = $derived(item.tool === "bash" && app.projectPath !== NO_FOLDER);

  async function pick(): Promise<void> {
    if (sent) return;
    const path = await pickFolder();
    if (path) void send({ decision: "once", path });
  }

  // A host without a folder dialog still takes a path typed on the card. The
  // harness checks it and leaves the card open if it is refused.
  function useTyped(): void {
    const path = typed.trim();
    if (!path || sent) return;
    typedPaths.set(item.id, path);
    void send({ decision: "once", path });
  }

  // An MCP call's arguments arrive as one JSON line; the card lays them out.
  const shown = $derived.by(() => {
    if (!mcp) return item.command;
    try {
      return JSON.stringify(JSON.parse(item.command), null, 2);
    } catch {
      return item.command;
    }
  });

  function cardTitle(tool: string, agent: string): string {
    const parts = mcpOf(tool);
    if (parts) {
      const action = `use ${parts.tool} on ${parts.server}`;
      return agent ? `${agent} wants to ${action}` : `Wants to ${action}`;
    }
    if (tool === "change_directory") {
      return agent ? `${agent} wants to move this conversation to a folder` : "Wants to move this conversation to a folder";
    }
    if (tool === "request_directory") {
      if (picking) return agent ? `${agent} asks you to choose a folder` : "Choose a folder for this conversation";
      return agent ? `${agent} wants access to a folder` : "Wants access to a folder";
    }
    const action =
      tool === "web_search"
        ? "search the web"
        : tool === "write" || tool === "edit"
          ? "change a file"
          : tool === "read" || tool === "read_image"
            ? "read a file outside the project"
            : tool === "grep"
              ? "search outside the project"
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

  {#if item.description}
    <p class="mx-4 mt-1.5 text-[13px] text-muted">{item.description}</p>
  {/if}

  {#if item.occupied}
    <p class="mx-4 mt-1.5 text-[13px] text-muted">This folder is not empty. The conversation's files will be copied into it, and the files already there are kept as they are.</p>
  {/if}

  {#if picking}
    <p class="mx-4 mt-1.5 text-[13px] text-muted">This conversation can then read and write in it.</p>
    <div class="flex flex-wrap items-center gap-2 px-4 py-3">
      <button class="chip chip-brand chip-tall" disabled={sent} onclick={() => void pick()}>Choose folder</button>
      <input
        class="input input-tall !w-[220px] !rounded-full"
        placeholder="Or type a full path"
        aria-label="Folder path"
        bind:value={typed}
        onkeydown={(e) => e.key === "Enter" && useTyped()}
      />
      <button class="chip chip-tall" disabled={sent || !typed.trim()} onclick={useTyped}>Use path</button>
      <button
        class="chip chip-plain chip-tall ml-auto text-muted"
        disabled={sent}
        onclick={() => send({ decision: "once" })}
      >
        No folder
      </button>
    </div>
  {:else}
    <pre class="mono-block mx-4 mt-2.5">{shown}</pre>
    <div class="flex flex-wrap items-center gap-2 px-4 py-3">
      <button class="chip chip-brand chip-tall" disabled={sent} onclick={() => send({ decision: "once" })}>
        {item.tool === "change_directory" ? "Move" : "Allow once"}
      </button>
      {#if canAlways}
        <button
          class="chip chip-tall"
          disabled={sent}
          onclick={() => send({ decision: "always", prefix: prefix.trim() })}
        >
          Allow always
        </button>
        <input class="input input-tall !w-[180px] !rounded-full" bind:value={prefix} aria-label="Allowed prefix" />
      {:else if mcp}
        <button class="chip chip-tall" disabled={sent} onclick={() => send({ decision: "always", prefix: "" })}>
          Allow always
        </button>
      {/if}
      <button
        class="chip chip-plain chip-tall ml-auto text-muted"
        disabled={sent}
        onclick={() => (asking ? send({ decision: "deny", reason: reason.trim() }) : (asking = true))}
      >
        Deny
      </button>
    </div>
  {/if}

  {#if asking}
    <div class="flex items-center gap-2 px-4 pb-3">
      <input
        class="input input-tall !rounded-full"
        placeholder="Reason, optional"
        bind:value={reason}
        onkeydown={(e) => e.key === "Enter" && send({ decision: "deny", reason: reason.trim() })}
      />
      <button
        class="chip chip-tall"
        disabled={sent}
        onclick={() => send({ decision: "deny", reason: reason.trim() })}
      >
        Send
      </button>
    </div>
  {/if}

  {#if canAlways || mcp || (keys && !picking)}
    <p class="flex flex-wrap gap-x-3 px-4 pb-3 text-[11.5px] text-muted">
      {#if canAlways}
        <span>Always adds the prefix to the allowlist for this project</span>
      {:else if mcp}
        <span>Always lets this tool run without asking in allowlist mode, in every project</span>
      {/if}
      {#if keys && !picking}
        <span class="ml-auto">Enter allows once, Escape denies</span>
      {/if}
    </p>
  {/if}
</div>

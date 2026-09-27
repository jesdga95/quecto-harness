<script lang="ts">
  import SidebarSimple from "phosphor-svelte/lib/SidebarSimple";
  import { app, artifacts, togglePanel } from "../state/app.svelte";

  // The one way into the panel from the header. It says only what is news:
  // lines the session changed, and a spinner while something is being written
  // or an agent is working.
  const busy = $derived(
    (app.status !== "idle" && artifacts().some((a) => !a.complete)) ||
      app.runs.some((r) => r.status !== "idle"),
  );
</script>

<button
  class="chip chip-plain"
  class:chip-solid={app.panel}
  onclick={togglePanel}
  title={app.panel ? "Close the panel" : "Files, changes and agents"}
  aria-label={app.panel ? "Close the panel" : "Open the panel"}
  aria-expanded={app.panel}
>
  <SidebarSimple mirrored />
  {#if app.changes.files.length > 0}
    <span class="num stat-add text-[12px]">+{app.changes.added}</span>
    <span class="num stat-del text-[12px]">&minus;{app.changes.removed}</span>
  {/if}
  {#if busy}
    <span class="spinner"></span>
  {/if}
</button>

<script lang="ts">
  import DotsThree from "phosphor-svelte/lib/DotsThree";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import Trash from "phosphor-svelte/lib/Trash";
  import Popover from "./Popover.svelte";

  let {
    onrename,
    ondelete,
    deleteLabel = "Delete",
  }: {
    onrename?: () => void;
    ondelete: () => void;
    deleteLabel?: string;
  } = $props();

  let open = $state(false);

  function close() {
    open = false;
  }
</script>

<div class="relative">
  <button
    class="row-menu"
    class:is-open={open}
    aria-label="Row options"
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={(e) => {
      e.stopPropagation();
      open = !open;
    }}
  >
    <DotsThree weight="bold" />
  </button>

  {#if open}
    <Popover onclose={close} align="right" width={180}>
      {#if onrename}
        <button
          class="popover-item"
          onclick={() => {
            close();
            onrename();
          }}
        >
          <PencilSimple size={15} />
          Rename
        </button>
      {/if}
      <button
        class="popover-item text-crit"
        onclick={() => {
          close();
          ondelete();
        }}
      >
        <Trash size={15} />
        {deleteLabel}
      </button>
    </Popover>
  {/if}
</div>

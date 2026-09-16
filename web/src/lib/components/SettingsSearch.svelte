<script lang="ts">
  import { untrack } from "svelte";
  import type { SearchAccount, SearchConfig, SearchProbe } from "../api";
  import { searchProviderHint, searchProviderLabel } from "../format";
  import { probeSearch } from "../state/app.svelte";
  import OptionPicker from "./OptionPicker.svelte";

  let {
    search,
    onchange,
  }: {
    search: SearchConfig;
    onchange: (patch: Partial<SearchConfig>) => void;
  } = $props();

  // The account the named provider uses. Switching the provider moves it, and
  // the fields below follow it; the other providers' accounts stay in the
  // draft untouched, which is what makes coming back free.
  const account = $derived(
    search.providers.find((p) => p.name === search.provider) ?? null,
  );

  const providerOptions = $derived([
    { value: "", label: "Off", hint: "Off leaves web_search out of every request." },
    ...search.known.map((name) => ({
      value: name,
      label: searchProviderLabel(name),
      hint: searchProviderHint(name),
    })),
  ]);

  // What the picker names. The draft object is replaced on every keystroke,
  // but the name outlives it, and a derived that answers the same string
  // re-triggers nothing. That is what keeps the refill below out of the
  // fields while they are being typed.
  const providerName = $derived(search.provider);

  let url = $state("");
  let key = $state("");
  let hits = $state("10");
  let chars = $state("4000");
  let timeout = $state("20");
  let editing: "hits" | "chars" | "timeout" | null = $state(null);

  let testing = $state(false);
  let tested = $state(false);
  let probe: SearchProbe | null = $state(null);

  // The pane is remounted for each visit, so the limits are filled one-way
  // from the draft it is pointed at. A commit must not refill the field the
  // user has the caret in, and the refill runs only while the user is not in
  // one, so a value that does not pass validation in one field is not
  // discarded by a commit landing in another.
  $effect(() => {
    const source = untrack(() => search);
    if (editing !== "hits") hits = String(source.max_hits);
    if (editing !== "chars") chars = String(source.max_chars);
    if (editing !== "timeout") timeout = String(source.timeout_s);
  });

  // The address and key follow the named provider, which moves when the
  // picker names another, so a switch shows the other account rather than
  // the words it had. It reacts to the name alone and reads the rest
  // untracked: a keystroke flushes before the commit behind it lands, and an
  // effect that reads the fields would run on the account as it was, and
  // the field the binding writes back from would carry the old key, which
  // erases the character just typed.
  let shown = "";
  $effect(() => {
    const provider = providerName;
    if (provider === shown) return;
    shown = provider;
    const acc = untrack(() => account);
    if (!acc) return;
    url = acc.url;
    key = acc.key;
    tested = false;
    probe = null;
  });

  function setAccount(patch: Partial<SearchAccount>) {
    const providers = search.providers.map((p) =>
      p.name === search.provider ? { ...p, ...patch } : p,
    );
    if (JSON.stringify(providers) !== JSON.stringify(search.providers)) onchange({ providers });
  }

  function commitUrl(text: string) {
    if (account && text.trim() !== account.url) {
      setAccount({ url: text.trim() });
      tested = false;
      probe = null;
    }
  }

  function commitKey(text: string) {
    if (account && text !== account.key) {
      setAccount({ key: text });
      tested = false;
      probe = null;
    }
  }

  // One billed request, sent with what is on screen, so an unsaved key can be
  // tested. The endpoint answers on success, which is what the line draws.
  async function test() {
    if (!search.provider || testing) return;
    commitUrl(url);
    commitKey(key);
    testing = true;
    tested = true;
    probe = null;
    const result = await probeSearch({ provider: search.provider, url: url.trim(), key });
    testing = false;
    if (result) probe = result;
  }

  function commitHits(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(25, Math.max(1, n));
    if (next !== search.max_hits) onchange({ max_hits: next });
  }

  function commitChars(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(100000, Math.max(500, n));
    if (next !== search.max_chars) onchange({ max_chars: next });
  }

  function commitTimeout(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(120, Math.max(1, n));
    if (next !== search.timeout_s) onchange({ timeout_s: next });
  }

  function invalid(text: string, min: number, max: number): boolean {
    const v = Number(text.trim());
    return text.trim() === "" || Number.isNaN(v) || v < min || v > max;
  }

  /** Writes anything typed but not yet committed. Called before the pane is
      pointed at another category or taken off the screen. */
  export function flush() {
    if (invalid(hits, 1, 25)) hits = String(search.max_hits);
    if (invalid(chars, 500, 100000)) chars = String(search.max_chars);
    if (invalid(timeout, 1, 120)) timeout = String(search.timeout_s);
    commitHits(hits);
    commitChars(chars);
    commitTimeout(timeout);
    commitUrl(url);
    commitKey(key);
  }
</script>

<div class="field-card">
  <div class="flex items-center justify-between gap-4">
    <div class="flex min-w-0 flex-col gap-0.5">
      <h4 class="field-head">Provider</h4>
      <span class="text-[12px] text-muted">
        {#if search.provider === ""}
          Off leaves web_search out of every request.
        {:else if !search.ready}
          Named but no working key yet: the tool is not offered until a key is saved.
        {:else}
          The harness offers web_search to every turn.
        {/if}
      </span>
    </div>
    <OptionPicker
      value={search.provider}
      options={providerOptions}
      onpick={(provider) => onchange({ provider })}
      label="Search provider"
      inline
    />
  </div>
</div>

{#if account}
  <div class="field-card">
    <div class="flex items-center justify-between gap-4">
      <h4 class="field-head">{searchProviderLabel(search.provider)}</h4>
      <button
        class="btn btn-sm btn-ghost shrink-0"
        disabled={testing}
        title="Sends one billed search with what is in the fields below"
        onclick={test}
      >
        {testing ? "Testing" : "Test key"}
      </button>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <label class="flex flex-col gap-1.5">
        <span class="label">Address</span>
        <input
          class="input num"
          placeholder="https://api.exa.ai"
          bind:value={url}
          oninput={(e) => commitUrl(e.currentTarget.value)}
          spellcheck="false"
          autocapitalize="off"
        />
      </label>
      <label class="flex flex-col gap-1.5">
        <span class="label">API key</span>
        <input
          class="input"
          type="password"
          placeholder="The key for the account"
          bind:value={key}
          oninput={(e) => commitKey(e.currentTarget.value)}
        />
      </label>
    </div>

    <div class="flex flex-col gap-1.5">
      {#if testing}
        <p class="flex items-center gap-2 text-[12px] text-muted">
          <span class="spinner"></span> Testing the key
        </p>
      {:else if probe?.ok}
        <p class="text-[12px] text-ok">The key works.</p>
      {:else if tested}
        <p class="text-[12px] text-crit">The search provider did not answer. Check the address and the key.</p>
      {/if}
      <span class="text-[11.5px] text-muted">One search is billed, including this test.</span>
    </div>
  </div>
{/if}

<div class="field-card">
  <h4 class="field-head">Limits</h4>
  <div class="grid grid-cols-3 gap-3">
    <label class="flex flex-col gap-1.5">
      <span class="label">Results</span>
      <input
        class="input num"
        bind:value={hits}
        oninput={(e) => commitHits(e.currentTarget.value)}
        onfocus={() => (editing = "hits")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">
        1 to 25. Ten is one request; past ten each result is billed on its own.
      </span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Characters</span>
      <input
        class="input num"
        bind:value={chars}
        oninput={(e) => commitChars(e.currentTarget.value)}
        onfocus={() => (editing = "chars")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">
        What one search may carry in total, shared across its results. The output limit caps it.
      </span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Timeout</span>
      <input
        class="input num"
        bind:value={timeout}
        oninput={(e) => commitTimeout(e.currentTarget.value)}
        onfocus={() => (editing = "timeout")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">Seconds, 1 to 120.</span>
    </label>
  </div>
</div>

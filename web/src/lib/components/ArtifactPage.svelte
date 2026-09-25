<script lang="ts">
  import { withoutOpenTag } from "../markup";

  let { source, complete }: { source: string; complete: boolean } = $props();

  // While the page is written, the frame holds a shell of ours that draws its
  // markup and styles with every script taken out. The page's scripts run once,
  // in a frame of their own, when the fence closes.

  // The run frame has no origin of its own, so the page reaches neither this
  // document nor any storage, and the policy blocks every request it could
  // make. Both are needed: the sandbox alone would still let it call out.
  const POLICY =
    `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; ` +
    `script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:">`;

  const nonce = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");

  // The shell's script is the only one its policy lets run. It parses each
  // copy of the source in an inert document, strips anything that runs or
  // fetches, and changes only the part of the body that differs, so what is
  // already drawn stays put as the page grows.
  const SHELL_SCRIPT = `
const DROP = "script,iframe,frame,frameset,object,embed,applet,base,meta,link,noscript,portal,template";
const URLS = ["href", "src", "srcset", "action", "formaction", "xlink:href", "poster", "background", "ping", "data", "srcdoc"];
function clean(root) {
  for (const el of root.querySelectorAll(DROP)) el.remove();
  for (const el of root.querySelectorAll("*")) {
    for (const attr of [...el.attributes]) {
      const name = attr.name.toLowerCase();
      const value = attr.value.trim();
      if (name.startsWith("on")) el.removeAttribute(attr.name);
      else if (URLS.includes(name)) {
        const keep = (name === "href" && value.startsWith("#")) ||
          (name === "src" && /^data:image\\//i.test(value));
        if (!keep) el.removeAttribute(attr.name);
      }
    }
  }
}
function syncAttrs(to, from) {
  for (const attr of [...to.attributes]) if (!from.hasAttribute(attr.name)) to.removeAttribute(attr.name);
  for (const attr of [...from.attributes]) if (to.getAttribute(attr.name) !== attr.value) to.setAttribute(attr.name, attr.value);
}
function morph(to, from) {
  const a = to.childNodes, b = from.childNodes;
  let i = 0;
  while (i < a.length && i < b.length && a[i].isEqualNode(b[i])) i++;
  if (i === a.length - 1 && i === b.length - 1 && a[i].nodeType === 1 &&
      a[i].nodeName === b[i].nodeName && a[i].nodeName !== "STYLE") {
    syncAttrs(a[i], b[i]);
    morph(a[i], b[i]);
    return;
  }
  while (a.length > i) to.removeChild(to.lastChild);
  for (const node of [...b].slice(i)) to.appendChild(document.importNode(node, true));
}
const styles = document.createElement("style");
document.head.appendChild(styles);
addEventListener("message", (event) => {
  if (event.source !== parent || typeof event.data !== "string") return;
  const doc = new DOMParser().parseFromString(event.data, "text/html");
  clean(doc.documentElement);
  const css = [...doc.head.querySelectorAll("style")].map((s) => s.textContent).join("\\n");
  if (styles.textContent !== css) styles.textContent = css;
  syncAttrs(document.body, doc.body);
  morph(document.body, doc.body);
});`;

  const SHELL =
    `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" ` +
    `content="default-src 'none'; script-src 'nonce-${nonce}'; style-src 'unsafe-inline'; ` +
    `img-src data:; font-src data:"><script nonce="${nonce}">${SHELL_SCRIPT}<\/script>` +
    `</head><body></body></html>`;

  const page = $derived(
    "<!doctype html>" + POLICY + source.replace(/^(\s|<!--[\s\S]*?-->)*<!doctype[^>]*>/i, ""),
  );

  // Live preview: the source is sent once the shell has loaded, and after that
  // at most once a frame.
  let live = $state<HTMLIFrameElement | null>(null);
  let ready = $state(false);
  let queued = 0;

  $effect(() => {
    const markup = withoutOpenTag(source);
    if (!live || !ready || complete) return;
    cancelAnimationFrame(queued);
    queued = requestAnimationFrame(() => live?.contentWindow?.postMessage(markup, "*"));
  });

  $effect(() => () => cancelAnimationFrame(queued));

  // The first load is the page itself. Any later one is the page sending the
  // frame somewhere else, which a policy cannot stop, so the frame is taken
  // down before whatever it loaded can do more.
  let loads = 0;
  let left = $state(false);

  function onRunLoad() {
    loads += 1;
    if (loads > 1) left = true;
  }
</script>

{#if complete}
  {#if left}
    <p class="artifact-wait">
      The page tried to open another address and was stopped. Reload to run it again.
    </p>
  {:else}
    <iframe
      class="artifact-frame"
      title="Page"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      srcdoc={page}
      onload={onRunLoad}
    ></iframe>
  {/if}
{:else}
  <iframe
    bind:this={live}
    class="artifact-frame"
    title="Page, as it is written"
    sandbox="allow-scripts"
    referrerpolicy="no-referrer"
    srcdoc={SHELL}
    onload={() => (ready = true)}
  ></iframe>
{/if}

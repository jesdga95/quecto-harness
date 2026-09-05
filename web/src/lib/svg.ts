// Drawings the model prints. The source is untrusted and usually half
// written, so it is cut back to its last complete tag and then stripped to an
// allowlist. Nothing that can run, fetch or restyle the page survives, which
// leaves out script, foreignObject, a, image, feImage and the animation
// elements.

const SVG_NS = "http://www.w3.org/2000/svg";
const LIMIT = 256 * 1024;
const MAX_WIDTH = 620;
const MAX_HEIGHT = 500;

const ELEMENTS = new Set(
  `svg g defs symbol use switch title desc style path rect circle ellipse line polyline
   polygon text tspan textPath marker linearGradient radialGradient stop pattern clipPath
   mask filter feBlend feColorMatrix feComponentTransfer feComposite feConvolveMatrix
   feDiffuseLighting feDisplacementMap feDistantLight feDropShadow feFlood feFuncA feFuncB
   feFuncG feFuncR feGaussianBlur feMerge feMergeNode feMorphology feOffset fePointLight
   feSpecularLighting feSpotLight feTile feTurbulence`.split(/\s+/),
);

// Index just past the last complete tag. A quote counts only inside a tag, so
// an apostrophe in a label does not swallow the rest of the drawing.
function lastTagEnd(source: string): number {
  let end = 0;
  let quote = "";
  let tag = false;
  for (let i = 0; i < source.length; i += 1) {
    const char = source[i]!;
    if (quote) {
      if (char === quote) quote = "";
    } else if (tag && (char === '"' || char === "'")) {
      quote = char;
    } else if (char === "<") {
      tag = true;
    } else if (char === ">" && tag) {
      tag = false;
      end = i + 1;
    }
  }
  return end;
}

// A reference survives only when it points inside the drawing, so nothing is
// fetched from wherever the model named.
function keeps(el: Element, name: string, value: string): boolean {
  if (/^on/i.test(name)) return false;
  if (name === "href" || name.endsWith(":href")) {
    return el.localName === "use" && value.startsWith("#");
  }
  if (name === "style") return !/url\(\s*(?!#)/i.test(value);
  return true;
}

function clean(el: Element): void {
  for (const attr of [...el.attributes]) {
    if (!keeps(el, attr.name, attr.value)) el.removeAttribute(attr.name);
  }
  for (const child of [...el.children]) {
    const name = child.localName;
    if (child.namespaceURI !== SVG_NS || !ELEMENTS.has(name)) child.remove();
    else if (name === "style" && /@import|url\(\s*(?!#)/i.test(child.textContent ?? ""))
      child.remove();
    else clean(child);
  }
}

/** The drawing as a node ready to insert, or null while there is nothing
    drawable. The html parser is the lenient one and its document is inert,
    so a prefix parses and neither scripts nor fetches happen on the way. */
export function drawing(source: string): SVGSVGElement | null {
  if (source.length > LIMIT) return null;
  const markup = source.slice(0, lastTagEnd(source));
  if (!markup) return null;

  const root = new DOMParser().parseFromString(markup, "text/html").body.querySelector("svg");
  if (!root) return null;
  clean(root);

  // A drawing is scaled to its own box, so a width the model chose cannot
  // stretch the column and the card ends where the drawing does. Without a box
  // there is nothing to scale.
  const width = Number.parseFloat(root.getAttribute("width") ?? "");
  const height = Number.parseFloat(root.getAttribute("height") ?? "");
  if (!root.hasAttribute("viewBox") && width > 0 && height > 0) {
    root.setAttribute("viewBox", `0 0 ${width} ${height}`);
  }
  const box = (root.getAttribute("viewBox") ?? "")
    .trim()
    .split(/[\s,]+/)
    .map(Number);
  const [boxWidth, boxHeight] = [box[2] ?? 0, box[3] ?? 0];
  if (!(boxWidth > 0) || !(boxHeight > 0)) return null;

  const scale = Math.min(MAX_WIDTH / boxWidth, MAX_HEIGHT / boxHeight);
  root.removeAttribute("width");
  root.removeAttribute("height");
  root.setAttribute(
    "style",
    `display:block;width:${Math.round(boxWidth * scale)}px;max-width:100%;` +
      `height:auto;aspect-ratio:${boxWidth}/${boxHeight}`,
  );

  return document.importNode(root, true);
}

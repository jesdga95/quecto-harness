// Which words in a reply name a file, and how a file is drawn: its glyph and
// what the panel can show of it. A name is recognised by a fixed list of
// extensions rather than by asking the harness, so a reply renders without a
// request and a method call such as os.path.join is never taken for a file.

import fileSrc from "phosphor-svelte/lib/File?raw";
import fileCodeSrc from "phosphor-svelte/lib/FileCode?raw";
import fileImageSrc from "phosphor-svelte/lib/FileImage?raw";
import fileTextSrc from "phosphor-svelte/lib/FileText?raw";
import { escapeHtml } from "./highlight";

export type Glyph = "image" | "code" | "text" | "file";

/** What the panel draws: a drawing, a page run in its frame, an image,
    rendered markdown, or the source. */
export type Preview = "svg" | "page" | "image" | "markdown" | "source";

const IMAGE = new Set(["svg", "png", "jpg", "jpeg", "gif", "webp", "avif", "ico", "bmp"]);
const TEXT = new Set(["md", "markdown", "mdx", "txt", "rst", "log", "csv", "tsv", "adoc"]);
const CODE = new Set([
  "py", "go", "ts", "tsx", "js", "jsx", "mjs", "cjs", "rs", "c", "h", "cc", "cpp", "hpp", "cs",
  "java", "kt", "kts", "swift", "rb", "php", "lua", "sh", "bash", "zsh", "fish", "ps1", "sql",
  "html", "htm", "css", "scss", "sass", "less", "svelte", "vue", "astro", "json", "jsonc", "jsonl",
  "yaml", "yml", "toml", "ini", "cfg", "conf", "xml", "env", "mod", "sum", "lock", "gradle", "mk",
  "tf", "hcl", "proto", "graphql", "gql", "r", "jl", "dart", "ex", "exs", "erl", "hs", "ml",
  "scala", "clj", "zig", "nim", "pl", "ipynb", "dockerfile", "nix", "wgsl", "glsl",
]);
const BARE = new Set([
  "Makefile", "Dockerfile", "LICENSE", "README", "Gemfile", "Rakefile", "Procfile", "Justfile",
]);

function extOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

/** The path a piece of inline code or a link names, with any :line or
    :line:col suffix taken off, or null when it does not name a file. */
export function fileTarget(raw: string): string | null {
  let text = raw.trim();
  if (text.startsWith("file://")) {
    try {
      text = decodeURI(text.slice(7));
    } catch {
      return null;
    }
  }
  if (text.length === 0 || text.length > 300 || /\s/.test(text)) return null;
  if (text.includes("://") || text.startsWith("-") || text.startsWith("~")) return null;
  text = text.replace(/:\d+(:\d+)?$/, "");
  if (text.endsWith("/")) return null;
  const segments = text.replace(/^\.\//, "").split("/");
  if (segments.some((s) => !/^[\w.@+-]+$/.test(s) && s !== "")) return null;
  const name = segments.at(-1) ?? "";
  if (BARE.has(name)) return text;
  const ext = extOf(name);
  if (IMAGE.has(ext) || TEXT.has(ext) || CODE.has(ext)) return text;
  return null;
}

export function glyphOf(path: string): Glyph {
  const name = path.split("/").pop() ?? path;
  const ext = extOf(name);
  if (IMAGE.has(ext)) return "image";
  if (TEXT.has(ext)) return "text";
  if (CODE.has(ext) || BARE.has(name)) return "code";
  return "file";
}

export function previewOf(path: string): Preview {
  const ext = extOf(path.split("/").pop() ?? path);
  if (ext === "svg") return "svg";
  if (ext === "html" || ext === "htm") return "page";
  if (ext === "md" || ext === "markdown" || ext === "mdx") return "markdown";
  if (IMAGE.has(ext)) return "image";
  return "source";
}

// The glyphs are phosphor's own regular weight, lifted out of the generated
// component so the ones drawn into markup match the components exactly.
function regular(source: string): string {
  const match = /weight === "regular"\}\s*([\s\S]*?)\s*\{:else/.exec(source);
  const paths = match?.[1] ?? "";
  return `<svg class="md-file-icon" viewBox="0 0 256 256" fill="currentColor" aria-hidden="true">${paths}</svg>`;
}

const ICONS: Record<Glyph, string> = {
  image: regular(fileImageSrc),
  code: regular(fileCodeSrc),
  text: regular(fileTextSrc),
  file: regular(fileSrc),
};

/** A file link as markup, for a renderer that emits strings. label is shown
    as it was written, the path is what a click opens. */
export function fileLinkHtml(path: string, label: string, code: boolean): string {
  const name = escapeHtml(label);
  const text = code ? `<code>${name}</code>` : `<span>${name}</span>`;
  return (
    `<button type="button" class="md-file${code ? " is-code" : ""}" data-file="${escapeHtml(path)}" title="${escapeHtml(path)}">` +
    `${ICONS[glyphOf(path)]}${text}</button>`
  );
}

/** A path as the project names it: relative to its root, which is how the
    harness answers for a file and so how two spellings of one are matched. */
export function inProject(raw: string, root: string): string {
  let p = raw.trim();
  if (root && p.startsWith(root.replace(/\/$/, "") + "/")) p = p.slice(root.replace(/\/$/, "").length + 1);
  return p.replace(/^(\.\/)+/, "");
}

// Markdown and TeX for model output, which is untrusted and usually half
// written. Every branch either escapes what it emits or builds the markup
// itself, so the result is safe to hand to {@html}. An unclosed fence, a lone
// dollar or a table missing its body stays literal text and never throws.

import { Marked, type RendererObject } from "marked";
import katex from "katex";
import { escapeHtml, guess, highlight } from "./highlight";
import { fileLinkHtml, fileTarget } from "./files";

// Formulas are lifted out before the markdown runs and put back afterwards.
// The slots use private use characters, which are stripped from the source
// first so nothing in the text can forge one.
const SLOT_OPEN = "\uE000";
const SLOT_CLOSE = "\uE001";
const SLOT_RE = /\uE000(\d+)\uE001/g;
// A display formula on a line of its own arrives as a paragraph holding one
// slot. That paragraph is dropped so the formula becomes a block of its own.
const LONE_SLOT_RE = /<p>\s*\uE000(\d+)\uE001\s*<\/p>/g;
const PRE_RE = /<pre>[\s\S]*?<\/pre>/g;
interface Formula {
  raw: string;
  tex: string;
  display: boolean;
}

// Only http, https and mailto survive, plus links relative to this page.
// Everything else, javascript: and data: included, loses its href.
export function safeUrl(raw: string | null | undefined): string | null {
  if (!raw) return null;
  const url = raw.replace(/[\u0000-\u0020\u007f]/g, "");
  if (url === "" || url.startsWith("//")) return null;
  if (/^(https?:|mailto:)/i.test(url)) return url;
  if (/^[a-z][a-z0-9+.-]*:/i.test(url)) return null;
  return url;
}

// The info string of a fence names the language and is shown as a label, so it
// is held to a short word. Anything else is dropped and the block renders
// without a label and without colours.
function fenceLanguage(lang: string | undefined): string {
  const word = (lang ?? "").trim().split(/\s+/)[0] ?? "";
  return /^[a-z0-9+#._-]{1,20}$/i.test(word) ? word : "";
}

function runLength(source: string, at: number, char: string): number {
  let n = 0;
  while (source[at + n] === char) n += 1;
  return n;
}

// Index just past the line that closes a fence, or the end of the source when
// the fence is still open.
function fenceEnd(source: string, open: number, char: string, size: number): number {
  const first = source.indexOf("\n", open);
  if (first < 0) return source.length;
  let line = first + 1;
  while (line < source.length) {
    let start = line;
    while (source[start] === " ") start += 1;
    if (runLength(source, start, char) >= size) {
      const nl = source.indexOf("\n", start);
      return nl < 0 ? source.length : nl + 1;
    }
    const nl = source.indexOf("\n", line);
    if (nl < 0) return source.length;
    line = nl + 1;
  }
  return source.length;
}

// Index just past the backtick run that closes a code span, or -1 while the
// span is still open.
function codeSpanEnd(source: string, open: number, size: number): number {
  let i = open + size;
  while (i < source.length) {
    if (source[i] === "`") {
      const run = runLength(source, i, "`");
      if (run === size) return i + run;
      i += run;
      continue;
    }
    i += 1;
  }
  return -1;
}

// Index of the dollar that closes inline math, or -1. The delimiters must hug
// their contents, the closer must not be followed by a digit, and neither a
// backtick nor a blank line may fall between. That keeps prices, shell
// variables and anything quoted as code out of the maths.
function inlineMathEnd(source: string, open: number): number {
  if (/\s/.test(source[open + 1] ?? "")) return -1;
  let i = open + 1;
  while (i < source.length) {
    const char = source[i]!;
    if (char === "\\") {
      i += 2;
      continue;
    }
    if (char === "`") return -1;
    if (char === "\n" && source[i + 1] === "\n") return -1;
    if (char === "$") {
      if (i === open + 1) return -1;
      if (/\s/.test(source[i - 1] ?? "")) return -1;
      if (/[0-9]/.test(source[i + 1] ?? "")) return -1;
      return i;
    }
    i += 1;
  }
  return -1;
}

// A fence inside a candidate span means the opening delimiter was never meant
// as maths, so the span is refused and the delimiter stays literal.
function spansFence(text: string): boolean {
  return /(^|\n)\s*(```|~~~)/.test(text);
}

// Replaces every closed formula, and every drawing, with a slot. Code fences
// and code spans are copied through untouched, so a dollar inside them stays a
// dollar.
function extract(source: string): { text: string; formulas: Formula[] } {
  const src = source.replace(/[\uE000-\uE001]/g, "");
  const formulas: Formula[] = [];
  let out = "";
  let i = 0;

  const slot = (raw: string, tex: string, display: boolean): string => {
    formulas.push({ raw, tex, display });
    return `${SLOT_OPEN}${formulas.length - 1}${SLOT_CLOSE}`;
  };

  while (i < src.length) {
    const char = src[i]!;

    const lineStart = i === 0 || src[i - 1] === "\n";

    if (lineStart && (char === "`" || char === "~")) {
      const size = runLength(src, i, char);
      if (size >= 3) {
        // A fence is code, so what is in it is kept from the formula scan.
        const end = fenceEnd(src, i, char, size);
        out += src.slice(i, end);
        i = end;
        continue;
      }
    }

    if (char === "`") {
      const size = runLength(src, i, "`");
      const end = codeSpanEnd(src, i, size);
      if (end < 0) {
        out += src.slice(i, i + size);
        i += size;
      } else {
        out += src.slice(i, end);
        i = end;
      }
      continue;
    }

    if (char === "\\") {
      const next = src[i + 1];
      if (next === "(" || next === "[") {
        const display = next === "[";
        const close = display ? "\\]" : "\\)";
        const end = src.indexOf(close, i + 2);
        const tex = end < 0 ? "" : src.slice(i + 2, end);
        if (end >= 0 && !(display && spansFence(tex))) {
          out += slot(src.slice(i, end + 2), tex, display);
          i = end + 2;
          continue;
        }
      }
      out += src.slice(i, i + 2);
      i += 2;
      continue;
    }

    if (char === "$") {
      const size = runLength(src, i, "$");
      if (size >= 2) {
        const end = src.indexOf("$$", i + 2);
        const tex = end < 0 ? "" : src.slice(i + 2, end);
        if (end >= 0 && !spansFence(tex)) {
          out += slot(src.slice(i, end + 2), tex, true);
          i = end + 2;
        } else {
          out += src.slice(i, i + size);
          i += size;
        }
        continue;
      }
      const end = inlineMathEnd(src, i);
      if (end >= 0) {
        out += slot(src.slice(i, end + 1), src.slice(i + 1, end), false);
        i = end + 1;
        continue;
      }
    }

    out += char;
    i += 1;
  }

  return { text: out, formulas };
}

// KaTeX is the slowest part of a redraw and the same formula arrives on every
// token of a stream, so finished markup is kept until the map grows large.
const mathCache = new Map<string, string>();

function renderMath(formula: Formula | undefined): string {
  if (!formula) return "";
  const key = (formula.display ? "d:" : "i:") + formula.tex;
  const cached = mathCache.get(key);
  if (cached !== undefined) return cached;

  let html: string;
  try {
    html = katex.renderToString(formula.tex, {
      displayMode: formula.display,
      output: "htmlAndMathml",
      throwOnError: false,
      strict: "ignore",
      trust: false,
      maxSize: 20,
      maxExpand: 400,
    });
  } catch {
    html = `<code>${escapeHtml(formula.tex)}</code>`;
  }
  if (formula.display) html = `<div class="md-math">${html}</div>`;

  if (mathCache.size > 500) mathCache.clear();
  mathCache.set(key, html);
  return html;
}

const renderer: RendererObject = {
  // Fences carry their language as a label and scroll inside their own box,
  // so a long line moves the code rather than the page. The highlighter
  // escapes what it emits; an unknown language falls back to escaped text.
  code({ text, lang }) {
    const language = fenceLanguage(lang);
    const head = language ? `<div class="md-code-lang">${escapeHtml(language)}</div>` : "";
    // A model routinely opens a fence with no language, so an unlabelled
    // block is guessed at rather than left grey.
    const marked = language ? highlight(text, language.toLowerCase()) : guess(text);
    return `<div class="md-code">${head}<pre><code>${marked ?? escapeHtml(text)}</code></pre></div>\n`;
  },

  // Markup written by the model is shown as the text it is, never parsed.
  html({ text }) {
    return escapeHtml(text);
  },

  // Inline code that names a file opens it in the panel.
  codespan({ text }) {
    const ref = fileTarget(text);
    return ref ? fileLinkHtml(ref, text, true) : `<code>${escapeHtml(text)}</code>`;
  },

  link({ href, title, tokens }) {
    // A link to a file has no page to open; the file opens in the panel.
    const ref = /^[a-z][a-z0-9+.-]*:/i.test(href) && !href.startsWith("file://") ? null : fileTarget(href);
    if (ref) return fileLinkHtml(ref, this.parser.parseInline(tokens, this.parser.textRenderer), false);
    const label = this.parser.parseInline(tokens);
    const url = safeUrl(href);
    if (!url) return label;
    const attr = title ? ` title="${escapeHtml(title)}"` : "";
    return `<a href="${escapeHtml(url)}"${attr} target="_blank" rel="noopener noreferrer">${label}</a>`;
  },

  // An image tag would fetch from wherever the model pointed it, so the
  // reference becomes a link the reader can choose to follow.
  image({ href, title, text }) {
    const url = safeUrl(href);
    const label = escapeHtml(text || href || "image");
    if (!url) return label;
    const attr = title ? ` title="${escapeHtml(title)}"` : "";
    return `<a href="${escapeHtml(url)}"${attr} target="_blank" rel="noopener noreferrer">${label}</a>`;
  },

  // Same markup as the default renderer, wrapped so a wide table scrolls.
  table(token) {
    let head = "";
    for (const cell of token.header) head += this.tablecell(cell);
    let body = "";
    for (const row of token.rows) {
      let cells = "";
      for (const cell of row) cells += this.tablecell(cell);
      body += this.tablerow({ text: cells });
    }
    const rows = body ? `<tbody>\n${body}</tbody>\n` : "";
    return `<div class="md-table"><table>\n<thead>\n${this.tablerow({ text: head })}</thead>\n${rows}</table></div>\n`;
  },
};

// breaks is on because a reply is written as chat, where a single newline is
// meant as a line break rather than as a continued paragraph.
const marked = new Marked({ gfm: true, breaks: true, renderer });

function fillMath(html: string, formulas: Formula[]): string {
  // An indented line is code, and a slot that landed in one goes back to the
  // text it was written as rather than becoming a formula inside the block.
  const plain = html.replace(PRE_RE, (block) =>
    block.replace(SLOT_RE, (_, index: string) => escapeHtml(formulas[Number(index)]?.raw ?? "")),
  );
  const unwrapped = plain.replace(LONE_SLOT_RE, (whole, index: string) => {
    const formula = formulas[Number(index)];
    return formula?.display ? renderMath(formula) : whole;
  });
  return unwrapped.replace(SLOT_RE, (_, index: string) => renderMath(formulas[Number(index)]));
}

export function render(source: string): string {
  if (!source) return "";
  try {
    const { text, formulas } = extract(source);
    return fillMath(marked.parse(text, { async: false }), formulas);
  } catch {
    return `<p>${escapeHtml(source)}</p>`;
  }
}

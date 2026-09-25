// Syntax colours for fenced code. Only the languages listed here are bundled,
// since the full library is several megabytes and the interface is embedded in
// the binary. An unknown language renders as plain text.

import hljs from "highlight.js/lib/core";

import bash from "highlight.js/lib/languages/bash";
import c from "highlight.js/lib/languages/c";
import cpp from "highlight.js/lib/languages/cpp";
import csharp from "highlight.js/lib/languages/csharp";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import java from "highlight.js/lib/languages/java";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import kotlin from "highlight.js/lib/languages/kotlin";
import lua from "highlight.js/lib/languages/lua";
import makefile from "highlight.js/lib/languages/makefile";
import markdown from "highlight.js/lib/languages/markdown";
import php from "highlight.js/lib/languages/php";
import python from "highlight.js/lib/languages/python";
import ruby from "highlight.js/lib/languages/ruby";
import rust from "highlight.js/lib/languages/rust";
import sql from "highlight.js/lib/languages/sql";
import swift from "highlight.js/lib/languages/swift";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";

for (const [name, language] of [
  ["bash", bash],
  ["c", c],
  ["cpp", cpp],
  ["csharp", csharp],
  ["css", css],
  ["diff", diff],
  ["dockerfile", dockerfile],
  ["go", go],
  ["ini", ini],
  ["java", java],
  ["javascript", javascript],
  ["json", json],
  ["kotlin", kotlin],
  ["lua", lua],
  ["makefile", makefile],
  ["markdown", markdown],
  ["php", php],
  ["python", python],
  ["ruby", ruby],
  ["rust", rust],
  ["sql", sql],
  ["swift", swift],
  ["typescript", typescript],
  ["xml", xml],
  ["yaml", yaml],
] as const) {
  hljs.registerLanguage(name, language);
}

// Names the model writes on a fence that the library does not answer to.
hljs.registerAliases(["svelte", "vue"], { languageName: "xml" });
hljs.registerAliases("tf", { languageName: "ini" });

/** Marked-up code, or null when the language is unknown or highlighting threw.
    The markup is escaped by the library, so it is safe to insert as html. */
export function highlight(code: string, lang: string): string | null {
  if (!lang || !hljs.getLanguage(lang)) return null;
  try {
    return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch {
    return null;
  }
}

/** Escapes text that is about to be put in markup, for the paths that do not
    go through the highlighter. */
export function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// What a file name says about its language. A name with no extension is
// matched whole, since Makefile and Dockerfile are named rather than suffixed.
const BY_NAME: Record<string, string> = {
  makefile: "makefile",
  dockerfile: "dockerfile",
  gemfile: "ruby",
  rakefile: "ruby",
};

const BY_EXTENSION: Record<string, string> = {
  bash: "bash",
  c: "c",
  cc: "cpp",
  cjs: "javascript",
  conf: "ini",
  cpp: "cpp",
  cs: "csharp",
  css: "css",
  diff: "diff",
  go: "go",
  h: "c",
  hpp: "cpp",
  htm: "xml",
  html: "xml",
  ini: "ini",
  java: "java",
  js: "javascript",
  json: "json",
  jsx: "javascript",
  kt: "kotlin",
  lua: "lua",
  markdown: "markdown",
  md: "markdown",
  mjs: "javascript",
  mk: "makefile",
  patch: "diff",
  php: "php",
  py: "python",
  rb: "ruby",
  rs: "rust",
  scss: "css",
  sh: "bash",
  sql: "sql",
  svelte: "xml",
  svg: "xml",
  swift: "swift",
  toml: "ini",
  ts: "typescript",
  tsx: "typescript",
  vue: "xml",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  zsh: "bash",
};

/** The language a path implies, or an empty string when nothing is known. */
export function languageOfPath(path: string): string {
  const name = (path.split("/").pop() ?? "").toLowerCase();
  const dot = name.lastIndexOf(".");
  if (dot > 0) return BY_EXTENSION[name.slice(dot + 1)] ?? "";
  return BY_NAME[name] ?? "";
}

// The languages a fence with no label is guessed from. A short snippet scores
// low against every one of them, so a guess is taken only when it is clear.
const GUESSES = [
  "bash",
  "c",
  "cpp",
  "css",
  "go",
  "java",
  "javascript",
  "json",
  "markdown",
  "python",
  "rust",
  "sql",
  "typescript",
  "xml",
  "yaml",
];

/** Marked-up code for a fence that named no language, or null when nothing
    scored well enough to be worth colouring. */
export function guess(code: string): string | null {
  try {
    const best = hljs.highlightAuto(code, GUESSES);
    return best.relevance >= 6 ? best.value : null;
  } catch {
    return null;
  }
}

/** Marked-up source of a file the model presented, coloured by its name, or
    as markup when it has none. An unknown extension is guessed at. */
export function sourceHtml(text: string, name: string): string {
  const language = name ? languageOfPath(name) : "xml";
  const marked = language ? highlight(text, language) : guess(text);
  return marked ?? escapeHtml(text);
}

/** Marked-up code, falling back to escaped text, for the views that render
    code outside markdown. */
export function codeHtml(text: string, language: string): string {
  return highlight(text, language) ?? escapeHtml(text);
}

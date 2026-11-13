import type { AttachedKind } from "./api";

/** How an attachment is drawn, on its tile and in the panel. */
export type PreviewKind = "image" | "video" | "pdf" | "markdown" | "text" | "other";

const images = /\.(png|jpe?g|gif|webp)$/i;
const videos = /\.(mp4|m4v|mov|mkv|webm)$/i;
const markdown = /\.(md|markdown|mdx)$/i;
// Formats sent whole that have no preview here.
const opaque = /\.(docx|pptx|xlsx|zip|gz|tar|bin|exe|dmg|iso)$/i;

/** What a file is drawn as. The harness's kind is used once it has read the
    file; before that, the name decides. */
export function previewKind(path: string, kind?: AttachedKind): PreviewKind {
  if (kind === "image" || (!kind && images.test(path))) return "image";
  if (kind === "video" || (!kind && videos.test(path))) return "video";
  if (/\.pdf$/i.test(path) && kind !== "text") return "pdf";
  if (kind === "document" || opaque.test(path)) return "other";
  return markdown.test(path) ? "markdown" : "text";
}

/** The start of a text file, as much as a tile has room to show. */
export function sheetText(text: string): string {
  return text.slice(0, 1600);
}

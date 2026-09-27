// What the panel can show besides its fixed tabs: the plan draft, and files in
// the project, written or opened. They are read from the transcript, so
// nothing about them is stored.

import type { ProjectFile, TranscriptItem } from "./api";
import { inProject, previewOf } from "./files";
import { planMarkdown, type Plan } from "./plan";

export interface Artifact {
  id: string;
  kind: "svg" | "page" | "file" | "plan" | "image" | "markdown";
  /** The name its source is coloured by. */
  name: string;
  source: string;
  complete: boolean;
  title: string;
  /** Set on a file in the project. Its source is empty until it has been
      read. */
  path?: string;
  /** The source is a write_file call's own text, because the call is still
      arriving or the file has not been read since. It may not be on disk yet. */
  fromWrite?: boolean;
}

export function fileArtifactId(path: string): string {
  return `file:${path}`;
}

/** A write_file call to a file the panel can draw, as it stands: the text so
    far, and whether all of it has arrived. */
export interface Write {
  content: string;
  writing: boolean;
}

/** The files the session wrote that the panel can draw, in the order they
    were first written, with the last write to each. A write still arriving is
    included, which is what draws a page or figure as it is written. */
export function writtenPreviews(items: TranscriptItem[], root: string): Map<string, Write> {
  const out = new Map<string, Write>();
  for (const item of items) {
    if (item.kind !== "tool" || item.name !== "write_file") continue;
    if (typeof item.args.path !== "string" || typeof item.args.content !== "string") continue;
    // A write that failed carries the error as its output.
    if (!item.running && !/^(wrote|replaced) /.test(item.output)) continue;
    const path = inProject(item.args.path, root);
    if (previewOf(path) === "source") continue;
    out.set(path, { content: item.args.content, writing: item.writing === true });
  }
  return out;
}

/** The panel's entries for files in the project. A write still arriving is
    drawn from its arguments; otherwise the file as last read wins, since it
    may have been edited since it was written. */
export function projectArtifacts(
  paths: string[],
  files: Record<string, ProjectFile | undefined>,
  writes: Map<string, Write>,
): Artifact[] {
  return paths.map((path) => {
    const write = writes.get(path);
    const file = files[path];
    const fromWrite = write !== undefined && (write.writing || !file);
    const source = fromWrite ? write.content : (file?.content ?? "");
    const preview = previewOf(path);
    const kind: Artifact["kind"] =
      preview === "svg"
        ? "svg"
        : preview === "page"
          ? "page"
          : preview === "image"
            ? "image"
            : preview === "markdown"
              ? "markdown"
              : "file";
    return {
      id: fileArtifactId(path),
      kind,
      name: path,
      source,
      complete: !write?.writing,
      title: path.split("/").pop() ?? path,
      path,
      fromWrite,
    };
  });
}

/** The id of the plan's place in the panel. There is one draft per session. */
export const PLAN_ID = "plan:draft";

/** The plan draft's place in the panel, once it has a section. */
export function planArtifacts(draft: Plan | null): Artifact[] {
  if (!draft) return [];
  const source = planMarkdown(draft);
  if (!draft.title && source.trim() === "") return [];
  return [
    {
      id: PLAN_ID,
      kind: "plan",
      name: "plan.md",
      source,
      complete: true,
      title: draft.title || "Plan",
    },
  ];
}

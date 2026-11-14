import type { DiffHunk, GitHunk } from "./api";

/** A hunk's header as `diff -u` writes it. A hunk that carries git's own
    counts uses them; otherwise the ranges count the lines carried, so a hunk
    the server capped still has a header that matches. */
export function hunkHeader(hunk: DiffHunk | GitHunk): string {
  let oldCount = 0;
  let newCount = 0;
  if ("old_lines" in hunk) {
    oldCount = hunk.old_lines;
    newCount = hunk.new_lines;
  } else {
    for (const line of hunk.lines) {
      if (line.kind !== "added") oldCount++;
      if (line.kind !== "removed") newCount++;
    }
  }
  const oldStart = oldCount === 0 ? 0 : hunk.old_start;
  const newStart = newCount === 0 ? 0 : hunk.new_start;
  const head = `@@ -${oldStart},${oldCount} +${newStart},${newCount} @@`;
  return hunk.section ? `${head} ${hunk.section}` : head;
}

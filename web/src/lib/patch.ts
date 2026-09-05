import type { FileChange } from "./api";

const sign = { added: "+", removed: "-", context: " " };

// One file as a unified diff, the way `diff -u` writes it. The ranges count
// the lines carried in the hunks, so a file the server capped still copies as
// a patch whose headers match its body.
function filePatch(file: FileChange): string {
  const old = file.status === "new" ? "/dev/null" : `a/${file.path}`;
  const out = [`--- ${old}`, `+++ b/${file.path}`];
  for (const hunk of file.hunks) {
    let oldCount = 0;
    let newCount = 0;
    for (const line of hunk.lines) {
      if (line.kind !== "added") oldCount++;
      if (line.kind !== "removed") newCount++;
    }
    const oldStart = oldCount === 0 ? 0 : hunk.old_start;
    const newStart = newCount === 0 ? 0 : hunk.new_start;
    out.push(`@@ -${oldStart},${oldCount} +${newStart},${newCount} @@`);
    for (const line of hunk.lines) out.push(sign[line.kind] + line.text);
  }
  return out.join("\n");
}

/** Every file that has a diff, as one patch. Files with nothing to compare
    carry no content, so they are left out. */
export function unifiedPatch(files: FileChange[]): string {
  const parts = files.filter((f) => f.hunks.length > 0).map(filePatch);
  return parts.length === 0 ? "" : parts.join("\n") + "\n";
}

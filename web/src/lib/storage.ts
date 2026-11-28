import type { TrashFailure } from "./api/types";

/** What Settings says about a folder or file Storage names, line by line. */
export interface Notice {
  status: string;
  /** Labels the button that copies the notice's path. */
  copy: string;
  /** Labels the trash path, when the notice has one. */
  trash?: string;
  /** What the person can do about a kept folder. */
  advice?: string;
  /** Labels the damaged record, when the notice names one. */
  record?: string;
}

function namesFile(failure: TrashFailure): boolean {
  return failure.file === true || failure.path === failure.record;
}

export function notice(failure: TrashFailure): Notice {
  let status = failure.kept ? "Kept" : "Move failed";
  // A damaged record alone does not say whether the folder moved.
  if (failure.waiting) status = failure.trash ? "Move awaiting verification" : failure.record ? "Not checked yet" : "Not moved yet";
  if (failure.foreign) status = "Another entry went to the trash in its place";
  const out: Notice = {
    status: `${status}: ${failure.reason}.`,
    copy: namesFile(failure) ? "Copy file path" : "Copy folder path",
  };
  if (failure.trash) out.trash = failure.foreign ? "That entry is now in the trash at:" : "Trash path to check:";
  if (failure.kept && !failure.record) {
    out.advice = "Delete it yourself when you no longer need it.";
  }
  if (failure.record && failure.record !== failure.path) out.record = "Record to inspect or repair:";
  return out;
}

/** The heading over a list of notices. */
export function heading(failures: TrashFailure[]): string {
  return failures.some(namesFile) ? "Folders and files to check" : "Folders to check";
}

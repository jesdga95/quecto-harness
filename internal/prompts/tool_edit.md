Replace exact runs of text in one file.
edits is a list of replacements applied in order. Each old_string must match byte-for-byte, indentation included, and appear exactly once; its new_string replaces it, and an empty new_string deletes it.

Make every change a file needs in one call.
Include enough surrounding lines in an old_string to make it unique.
Set replace_all on a replacement to change every occurrence at once, such as a rename within the file.
replace_all is the JSON literal true or false, lowercase and unquoted.
Every replacement that matches is applied. The result lists any that did not, by number, with the reason and the lines a repeated old_string appears on.

Prefer over write for a partial change.

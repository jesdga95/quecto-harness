Replace one exact run of text in a file.
old_string must match byte-for-byte, indentation included, and appear exactly once; new_string replaces it, an empty new_string deletes it.

Include enough surrounding lines in old_string to make it unique.
Set replace_all to change every occurrence at once, such as a rename within the file.
The call fails without writing if old_string is missing, or appears more than once without replace_all; the error names the lines it appears on.

Prefer over write for a partial change.

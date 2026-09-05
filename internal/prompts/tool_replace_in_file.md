Replace one exact run of text in a file. old_string must appear once and must match
the file byte for byte, indentation included. new_string replaces it, and an empty
new_string deletes it.

Give enough surrounding lines in old_string to make it the only match. The call fails
without writing when old_string is missing or appears more than once.

Prefer this to write_file for a change to part of a file.

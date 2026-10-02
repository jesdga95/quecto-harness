Find files by name and lines by content.

text: searched inside files, as plain text or a regular expression. A line matches if it contains the text as written or matches the expression.
name: matches the file name, e.g. *.go.
Give either or both to search only matching names; matching is case-insensitive.

path: limits the search to one folder or file; without it the search spans the project. Folders of installed and generated code are skipped and named, unless path is one of them or lies inside one.

When matches fall in one or two files, the result also lists same-folder files of the same name with a different extension.

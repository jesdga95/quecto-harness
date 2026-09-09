Find files by name and lines by content.

text: searched inside files, literal unless regex is set.
name: matches the file name, e.g. *.go.
Give either or both to search only matching names; matching is case-insensitive.

path: limits the search to one folder or file; without it the search spans the project, skipping (and naming) folders of installed and generated code.

When matches fall in one or two files, the result also lists same-folder files of the same name with a different extension.

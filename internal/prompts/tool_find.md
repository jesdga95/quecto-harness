Find files by name and lines by their contents.

text is searched for inside files, literally unless regex is set. name matches the
file name, such as *.go. Give either, or both to search only the files whose name
matches. Matching ignores case.

path limits the search to one folder or one file. Without it the search covers the
project and passes over folders of installed and generated code, naming the ones it
passed over.

When the matches fall in one or two files, the result also lists files in the same
folder with the same name and a different extension.

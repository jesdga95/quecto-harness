Run a shell command in the project root; returns output and exit code.
Each call is a fresh shell; nothing persists between calls, and stdin is empty.
Long output keeps its head and tail. When it is cut, the whole output is saved to a file the result names, which read can open.

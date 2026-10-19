Run a shell command in the project root; returns output and exit code.
Each call is a fresh shell; nothing persists between calls, and stdin is empty.
Long output keeps its head and tail. When it is cut, the whole output is saved to a file the result names, which read can open.
A command still running after a while moves to the background on its own: the call returns with the output so far, and the exit code and last lines arrive as a message when it ends.
With background the command starts and the call returns at once, with no deadline. Its tail reaches the Processes panel, processes reports it later, and when it ends its exit code and last lines arrive as a message.
background is the JSON literal true or false, lowercase and unquoted.

Delegate a task to another agent.
It starts at once and works while you carry on, so this returns its handle, not its result.

agent: the agent to run.
task: the full brief, since it sees none of this conversation; state what to do, where to look, and what to answer with.

It messages you when done; send_message reaches it before then.
Use when the work is separable or better suited to another agent.

Delegate a task to an agent and wait for its result.

agent: the agent to run.
task: the full brief, since it sees none of this conversation; state what to do, where to look, and what to answer with.
wait: false to start it and carry on without its result, which then arrives as a message; send_message reaches it before then.

Calls to subagent in one reply run at the same time, so start independent tasks together.
Use when the work is separable or better suited to another agent.

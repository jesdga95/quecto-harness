{{.Why}}, so it was moved to the background as {{.ID}} (pid {{.PID}}). Do not sleep or poll to wait for it: its exit code and last lines arrive as a message when it ends, mid-turn or after the turn. Go on with other work, or end the turn if nothing else needs doing.{{if .Output}}
Output so far:
{{.Output}}{{end}}
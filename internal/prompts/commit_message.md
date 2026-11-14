Write a commit message for the changes in the diff below. Do not change any files and do not call a tool.

The first line is a summary of 72 characters or fewer, in the imperative mood and sentence case, with no full stop. When more needs saying, leave a blank line and add a short body of plain sentences on what changed and why, wrapped at 72 characters.
{{- if .Recent}}

Recent subjects in this repository, to match in style:
{{range .Recent}}- {{.}}
{{end}}
{{- end}}
{{- if .Cut}}

The diff was cut short. Describe what it shows.
{{- end}}

Reply with only the commit message.

<diff>
{{.Diff}}
</diff>

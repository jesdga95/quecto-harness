Plan: {{.Title}}
{{if .Goal}}Goal: {{.Goal}}
{{end}}{{if .Files}}Files:
{{range .Files}}- {{.}}
{{end}}{{end}}Steps:
{{range .Steps}}- {{.}}
{{end}}{{if .Check}}Check: {{.Check}}
{{end}}{{if .Open}}Open:
{{range .Open}}- {{.}}
{{end}}{{end}}

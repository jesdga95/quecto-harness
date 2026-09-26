Plan: {{.Title}}
Goal: {{.Goal}}
Code:
{{range .Code}}- {{.}}
{{end}}Tests:
{{range .Tests}}- {{.}}
{{end}}Validation:
{{range .Validation}}- {{.}}
{{end}}Migration: {{.Migration}}
Risks:
{{range .Risks}}- {{.}}
{{end}}Steps:
{{range .Steps}}- {{.}}
{{end}}{{if .Open}}Open:
{{range .Open}}- {{.}}
{{end}}{{end}}

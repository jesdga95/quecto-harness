package tools

import (
	"bytes"
	"encoding/json"
)

// A Param is one argument a tool takes, as the settings pane's debug view
// lists it.
type Param struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Minimum     *int   `json:"minimum,omitempty"`
}

// A ToolDebug is one tool with its parameters spelled out, as the settings
// pane's debug view draws it.
type ToolDebug struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Params      []Param `json:"params"`
}

// DebugTools returns the tools offer sends, each with its parameters parsed
// out of the schema, in the order the model sees them.
func DebugTools(o Offer) []ToolDebug {
	sents := Schemas(o)
	out := make([]ToolDebug, 0, len(sents))
	for _, t := range sents {
		out = append(out, ToolDebug{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Params:      parseParams(t.Function.Parameters),
		})
	}
	return out
}

// parseParams reads a tool's parameter schema and lists each argument in the
// order the schema writes it.
func parseParams(raw json.RawMessage) []Param {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var out []Param
	var required []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := keyTok.(string)
		switch key {
		case "properties":
			if _, err := dec.Token(); err != nil {
				return out
			}
			for dec.More() {
				nameTok, err := dec.Token()
				if err != nil {
					return out
				}
				name, ok := nameTok.(string)
				if !ok {
					return out
				}
				p := Param{Name: name}
				if err := dec.Decode(&p); err != nil {
					return out
				}
				out = append(out, p)
			}
			if _, err := dec.Token(); err != nil {
				return out
			}
		case "required":
			required = nil
			if err := dec.Decode(&required); err != nil {
				return out
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return out
			}
		}
	}
	req := make(map[string]bool, len(required))
	for _, r := range required {
		req[r] = true
	}
	for i := range out {
		out[i].Required = req[out[i].Name]
	}
	return out
}

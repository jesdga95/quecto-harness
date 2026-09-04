package llama

import "context"

type Mode string

const (
	ModeUnknown Mode = "unknown"
	ModeRouter  Mode = "router"
	ModeSingle  Mode = "single"
)

// Info is what one GET /props tells us about the server as a whole. Autoload
// is router-only; Model and NCtx are single-mode only.
type Info struct {
	Mode     Mode
	Build    string
	Autoload bool
	Model    string
	NCtx     int
}

type propsBody struct {
	Role                      *string         `json:"role"`
	ModelsAutoload            *bool           `json:"models_autoload"`
	ModelAlias                *string         `json:"model_alias"`
	BuildInfo                 *string         `json:"build_info"`
	TotalSlots                *int            `json:"total_slots"`
	IsSleeping                *bool           `json:"is_sleeping"`
	ChatTemplateCaps          map[string]bool `json:"chat_template_caps"`
	DefaultGenerationSettings *struct {
		NCtx *int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// Detect reads GET /props with no query parameters, so it can never load a
// model. Anything but role "router" is a single-model server.
func (c *Client) Detect(ctx context.Context) (Info, error) {
	var body propsBody
	if err := c.getJSON(ctx, "detect", "", c.BaseURL+"/props", &body); err != nil {
		return Info{Mode: ModeUnknown}, err
	}
	info := Info{Mode: ModeSingle, Build: deref(body.BuildInfo)}
	if deref(body.Role) == "router" {
		info.Mode = ModeRouter
		info.Autoload = deref(body.ModelsAutoload)
		return info, nil
	}
	info.Model = deref(body.ModelAlias)
	if body.DefaultGenerationSettings != nil {
		info.NCtx = deref(body.DefaultGenerationSettings.NCtx)
	}
	return info, nil
}

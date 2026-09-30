package homeassistant

import "context"

// GetTranslations returns the frontend strings of one category, such as
// "services", "entity" or "state", flattened to keys like
// "component.light.services.turn_on.fields.brightness_pct.description".
// Integrations limits the result to those integrations; none loads all of
// them. HA fills strings missing in language from English.
func (c *Client) GetTranslations(ctx context.Context, language, category string, integrations ...string) (map[string]string, error) {
	if language == "" || category == "" {
		return nil, invalidArg("language and category are required")
	}
	p := map[string]any{"language": language, "category": category}
	if len(integrations) > 0 {
		p["integration"] = integrations
	}
	var out struct {
		Resources map[string]string `json:"resources"`
	}
	if err := c.wsCall(ctx, "frontend/get_translations", p, &out); err != nil {
		return nil, err
	}
	return out.Resources, nil
}

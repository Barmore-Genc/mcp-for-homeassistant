package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// ConfigKind is a UI-editable configuration type stored in YAML files.
type ConfigKind string

const (
	KindAutomation ConfigKind = "automation"
	KindScript     ConfigKind = "script"
	KindScene      ConfigKind = "scene"
)

func (k ConfigKind) validateID(id string) error {
	switch k {
	case KindAutomation, KindScene:
		return validateConfigID(string(k), id)
	case KindScript:
		// Scripts are keyed by their object id, which HA validates as a slug.
		return validateSlug("script id", id)
	default:
		return invalidArg("unknown config kind %q", k)
	}
}

// GetConfigItem returns the stored configuration of an automation, script or
// scene (admin only). Only items defined in automations.yaml, scripts.yaml
// and scenes.yaml (the UI-managed files) are available.
func (c *Client) GetConfigItem(ctx context.Context, kind ConfigKind, id string) (map[string]any, error) {
	raw, err := c.GetConfigItemRaw(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetConfigItemRaw is GetConfigItem without decoding, which keeps the key
// order HA stored the item with.
func (c *Client) GetConfigItemRaw(ctx context.Context, kind ConfigKind, id string) (json.RawMessage, error) {
	if err := kind.validateID(id); err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := c.doJSON(ctx, restRequest{
		method: http.MethodGet,
		path:   []string{"api", "config", string(kind), "config", id},
	}, &out); err != nil {
		return nil, err
	}
	if !isJSONObject(out) {
		return nil, &Error{Op: "GET /api/config/" + string(kind) + "/config/" + id, Message: "response is not a JSON object"}
	}
	return out, nil
}

// SaveConfigItem creates or fully replaces an automation, script or scene
// (admin only). HA validates the config, writes the YAML file and reloads the
// item. Any "id" field in config is replaced by id.
func (c *Client) SaveConfigItem(ctx context.Context, kind ConfigKind, id string, config map[string]any) error {
	if config == nil {
		return invalidArg("config is required")
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return invalidArg("config cannot be encoded as JSON: %v", err)
	}
	return c.SaveConfigItemRaw(ctx, kind, id, raw)
}

// SaveConfigItemRaw is SaveConfigItem for a JSON object. HA writes the YAML
// file with the keys in the order they are sent, below the few it puts first.
func (c *Client) SaveConfigItemRaw(ctx context.Context, kind ConfigKind, id string, config json.RawMessage) error {
	if err := kind.validateID(id); err != nil {
		return err
	}
	if !isJSONObject(config) {
		return invalidArg("config must be a JSON object")
	}
	// HA copies an "id" from the body over the URL key for id-based items,
	// which would store the item under a different id than requested.
	pin := id
	if kind == KindScript {
		pin = ""
	}
	body, err := withTopLevelID(config, pin)
	if err != nil {
		return invalidArg("config is not a valid JSON object: %v", err)
	}
	return c.doJSON(ctx, restRequest{
		method: http.MethodPost,
		path:   []string{"api", "config", string(kind), "config", id},
		body:   body,
	}, nil)
}

// withTopLevelID drops every top-level "id" key from obj and, if id is not
// empty, puts "id": id first. The other keys keep their order.
func withTopLevelID(obj json.RawMessage, id string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteByte('{')
	if id != "" {
		k, _ := json.Marshal(id)
		b.WriteString(`"id":`)
		b.Write(k)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		if key == "id" {
			continue
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// DeleteConfigItem deletes an automation, script or scene (admin only).
func (c *Client) DeleteConfigItem(ctx context.Context, kind ConfigKind, id string) error {
	if err := kind.validateID(id); err != nil {
		return err
	}
	return c.doJSON(ctx, restRequest{
		method: http.MethodDelete,
		path:   []string{"api", "config", string(kind), "config", id},
	}, nil)
}

// ConfigItemSummary is an automation, script or scene found through its state.
type ConfigItemSummary struct {
	EntityID string `json:"entity_id"`
	// ConfigID is the id used by the config endpoints and traces. It is empty
	// for items defined outside the UI-managed YAML files.
	ConfigID string         `json:"config_id,omitempty"`
	Name     string         `json:"name,omitempty"`
	State    string         `json:"state"`
	Attrs    map[string]any `json:"attributes,omitempty"`
}

// ListConfigItems lists automations, scripts or scenes from the state
// machine. Automations and scenes carry their config id in the "id"
// attribute; scripts use the entity registry unique id, which is the script key.
func (c *Client) ListConfigItems(ctx context.Context, kind ConfigKind) ([]ConfigItemSummary, error) {
	if kind != KindAutomation && kind != KindScript && kind != KindScene {
		return nil, invalidArg("unknown config kind %q", kind)
	}
	states, err := c.ListStates(ctx)
	if err != nil {
		return nil, err
	}
	var scriptKeys map[string]string
	if kind == KindScript {
		entries, err := c.ListEntityRegistry(ctx)
		if err != nil {
			return nil, err
		}
		scriptKeys = map[string]string{}
		for _, e := range entries {
			if e.Platform == "script" {
				scriptKeys[e.EntityID] = e.UniqueID
			}
		}
	}
	prefix := string(kind) + "."
	out := []ConfigItemSummary{}
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, prefix) {
			continue
		}
		item := ConfigItemSummary{EntityID: s.EntityID, State: s.State, Attrs: s.Attributes}
		if name, ok := s.Attributes["friendly_name"].(string); ok {
			item.Name = name
		}
		switch kind {
		case KindScript:
			item.ConfigID = scriptKeys[s.EntityID]
		default:
			if id, ok := s.Attributes["id"].(string); ok {
				item.ConfigID = id
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// ValidationResult is the per-section result of ValidateConfig.
type ValidationResult struct {
	Valid bool    `json:"valid"`
	Error *string `json:"error"`
}

// ValidateConfigRequest holds the sections to validate. Each is the raw
// triggers, conditions or actions value (a list or a single item).
type ValidateConfigRequest struct {
	Triggers   any `json:"triggers,omitempty"`
	Conditions any `json:"conditions,omitempty"`
	Actions    any `json:"actions,omitempty"`
}

// ValidateConfigResponse has one entry per section that was sent.
type ValidateConfigResponse struct {
	Triggers   *ValidationResult `json:"triggers,omitempty"`
	Conditions *ValidationResult `json:"conditions,omitempty"`
	Actions    *ValidationResult `json:"actions,omitempty"`
}

// ValidateConfig validates triggers, conditions and actions without saving them.
func (c *Client) ValidateConfig(ctx context.Context, req ValidateConfigRequest) (*ValidateConfigResponse, error) {
	if req.Triggers == nil && req.Conditions == nil && req.Actions == nil {
		return nil, invalidArg("at least one of triggers, conditions or actions is required")
	}
	var out ValidateConfigResponse
	if err := c.wsCall(ctx, "validate_config", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeviceAutomationType selects device triggers, conditions or actions.
type DeviceAutomationType string

const (
	DeviceTrigger   DeviceAutomationType = "trigger"
	DeviceCondition DeviceAutomationType = "condition"
	DeviceAction    DeviceAutomationType = "action"
)

// ListDeviceAutomations returns the device triggers, conditions or actions
// available for a device. Each item can be used as-is in an automation.
func (c *Client) ListDeviceAutomations(ctx context.Context, typ DeviceAutomationType, deviceID string) ([]map[string]any, error) {
	if typ != DeviceTrigger && typ != DeviceCondition && typ != DeviceAction {
		return nil, invalidArg("unknown device automation type %q", typ)
	}
	if err := validateRegistryID("device", deviceID); err != nil {
		return nil, err
	}
	var out []map[string]any
	err := c.wsCall(ctx, "device_automation/"+string(typ)+"/list", map[string]any{"device_id": deviceID}, &out)
	return out, err
}

// DeviceAutomationCapabilities returns the extra fields (as a voluptuous-serialized
// schema under "extra_fields") that a device trigger, condition or action accepts.
func (c *Client) DeviceAutomationCapabilities(ctx context.Context, typ DeviceAutomationType, item map[string]any) (json.RawMessage, error) {
	if typ != DeviceTrigger && typ != DeviceCondition && typ != DeviceAction {
		return nil, invalidArg("unknown device automation type %q", typ)
	}
	if item == nil {
		return nil, invalidArg("item is required")
	}
	var out json.RawMessage
	err := c.wsCall(ctx, "device_automation/"+string(typ)+"/capabilities", map[string]any{string(typ): item}, &out)
	return out, err
}

// SearchItemType is an item type understood by search/related.
type SearchItemType string

const (
	SearchArea                SearchItemType = "area"
	SearchAutomation          SearchItemType = "automation"
	SearchAutomationBlueprint SearchItemType = "automation_blueprint"
	SearchConfigEntry         SearchItemType = "config_entry"
	SearchDevice              SearchItemType = "device"
	SearchEntity              SearchItemType = "entity"
	SearchFloor               SearchItemType = "floor"
	SearchGroup               SearchItemType = "group"
	SearchIntegration         SearchItemType = "integration"
	SearchLabel               SearchItemType = "label"
	SearchPerson              SearchItemType = "person"
	SearchScene               SearchItemType = "scene"
	SearchScript              SearchItemType = "script"
	SearchScriptBlueprint     SearchItemType = "script_blueprint"
)

// SearchRelated returns items related to the given item, keyed by item type.
// Automations, scripts and scenes are referenced by entity id.
func (c *Client) SearchRelated(ctx context.Context, itemType SearchItemType, itemID string) (map[SearchItemType][]string, error) {
	if itemType == "" || itemID == "" {
		return nil, invalidArg("item type and id are required")
	}
	var out map[SearchItemType][]string
	err := c.wsCall(ctx, "search/related", map[string]any{"item_type": itemType, "item_id": itemID}, &out)
	return out, err
}

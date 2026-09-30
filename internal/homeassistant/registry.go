package homeassistant

import (
	"context"
	"encoding/json"
	"time"
)

// UnixTime decodes HA's epoch-second float timestamps and encodes as RFC 3339.
type UnixTime struct{ time.Time }

func (t *UnixTime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*t = UnixTime{}
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	t.Time = unixFloat(f)
	return nil
}

// Area is an area registry entry.
type Area struct {
	AreaID              string   `json:"area_id"`
	Name                string   `json:"name"`
	Aliases             []string `json:"aliases"`
	FloorID             *string  `json:"floor_id"`
	HumidityEntityID    *string  `json:"humidity_entity_id"`
	TemperatureEntityID *string  `json:"temperature_entity_id"`
	Icon                *string  `json:"icon"`
	Labels              []string `json:"labels"`
	Picture             *string  `json:"picture"`
	CreatedAt           UnixTime `json:"created_at"`
	ModifiedAt          UnixTime `json:"modified_at"`
}

// AreaCreate creates an area.
type AreaCreate struct {
	Name                string   `json:"name"`
	Aliases             []string `json:"aliases,omitempty"`
	FloorID             string   `json:"floor_id,omitempty"`
	HumidityEntityID    string   `json:"humidity_entity_id,omitempty"`
	TemperatureEntityID string   `json:"temperature_entity_id,omitempty"`
	Icon                string   `json:"icon,omitempty"`
	Labels              []string `json:"labels,omitempty"`
	Picture             string   `json:"picture,omitempty"`
}

// AreaUpdate changes only the fields that are set. A non-nil empty slice
// clears Aliases or Labels.
type AreaUpdate struct {
	Name                string           `json:"name,omitzero"`
	Aliases             []string         `json:"aliases,omitzero"`
	FloorID             Nullable[string] `json:"floor_id,omitzero"`
	HumidityEntityID    Nullable[string] `json:"humidity_entity_id,omitzero"`
	TemperatureEntityID Nullable[string] `json:"temperature_entity_id,omitzero"`
	Icon                Nullable[string] `json:"icon,omitzero"`
	Labels              []string         `json:"labels,omitzero"`
	Picture             Nullable[string] `json:"picture,omitzero"`
}

func (c *Client) ListAreas(ctx context.Context) ([]Area, error) {
	var out []Area
	err := c.wsCall(ctx, "config/area_registry/list", nil, &out)
	return out, err
}

func (c *Client) CreateArea(ctx context.Context, a AreaCreate) (*Area, error) {
	if a.Name == "" {
		return nil, invalidArg("name is required")
	}
	var out Area
	if err := c.wsCall(ctx, "config/area_registry/create", a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateArea(ctx context.Context, areaID string, u AreaUpdate) (*Area, error) {
	if err := validateRegistryID("area", areaID); err != nil {
		return nil, err
	}
	payload, err := withField(u, "area_id", areaID)
	if err != nil {
		return nil, err
	}
	var out Area
	if err := c.wsCall(ctx, "config/area_registry/update", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteArea(ctx context.Context, areaID string) error {
	if err := validateRegistryID("area", areaID); err != nil {
		return err
	}
	return c.wsCall(ctx, "config/area_registry/delete", map[string]any{"area_id": areaID}, nil)
}

// Floor is a floor registry entry.
type Floor struct {
	FloorID    string   `json:"floor_id"`
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases"`
	Icon       *string  `json:"icon"`
	Level      *int     `json:"level"`
	CreatedAt  UnixTime `json:"created_at"`
	ModifiedAt UnixTime `json:"modified_at"`
}

type FloorCreate struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Icon    string   `json:"icon,omitempty"`
	Level   *int     `json:"level,omitempty"`
}

type FloorUpdate struct {
	Name    string           `json:"name,omitzero"`
	Aliases []string         `json:"aliases,omitzero"`
	Icon    Nullable[string] `json:"icon,omitzero"`
	Level   Nullable[int]    `json:"level,omitzero"`
}

func (c *Client) ListFloors(ctx context.Context) ([]Floor, error) {
	var out []Floor
	err := c.wsCall(ctx, "config/floor_registry/list", nil, &out)
	return out, err
}

func (c *Client) CreateFloor(ctx context.Context, f FloorCreate) (*Floor, error) {
	if f.Name == "" {
		return nil, invalidArg("name is required")
	}
	var out Floor
	if err := c.wsCall(ctx, "config/floor_registry/create", f, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateFloor(ctx context.Context, floorID string, u FloorUpdate) (*Floor, error) {
	if err := validateRegistryID("floor", floorID); err != nil {
		return nil, err
	}
	payload, err := withField(u, "floor_id", floorID)
	if err != nil {
		return nil, err
	}
	var out Floor
	if err := c.wsCall(ctx, "config/floor_registry/update", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteFloor(ctx context.Context, floorID string) error {
	if err := validateRegistryID("floor", floorID); err != nil {
		return err
	}
	return c.wsCall(ctx, "config/floor_registry/delete", map[string]any{"floor_id": floorID}, nil)
}

// Label is a label registry entry.
type Label struct {
	LabelID     string   `json:"label_id"`
	Name        string   `json:"name"`
	Color       *string  `json:"color"`
	Description *string  `json:"description"`
	Icon        *string  `json:"icon"`
	CreatedAt   UnixTime `json:"created_at"`
	ModifiedAt  UnixTime `json:"modified_at"`
}

type LabelCreate struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

type LabelUpdate struct {
	Name        string           `json:"name,omitzero"`
	Color       Nullable[string] `json:"color,omitzero"`
	Description Nullable[string] `json:"description,omitzero"`
	Icon        Nullable[string] `json:"icon,omitzero"`
}

func (c *Client) ListLabels(ctx context.Context) ([]Label, error) {
	var out []Label
	err := c.wsCall(ctx, "config/label_registry/list", nil, &out)
	return out, err
}

func (c *Client) CreateLabel(ctx context.Context, l LabelCreate) (*Label, error) {
	if l.Name == "" {
		return nil, invalidArg("name is required")
	}
	var out Label
	if err := c.wsCall(ctx, "config/label_registry/create", l, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateLabel(ctx context.Context, labelID string, u LabelUpdate) (*Label, error) {
	if err := validateRegistryID("label", labelID); err != nil {
		return nil, err
	}
	payload, err := withField(u, "label_id", labelID)
	if err != nil {
		return nil, err
	}
	var out Label
	if err := c.wsCall(ctx, "config/label_registry/update", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteLabel(ctx context.Context, labelID string) error {
	if err := validateRegistryID("label", labelID); err != nil {
		return err
	}
	return c.wsCall(ctx, "config/label_registry/delete", map[string]any{"label_id": labelID}, nil)
}

// Category is a category registry entry. Categories are scoped, for example
// to "automation", "script", "scene" or "helpers".
type Category struct {
	CategoryID string   `json:"category_id"`
	Name       string   `json:"name"`
	Icon       *string  `json:"icon"`
	CreatedAt  UnixTime `json:"created_at"`
	ModifiedAt UnixTime `json:"modified_at"`
}

type CategoryCreate struct {
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

type CategoryUpdate struct {
	Name string           `json:"name,omitzero"`
	Icon Nullable[string] `json:"icon,omitzero"`
}

func (c *Client) ListCategories(ctx context.Context, scope string) ([]Category, error) {
	if err := validateSlug("category scope", scope); err != nil {
		return nil, err
	}
	var out []Category
	err := c.wsCall(ctx, "config/category_registry/list", map[string]any{"scope": scope}, &out)
	return out, err
}

func (c *Client) CreateCategory(ctx context.Context, scope string, cat CategoryCreate) (*Category, error) {
	if err := validateSlug("category scope", scope); err != nil {
		return nil, err
	}
	if cat.Name == "" {
		return nil, invalidArg("name is required")
	}
	payload, err := withField(cat, "scope", scope)
	if err != nil {
		return nil, err
	}
	var out Category
	if err := c.wsCall(ctx, "config/category_registry/create", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateCategory(ctx context.Context, scope, categoryID string, u CategoryUpdate) (*Category, error) {
	if err := validateSlug("category scope", scope); err != nil {
		return nil, err
	}
	if err := validateRegistryID("category", categoryID); err != nil {
		return nil, err
	}
	payload, err := withField(u, "scope", scope)
	if err != nil {
		return nil, err
	}
	payload["category_id"] = categoryID
	var out Category
	if err := c.wsCall(ctx, "config/category_registry/update", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteCategory(ctx context.Context, scope, categoryID string) error {
	if err := validateSlug("category scope", scope); err != nil {
		return err
	}
	if err := validateRegistryID("category", categoryID); err != nil {
		return err
	}
	return c.wsCall(ctx, "config/category_registry/delete", map[string]any{"scope": scope, "category_id": categoryID}, nil)
}

// Device is a device registry entry.
type Device struct {
	ID                 string     `json:"id"`
	Name               *string    `json:"name"`
	NameByUser         *string    `json:"name_by_user"`
	AreaID             *string    `json:"area_id"`
	Manufacturer       *string    `json:"manufacturer"`
	Model              *string    `json:"model"`
	ModelID            *string    `json:"model_id"`
	HWVersion          *string    `json:"hw_version"`
	SWVersion          *string    `json:"sw_version"`
	SerialNumber       *string    `json:"serial_number"`
	ConfigEntries      []string   `json:"config_entries"`
	ConfigEntryID      *string    `json:"config_entry_id"`
	PrimaryConfigEntry *string    `json:"primary_config_entry"`
	ConfigurationURL   *string    `json:"configuration_url"`
	Connections        [][]string `json:"connections"`
	Identifiers        [][]string `json:"identifiers"`
	DisabledBy         *string    `json:"disabled_by"`
	EntryType          *string    `json:"entry_type"`
	Labels             []string   `json:"labels"`
	ViaDeviceID        *string    `json:"via_device_id"`
	ParentDeviceID     *string    `json:"parent_device_id"`
	CreatedAt          UnixTime   `json:"created_at"`
	ModifiedAt         UnixTime   `json:"modified_at"`
}

// DisplayName returns the user-assigned name, falling back to the integration's name.
func (d Device) DisplayName() string {
	if d.NameByUser != nil && *d.NameByUser != "" {
		return *d.NameByUser
	}
	if d.Name != nil {
		return *d.Name
	}
	return ""
}

// DeviceUpdate changes only the fields that are set. DisabledBy accepts
// "user" or Null.
type DeviceUpdate struct {
	AreaID     Nullable[string] `json:"area_id,omitzero"`
	NameByUser Nullable[string] `json:"name_by_user,omitzero"`
	DisabledBy Nullable[string] `json:"disabled_by,omitzero"`
	Labels     []string         `json:"labels,omitzero"`
}

func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var out []Device
	err := c.wsCall(ctx, "config/device_registry/list", nil, &out)
	return out, err
}

func (c *Client) UpdateDevice(ctx context.Context, deviceID string, u DeviceUpdate) (*Device, error) {
	if err := validateRegistryID("device", deviceID); err != nil {
		return nil, err
	}
	if v, ok := u.DisabledBy.Get(); ok && v != "user" {
		return nil, invalidArg(`disabled_by must be "user" or null`)
	}
	payload, err := withField(u, "device_id", deviceID)
	if err != nil {
		return nil, err
	}
	var out Device
	if err := c.wsCall(ctx, "config/device_registry/update", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EntityRegistryEntry is an entity registry entry. Aliases, Capabilities,
// DeviceClass, OriginalDeviceClass and OriginalIcon are only filled by GetEntityRegistryEntry.
type EntityRegistryEntry struct {
	ID                  string            `json:"id"`
	EntityID            string            `json:"entity_id"`
	UniqueID            string            `json:"unique_id"`
	Platform            string            `json:"platform"`
	Name                *string           `json:"name"`
	OriginalName        *string           `json:"original_name"`
	Icon                *string           `json:"icon"`
	AreaID              *string           `json:"area_id"`
	DeviceID            *string           `json:"device_id"`
	ConfigEntryID       *string           `json:"config_entry_id"`
	ConfigSubentryID    *string           `json:"config_subentry_id"`
	Categories          map[string]string `json:"categories"`
	Labels              []string          `json:"labels"`
	DisabledBy          *string           `json:"disabled_by"`
	HiddenBy            *string           `json:"hidden_by"`
	EntityCategory      *string           `json:"entity_category"`
	HasEntityName       bool              `json:"has_entity_name"`
	TranslationKey      *string           `json:"translation_key"`
	Options             map[string]any    `json:"options"`
	CreatedAt           UnixTime          `json:"created_at"`
	ModifiedAt          UnixTime          `json:"modified_at"`
	Aliases             []*string         `json:"aliases,omitempty"`
	Capabilities        map[string]any    `json:"capabilities,omitempty"`
	DeviceClass         *string           `json:"device_class,omitempty"`
	OriginalDeviceClass *string           `json:"original_device_class,omitempty"`
	OriginalIcon        *string           `json:"original_icon,omitempty"`
}

// EntityUpdate changes only the fields that are set. DisabledBy and HiddenBy
// accept "user" or Null. A nil value in Categories removes the entity from
// that scope's category. OptionsDomain and Options must be set together.
type EntityUpdate struct {
	Name          Nullable[string]   `json:"name,omitzero"`
	Icon          Nullable[string]   `json:"icon,omitzero"`
	AreaID        Nullable[string]   `json:"area_id,omitzero"`
	DeviceClass   Nullable[string]   `json:"device_class,omitzero"`
	Aliases       []string           `json:"aliases,omitzero"`
	Labels        []string           `json:"labels,omitzero"`
	Categories    map[string]*string `json:"categories,omitzero"`
	NewEntityID   string             `json:"new_entity_id,omitzero"`
	DisabledBy    Nullable[string]   `json:"disabled_by,omitzero"`
	HiddenBy      Nullable[string]   `json:"hidden_by,omitzero"`
	OptionsDomain string             `json:"options_domain,omitzero"`
	Options       map[string]any     `json:"options,omitzero"`
}

// EntityUpdateResult is returned by UpdateEntityRegistryEntry. When an entity
// was re-enabled HA either reloads its config entry after ReloadDelay seconds
// or needs a restart.
type EntityUpdateResult struct {
	Entry          EntityRegistryEntry `json:"entity_entry"`
	RequireRestart bool                `json:"require_restart,omitempty"`
	ReloadDelay    int                 `json:"reload_delay,omitempty"`
}

func (c *Client) ListEntityRegistry(ctx context.Context) ([]EntityRegistryEntry, error) {
	var out []EntityRegistryEntry
	err := c.wsCall(ctx, "config/entity_registry/list", nil, &out)
	return out, err
}

func (c *Client) GetEntityRegistryEntry(ctx context.Context, entityID string) (*EntityRegistryEntry, error) {
	if err := ValidateEntityID(entityID); err != nil {
		return nil, err
	}
	var out EntityRegistryEntry
	if err := c.wsCall(ctx, "config/entity_registry/get", map[string]any{"entity_id": entityID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateEntityRegistryEntry(ctx context.Context, entityID string, u EntityUpdate) (*EntityUpdateResult, error) {
	if err := ValidateEntityID(entityID); err != nil {
		return nil, err
	}
	if u.NewEntityID != "" {
		if err := ValidateEntityID(u.NewEntityID); err != nil {
			return nil, err
		}
	}
	for _, f := range []Nullable[string]{u.DisabledBy, u.HiddenBy} {
		if v, ok := f.Get(); ok && v != "user" {
			return nil, invalidArg(`disabled_by and hidden_by must be "user" or null`)
		}
	}
	if (u.OptionsDomain == "") != (u.Options == nil) {
		return nil, invalidArg("options_domain and options must be set together")
	}
	payload, err := withField(u, "entity_id", entityID)
	if err != nil {
		return nil, err
	}
	var out EntityUpdateResult
	if err := c.wsCall(ctx, "config/entity_registry/update", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveEntityRegistryEntry removes an entity from the registry. Integrations
// that still provide the entity will re-add it on reload.
func (c *Client) RemoveEntityRegistryEntry(ctx context.Context, entityID string) error {
	if err := ValidateEntityID(entityID); err != nil {
		return err
	}
	return c.wsCall(ctx, "config/entity_registry/remove", map[string]any{"entity_id": entityID}, nil)
}

// withField marshals v to a JSON object and adds key=value.
func withField(v any, key string, value any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	m[key] = value
	return m, nil
}

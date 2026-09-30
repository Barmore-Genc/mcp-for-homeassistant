package homeassistant

import (
	"context"
	"fmt"
	"slices"
)

// HelperDomains are the UI-managed helper types supported by the helper methods.
var HelperDomains = []string{
	"input_boolean", "input_number", "input_select", "input_text", "input_datetime",
	"input_button", "counter", "timer", "schedule", "zone",
}

func validateHelperDomain(domain string) error {
	if !slices.Contains(HelperDomains, domain) {
		return invalidArg("unsupported helper domain %q", domain)
	}
	return nil
}

// ListHelpers returns the UI-created helpers of a domain. Each item has an
// "id" plus the domain's fields (for example name, icon, min, max). Helpers
// defined in YAML are not included.
func (c *Client) ListHelpers(ctx context.Context, domain string) ([]map[string]any, error) {
	if err := validateHelperDomain(domain); err != nil {
		return nil, err
	}
	var out []map[string]any
	err := c.wsCall(ctx, domain+"/list", nil, &out)
	return out, err
}

// CreateHelper creates a helper with the domain's fields ("name" is always
// required) and returns the stored item including its "id".
func (c *Client) CreateHelper(ctx context.Context, domain string, fields map[string]any) (map[string]any, error) {
	if err := validateHelperDomain(domain); err != nil {
		return nil, err
	}
	if err := rejectReservedKeys(fields); err != nil {
		return nil, err
	}
	var out map[string]any
	err := c.wsCall(ctx, domain+"/create", fields, &out)
	return out, err
}

// UpdateHelper changes the given fields of a helper; a nil value removes the
// field. Most helper types replace their whole stored config on update, so
// the current item is read and merged with changes before sending.
func (c *Client) UpdateHelper(ctx context.Context, domain, id string, changes map[string]any) (map[string]any, error) {
	if err := validateHelperDomain(domain); err != nil {
		return nil, err
	}
	if err := validateRegistryID(domain, id); err != nil {
		return nil, err
	}
	idKey := domain + "_id"
	if err := rejectReservedKeys(changes); err != nil {
		return nil, err
	}
	if _, ok := changes[idKey]; ok {
		return nil, invalidArg("field %q is reserved", idKey)
	}

	items, err := c.ListHelpers(ctx, domain)
	if err != nil {
		return nil, err
	}
	var current map[string]any
	for _, it := range items {
		if it["id"] == id {
			current = it
			break
		}
	}
	if current == nil {
		return nil, &Error{Op: domain + "/update", Code: "not_found", Message: fmt.Sprintf("%s %s not found", domain, id)}
	}

	payload := make(map[string]any, len(current)+len(changes))
	for k, v := range current {
		if k != "id" {
			payload[k] = v
		}
	}
	for k, v := range changes {
		if v == nil {
			delete(payload, k)
		} else {
			payload[k] = v
		}
	}
	payload[idKey] = id

	var out map[string]any
	err = c.wsCall(ctx, domain+"/update", payload, &out)
	return out, err
}

// DeleteHelper deletes a UI-created helper.
func (c *Client) DeleteHelper(ctx context.Context, domain, id string) error {
	if err := validateHelperDomain(domain); err != nil {
		return err
	}
	if err := validateRegistryID(domain, id); err != nil {
		return err
	}
	return c.wsCall(ctx, domain+"/delete", map[string]any{domain + "_id": id}, nil)
}

// Person is a person entry.
type Person struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	UserID         *string  `json:"user_id"`
	DeviceTrackers []string `json:"device_trackers"`
	Picture        *string  `json:"picture"`
}

// Persons lists persons created in the UI (Storage) and in YAML (Config).
type Persons struct {
	Storage []Person `json:"storage"`
	Config  []Person `json:"config"`
}

func (c *Client) ListPersons(ctx context.Context) (*Persons, error) {
	var out Persons
	if err := c.wsCall(ctx, "person/list", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

//go:build integration

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/oauth"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests run every organize tool against a real Home Assistant that other
// test runs share, so they only create items named mcp_org_*, restore whatever
// else they touch, and clean up after themselves.

func organizeLive(t *testing.T) (*mcp.ClientSession, *homeassistant.Client) {
	t.Helper()
	url, token := os.Getenv("HA_URL"), os.Getenv("HA_TOKEN")
	if url == "" || token == "" {
		t.Skip("HA_URL and HA_TOKEN must be set")
	}
	ha, err := homeassistant.New(url, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ha.Close() })
	s := New(ha, oauth.NewSigner("secret"), "https://mcp.example", "test", false)
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := s.build().Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, ha
}

// organizeLiveWS sends one raw WebSocket command, for the setup and cleanup
// steps the client deliberately has no method for.
func organizeLiveWS(t *testing.T, typ string, payload map[string]any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wsURL := strings.Replace(os.Getenv("HA_URL"), "http", "ws", 1) + "/api/websocket"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 20)
	read := func() map[string]json.RawMessage {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		return m
	}
	write := func(v any) {
		b, _ := json.Marshal(v)
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	read()
	write(map[string]any{"type": "auth", "access_token": os.Getenv("HA_TOKEN")})
	read()
	msg := map[string]any{"id": 1, "type": typ}
	for k, v := range payload {
		msg[k] = v
	}
	write(msg)
	for {
		m := read()
		if string(m["type"]) != `"result"` {
			continue
		}
		if string(m["success"]) != "true" {
			t.Fatalf("%s failed: %s", typ, m["error"])
		}
		return m["result"]
	}
}

func organizeLiveCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	t.Logf("%s %v →\n%s", name, args, b.String())
	return b.String(), res.IsError
}

func organizeLiveOK(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, want ...string) string {
	t.Helper()
	out, isErr := organizeLiveCall(t, cs, name, args)
	if isErr {
		t.Fatalf("%s failed: %s", name, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%s output lacks %q:\n%s", name, w, out)
		}
	}
	return out
}

func organizeLiveErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, want string) string {
	t.Helper()
	out, isErr := organizeLiveCall(t, cs, name, args)
	if !isErr || !strings.Contains(out, want) {
		t.Fatalf("%s: expected an error containing %q, got:\n%s", name, want, out)
	}
	return out
}

func TestOrganizeIntegrationReadTools(t *testing.T) {
	cs, _ := organizeLive(t)
	for _, typ := range []string{"areas", "floors", "labels", "categories", "persons", "zones"} {
		organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": typ})
	}
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "devices", "integration": "demo", "limit": 15}, "device_id")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "domain": "light"}, "light.kitchen_lights")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "search": "sun", "include_disabled": true}, "disabled by integration")
	organizeLiveErr(t, cs, "ha_list_registry", map[string]any{"type": "entities", "area": "No Such Room"}, "no area")
	organizeLiveOK(t, cs, "ha_list_helpers", map[string]any{})
	organizeLiveOK(t, cs, "ha_list_integrations", map[string]any{}, "domain demo")
	organizeLiveOK(t, cs, "ha_get_dashboard", map[string]any{}, "url_path lovelace")
	organizeLiveOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": "lovelace"}, "```yaml", "views:")
	organizeLiveOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": "map"}, "strategy")
	organizeLiveOK(t, cs, "ha_backup_info", map[string]any{}, "Automatic backups")
}

func TestOrganizeIntegrationRegistry(t *testing.T) {
	cs, ha := organizeLive(t)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, a := range organizeMust(ha.ListAreas(ctx)) {
			if strings.HasPrefix(a.Name, "mcp_org_") {
				_ = ha.DeleteArea(ctx, a.AreaID)
			}
		}
		for _, f := range organizeMust(ha.ListFloors(ctx)) {
			if strings.HasPrefix(f.Name, "mcp_org_") {
				_ = ha.DeleteFloor(ctx, f.FloorID)
			}
		}
		for _, l := range organizeMust(ha.ListLabels(ctx)) {
			if strings.HasPrefix(l.Name, "mcp_org_") {
				_ = ha.DeleteLabel(ctx, l.LabelID)
			}
		}
		for _, z := range organizeMust(ha.ListHelpers(ctx, "zone")) {
			if name, _ := z["name"].(string); strings.HasPrefix(name, "mcp_org_") {
				_ = ha.DeleteHelper(ctx, "zone", z["id"].(string))
			}
		}
	})

	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "label", "action": "create", "name": "mcp_org_label", "color": "indigo"}, "label_id mcp_org_label")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "label", "action": "update", "id": "mcp_org_label", "description": "test label", "icon": "mdi:tag"}, "After:", "icon mdi:tag")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "floor", "action": "create", "name": "mcp_org_floor", "level": 1}, "level 1")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "floor", "action": "update", "id": "mcp_org_floor", "level": 2, "aliases": []string{"mcp_org_upstairs"}}, "level 2")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{
		"resource": "area", "action": "create", "name": "mcp_org_area", "floor": "mcp_org_floor", "labels": []string{"mcp_org_label"}, "icon": "mdi:flask",
	}, "floor mcp_org_floor", "labels mcp_org_label")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "area", "action": "update", "id": "mcp_org_area", "floor": ""}, "Before:")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "floors"}, "mcp_org_floor")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "labels"}, "mcp_org_label")

	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "category", "action": "create", "scope": "automation", "name": "mcp_org_cat"}, "category_id")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "category", "action": "update", "scope": "automation", "id": "mcp_org_cat", "icon": "mdi:test-tube"}, "mdi:test-tube")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "categories", "scope": "automation"}, "mcp_org_cat")
	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "category", "action": "delete", "scope": "automation", "id": "mcp_org_cat"}, "confirm")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "category", "action": "delete", "scope": "automation", "id": "mcp_org_cat", "confirm": true}, "Deleted")

	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "zone", "action": "create", "name": "mcp_org_zone", "latitude": 32.88, "longitude": 117.23, "radius": 50}, "radius 50m")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "zone", "action": "update", "id": "mcp_org_zone", "radius": 75, "passive": true}, "radius 75m", "passive")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "zones"}, "mcp_org_zone", "zone.home")
	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "zone", "action": "update", "id": "zone.home", "radius": 10}, "Settings")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "zone", "action": "delete", "id": "mcp_org_zone", "confirm": true}, "Deleted zone")

	// A demo device and entity are shared with other runs, so every field
	// changed here is put back to what it was.
	devices := organizeMust(ha.ListDevices(ctx))
	var dev homeassistant.Device
	for _, d := range devices {
		if d.DisplayName() == "Outside Humidity" {
			dev = d
		}
	}
	if dev.ID == "" {
		t.Fatal("demo device Outside Humidity not found")
	}
	t.Cleanup(func() {
		u := homeassistant.DeviceUpdate{Labels: dev.Labels, NameByUser: homeassistant.Null[string](), AreaID: homeassistant.Null[string]()}
		if dev.Labels == nil {
			u.Labels = []string{}
		}
		if dev.NameByUser != nil {
			u.NameByUser = homeassistant.Set(*dev.NameByUser)
		}
		if dev.AreaID != nil {
			u.AreaID = homeassistant.Set(*dev.AreaID)
		}
		if _, err := ha.UpdateDevice(ctx, dev.ID, u); err != nil {
			t.Errorf("restore device: %v", err)
		}
	})
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{
		"resource": "device", "action": "update", "id": dev.ID, "name": "mcp_org_device", "area": "mcp_org_area", "labels": []string{"mcp_org_label"},
	}, "mcp_org_device", "area mcp_org_area")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "devices", "area": "mcp_org_area"}, "mcp_org_device")
	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "device", "action": "delete", "id": dev.ID, "confirm": true}, "only be updated")

	const entityID = "sensor.outside_humidity"
	orig := organizeMust(ha.GetEntityRegistryEntry(ctx, entityID))
	t.Cleanup(func() {
		u := homeassistant.EntityUpdate{
			Name: homeassistant.Null[string](), Icon: homeassistant.Null[string](), AreaID: homeassistant.Null[string](),
			HiddenBy: homeassistant.Null[string](), Labels: orig.Labels,
		}
		if orig.Labels == nil {
			u.Labels = []string{}
		}
		if orig.Name != nil {
			u.Name = homeassistant.Set(*orig.Name)
		}
		if orig.Icon != nil {
			u.Icon = homeassistant.Set(*orig.Icon)
		}
		if orig.AreaID != nil {
			u.AreaID = homeassistant.Set(*orig.AreaID)
		}
		if orig.HiddenBy != nil {
			u.HiddenBy = homeassistant.Set(*orig.HiddenBy)
		}
		if _, err := ha.UpdateEntityRegistryEntry(ctx, entityID, u); err != nil {
			t.Errorf("restore entity: %v", err)
		}
	})
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "area": "mcp_org_area"}, entityID, "(from device)")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{
		"resource": "entity", "action": "update", "id": entityID, "name": "mcp_org_humidity", "icon": "mdi:water", "hidden": true, "labels": []string{"mcp_org_label"},
	}, "mcp_org_humidity (renamed)", "hidden by user", "labels mcp_org_label")
	organizeLiveOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "label": "mcp_org_label"}, entityID)
	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "entity", "action": "update", "id": "light.kitchen_lights", "new_entity_id": "light.mcp_org_kitchen"}, "automation.test_toggle_kitchen_light")
	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "entity", "action": "delete", "id": entityID}, "confirm")

	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "area", "action": "delete", "id": "mcp_org_area"}, "confirm")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "area", "action": "delete", "id": "mcp_org_area", "confirm": true}, "Deleted area")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "floor", "action": "delete", "id": "mcp_org_floor", "confirm": true}, "Deleted floor")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "label", "action": "delete", "id": "mcp_org_label", "confirm": true}, "Deleted label")
}

func TestOrganizeIntegrationHelpers(t *testing.T) {
	cs, ha := organizeLive(t)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, d := range organizeHelperDomains {
			for _, it := range organizeMust(ha.ListHelpers(ctx, d)) {
				if name, _ := it["name"].(string); strings.HasPrefix(name, "mcp_org_") {
					_ = ha.DeleteHelper(ctx, d, it["id"].(string))
				}
			}
		}
	})
	organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": "input_number", "action": "create", "fields": map[string]any{
		"name": "mcp_org_number", "min": 0, "max": 10, "step": 0.5, "mode": "box", "unit_of_measurement": "°C",
	}}, "input_number.mcp_org_number", "max 10")
	organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": "input_number", "action": "update", "id": "input_number.mcp_org_number", "fields": map[string]any{"max": 20, "unit_of_measurement": nil}}, "max 20")
	organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": "input_select", "action": "create", "fields": map[string]any{"name": "mcp_org_select", "options": []string{"a", "b"}}}, `options ["a","b"]`)
	organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": "schedule", "action": "create", "fields": map[string]any{
		"name": "mcp_org_schedule", "monday": []any{map[string]any{"from": "07:00:00", "to": "09:00:00"}},
	}}, "monday")
	// HA remembers a deleted helper's entity_id and hands it back when the same
	// id is created again, so the helper that gets renamed is unique per run.
	boolID := fmt.Sprintf("mcp_org_bool_%d", time.Now().UnixNano()%1e9)
	entity, renamed := "input_boolean."+boolID, "input_boolean."+boolID+"_renamed"
	organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": "input_boolean", "action": "create", "fields": map[string]any{"name": boolID, "icon": "mdi:toggle-switch"}}, entity)
	organizeLiveOK(t, cs, "ha_list_helpers", map[string]any{}, "mcp_org_number", "mcp_org_select", "mcp_org_schedule")
	organizeLiveOK(t, cs, "ha_list_helpers", map[string]any{"domain": "input_boolean"}, boolID)

	// Renaming an entity_id and removing a registry entry are exercised on a
	// helper this test owns rather than on a shared demo entity.
	organizeLiveErr(t, cs, "ha_manage_registry", map[string]any{"resource": "entity", "action": "update", "id": entity, "new_entity_id": renamed}, "No automation")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "entity", "action": "update", "id": entity, "new_entity_id": renamed, "confirm": true}, renamed)
	organizeLiveOK(t, cs, "ha_list_helpers", map[string]any{"domain": "input_boolean"}, renamed)
	organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": "input_boolean", "action": "update", "id": renamed, "fields": map[string]any{"initial": true}}, "initial true")
	organizeLiveOK(t, cs, "ha_manage_registry", map[string]any{"resource": "entity", "action": "delete", "id": renamed, "confirm": true}, "Removed")

	organizeLiveErr(t, cs, "ha_manage_helper", map[string]any{"domain": "input_number", "action": "delete", "id": "mcp_org_number"}, "confirm")
	for _, d := range []string{"input_number", "input_select", "schedule", "input_boolean"} {
		for _, it := range organizeMust(ha.ListHelpers(ctx, d)) {
			if name, _ := it["name"].(string); strings.HasPrefix(name, "mcp_org_") {
				organizeLiveOK(t, cs, "ha_manage_helper", map[string]any{"domain": d, "action": "delete", "id": it["id"], "confirm": true}, "Deleted")
			}
		}
	}
}

func TestOrganizeIntegrationIntegrations(t *testing.T) {
	cs, ha := organizeLive(t)
	ctx := context.Background()
	entries := organizeMust(ha.ListConfigEntries(ctx, "sun"))
	if len(entries) != 1 {
		t.Fatalf("expected one sun entry, got %d", len(entries))
	}
	id := entries[0].EntryID
	t.Cleanup(func() {
		if _, err := ha.SetConfigEntryDisabled(ctx, id, false); err != nil {
			t.Errorf("re-enable sun: %v", err)
		}
	})
	organizeLiveOK(t, cs, "ha_list_integrations", map[string]any{"domain": "sun"}, id)
	organizeLiveOK(t, cs, "ha_manage_integration", map[string]any{"entry_id": id, "action": "reload"}, "Reloaded Sun", "loaded")
	organizeLiveErr(t, cs, "ha_manage_integration", map[string]any{"entry_id": id, "action": "disable"}, "confirm")
	organizeLiveOK(t, cs, "ha_manage_integration", map[string]any{"entry_id": id, "action": "disable", "confirm": true}, "Disabled Sun", "disabled by user")
	organizeLiveOK(t, cs, "ha_manage_integration", map[string]any{"entry_id": id, "action": "enable"}, "Enabled Sun")
}

func TestOrganizeIntegrationDashboard(t *testing.T) {
	cs, _ := organizeLive(t)
	const path = "mcp-org-dash"
	res := organizeLiveWS(t, "lovelace/dashboards/create", map[string]any{"url_path": path, "title": "mcp_org_dash", "mode": "storage"})
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res, &created)
	t.Cleanup(func() { organizeLiveWS(t, "lovelace/dashboards/delete", map[string]any{"dashboard_id": created.ID}) })

	organizeLiveOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": path}, "never been edited")
	first := "views:\n  - title: One\n    cards:\n      - type: markdown\n        content: |\n          line one\n          line two\n"
	organizeLiveOK(t, cs, "ha_save_dashboard", map[string]any{"url_path": path, "config": first}, "Saved dashboard", "auto-generated")
	got := organizeLiveOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": path}, "1 view", "content: |")
	organizeLiveErr(t, cs, "ha_save_dashboard", map[string]any{"url_path": path, "config": `{"views":[{"title":"Two"},{"title":"Three"}]}`}, "confirm:true")
	out := organizeLiveOK(t, cs, "ha_save_dashboard", map[string]any{"url_path": path, "config": map[string]any{"views": []any{map[string]any{"title": "Two"}, map[string]any{"title": "Three"}}}, "confirm": true}, "2 views", "title: One")

	// The previous config in the answer has to be something that can be saved
	// back as-is to revert.
	prev := regexp.MustCompile("(?s)```yaml\n(.*)```").FindStringSubmatch(out)
	if prev == nil {
		t.Fatalf("no YAML block in %s", out)
	}
	organizeLiveOK(t, cs, "ha_save_dashboard", map[string]any{"url_path": path, "config": prev[1], "confirm": true}, "1 view")
	if again := organizeLiveOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": path}); again != got {
		t.Fatalf("revert did not restore the config:\n%s\nvs\n%s", again, got)
	}
	organizeLiveErr(t, cs, "ha_save_dashboard", map[string]any{"url_path": path, "config": "title: only a view", "confirm": true}, "views")
}

func TestOrganizeIntegrationBackup(t *testing.T) {
	cs, ha := organizeLive(t)
	ctx := context.Background()
	before := map[string]bool{}
	for _, b := range organizeMust(ha.BackupInfo(ctx)).Backups {
		before[b.BackupID] = true
	}
	out, isErr := organizeLiveCall(t, cs, "ha_create_backup", map[string]any{})
	if isErr {
		if !strings.Contains(out, "Settings > System > Backups") {
			t.Fatalf("a failed backup does not say how to fix it: %s", out)
		}
		t.Skipf("automatic backups are not configured here: %s", out)
	}
	if !strings.Contains(out, "Backup finished") {
		t.Fatalf("unexpected answer: %s", out)
	}
	organizeLiveOK(t, cs, "ha_backup_info", map[string]any{}, "backup_id")
	for _, b := range organizeMust(ha.BackupInfo(ctx)).Backups {
		if !before[b.BackupID] {
			organizeLiveWS(t, "backup/delete", map[string]any{"backup_id": b.BackupID})
		}
	}
}

func organizeMust[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("setup: %v", err))
	}
	return v
}

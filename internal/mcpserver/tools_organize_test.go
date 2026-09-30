package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/oauth"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const organizeTestToken = "organize-test-token"

// organizeFake is a Home Assistant that answers the WebSocket commands and
// REST calls the organize tools make with canned data, and records what it was
// sent so a test can check that a refused write never reached it.
type organizeFake struct {
	t        *testing.T
	mu       sync.Mutex
	received []map[string]any
	rest     []string
	ws       map[string]func(msg map[string]any) (any, string)
}

func newOrganizeFake(t *testing.T) *organizeFake {
	f := &organizeFake{t: t}
	f.ws = map[string]func(map[string]any) (any, string){
		"config/area_registry/list": func(map[string]any) (any, string) {
			return []any{
				map[string]any{"area_id": "kitchen", "name": "Kitchen", "floor_id": "ground", "icon": "mdi:stove", "labels": []any{}, "aliases": []any{}},
				map[string]any{"area_id": "garden", "name": "Garden", "labels": []any{"outdoor"}, "aliases": []any{"yard"}},
			}, ""
		},
		"config/floor_registry/list": func(map[string]any) (any, string) {
			return []any{map[string]any{"floor_id": "ground", "name": "Ground Floor", "level": 0, "aliases": []any{}}}, ""
		},
		"config/label_registry/list": func(map[string]any) (any, string) {
			return []any{map[string]any{"label_id": "outdoor", "name": "Outdoor", "color": "green"}}, ""
		},
		"config/category_registry/list": func(msg map[string]any) (any, string) {
			if msg["scope"] == "automation" {
				return []any{map[string]any{"category_id": "01CAT", "name": "Lighting"}}, ""
			}
			return []any{}, ""
		},
		"config/device_registry/list": func(map[string]any) (any, string) {
			return []any{
				map[string]any{"id": "dev1", "name": "Kitchen Lamp", "area_id": "kitchen", "manufacturer": "Signify", "model": "Hue White",
					"config_entries": []any{"entry1"}, "labels": []any{}},
				map[string]any{"id": "dev2", "name": "Garden Sensor", "name_by_user": "Patio Sensor", "manufacturer": "Aqara",
					"config_entries": []any{"entry2"}, "labels": []any{"outdoor"}},
				map[string]any{"id": "dev3", "name": "Old Plug", "config_entries": []any{"entry1"}, "disabled_by": "user", "labels": []any{}},
			}, ""
		},
		"config/entity_registry/list": func(map[string]any) (any, string) {
			return []any{
				map[string]any{"entity_id": "light.kitchen_lamp", "unique_id": "u1", "platform": "hue", "device_id": "dev1",
					"has_entity_name": true, "original_name": nil, "labels": []any{}},
				map[string]any{"entity_id": "sensor.patio_temperature", "unique_id": "u2", "platform": "zha", "device_id": "dev2",
					"has_entity_name": true, "original_name": "Temperature", "area_id": "garden", "labels": []any{"outdoor"}},
				map[string]any{"entity_id": "sensor.patio_battery", "unique_id": "u3", "platform": "zha", "device_id": "dev2",
					"has_entity_name": true, "original_name": "Battery", "disabled_by": "integration", "entity_category": "diagnostic", "labels": []any{}},
				map[string]any{"entity_id": "input_boolean.guest_mode", "unique_id": "guest_mode", "platform": "input_boolean",
					"original_name": "Guest mode", "labels": []any{}},
				map[string]any{"entity_id": "schedule.heating", "unique_id": "heating", "platform": "schedule",
					"original_name": "Heating", "labels": []any{}},
			}, ""
		},
		"config/entity_registry/get": func(msg map[string]any) (any, string) {
			if msg["entity_id"] != "light.kitchen_lamp" {
				return nil, "not_found"
			}
			return map[string]any{"entity_id": "light.kitchen_lamp", "unique_id": "u1", "platform": "hue", "device_id": "dev1",
				"has_entity_name": true, "labels": []any{}}, ""
		},
		"config/entity_registry/update": func(msg map[string]any) (any, string) {
			entry := map[string]any{"entity_id": "light.kitchen_lamp", "platform": "hue", "device_id": "dev1", "has_entity_name": true}
			for _, k := range []string{"name", "area_id", "labels", "hidden_by"} {
				if v, ok := msg[k]; ok {
					entry[k] = v
				}
			}
			if v, ok := msg["new_entity_id"]; ok {
				entry["entity_id"] = v
			}
			return map[string]any{"entity_entry": entry}, ""
		},
		"config/area_registry/delete": func(map[string]any) (any, string) { return nil, "" },
		"search/related": func(map[string]any) (any, string) {
			return map[string]any{"automation": []any{"automation.evening_lights"}, "device": []any{"dev1"}}, ""
		},
		"input_boolean/list": func(map[string]any) (any, string) {
			return []any{map[string]any{"id": "guest_mode", "name": "Guest mode", "icon": "mdi:account"}}, ""
		},
		"input_boolean/update": func(msg map[string]any) (any, string) {
			return map[string]any{"id": msg["input_boolean_id"], "name": msg["name"], "icon": msg["icon"], "initial": msg["initial"]}, ""
		},
		"schedule/list": func(map[string]any) (any, string) {
			return []any{map[string]any{"id": "heating", "name": "Heating", "monday": []any{
				map[string]any{"from": "06:30:00", "to": "08:00:00"}, map[string]any{"from": "17:00:00", "to": "22:00:00"},
			}, "tuesday": []any{}}}, ""
		},
		"config_entries/get": func(map[string]any) (any, string) {
			return []any{
				map[string]any{"entry_id": "entry1", "domain": "hue", "title": "Hue Bridge", "state": "loaded"},
				map[string]any{"entry_id": "entry2", "domain": "zha", "title": "Zigbee", "state": "setup_retry", "reason": "Coordinator not found"},
			}, ""
		},
		"config_entries/get_single": func(msg map[string]any) (any, string) {
			return map[string]any{"config_entry": map[string]any{"entry_id": msg["entry_id"], "domain": "hue", "title": "Hue Bridge", "state": "loaded"}}, ""
		},
		"lovelace/dashboards/list": func(map[string]any) (any, string) {
			return []any{
				map[string]any{"id": "lovelace", "url_path": "lovelace", "title": "Overview", "mode": "storage", "show_in_sidebar": true},
				map[string]any{"id": "energy_yaml", "url_path": "energy-yaml", "title": "Energy", "mode": "yaml", "show_in_sidebar": true},
				map[string]any{"id": "fresh", "url_path": "fresh-dash", "title": "Fresh", "mode": "storage", "show_in_sidebar": false},
			}, ""
		},
		"lovelace/config": func(msg map[string]any) (any, string) {
			if msg["url_path"] == "fresh-dash" {
				return nil, "config_not_found"
			}
			return json.RawMessage(`{"title":"Home","views":[{"title":"Main","cards":[{"type":"markdown","content":"line one\nline two"},{"type":"entities","entities":["light.kitchen_lamp"]}]}]}`), ""
		},
		"lovelace/config/save": func(map[string]any) (any, string) { return nil, "" },
		"backup/info": func(map[string]any) (any, string) {
			return map[string]any{
				"backups": []any{
					map[string]any{"backup_id": "old1", "name": "Before upgrade", "date": "2026-09-01T03:00:00+00:00", "homeassistant_included": true,
						"database_included": true, "homeassistant_version": "2026.8.0", "agents": map[string]any{"backup.local": map[string]any{"protected": true, "size": 52428800}}},
					map[string]any{"backup_id": "new1", "name": "Automatic backup", "date": "2026-09-29T03:00:00+00:00", "homeassistant_included": true,
						"with_automatic_settings": true, "failed_agent_ids": []any{"cloud.cloud"},
						"agents": map[string]any{"backup.local": map[string]any{"protected": false, "size": 1024}}},
				},
				"last_attempted_automatic_backup": "2026-09-29T03:00:00+00:00", "last_completed_automatic_backup": "2026-09-29T03:05:00+00:00",
				"next_automatic_backup": "2026-09-30T03:00:00+00:00", "state": "idle",
			}, ""
		},
		"backup/generate_with_automatic_settings": func(map[string]any) (any, string) {
			return nil, "home_assistant_error"
		},
	}
	return f
}

func (f *organizeFake) serve() *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/websocket" {
			f.mu.Lock()
			f.rest = append(f.rest, r.Method+" "+r.URL.Path)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Path == "/api/states/zone.home":
				w.Write([]byte(`{"entity_id":"zone.home","state":"0","attributes":{"latitude":52.1,"longitude":4.3,"radius":100}}`))
			case strings.HasSuffix(r.URL.Path, "/reload"):
				w.Write([]byte(`{"require_restart":true}`))
			default:
				f.t.Errorf("unexpected REST call %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
			}
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		send := func(v any) {
			b, _ := json.Marshal(v)
			_ = c.Write(ctx, websocket.MessageText, b)
		}
		send(map[string]any{"type": "auth_required"})
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
		send(map[string]any{"type": "auth_ok", "ha_version": "2026.9.4"})
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]any
			_ = json.Unmarshal(data, &msg)
			id := msg["id"]
			typ, _ := msg["type"].(string)
			switch typ {
			case "supported_features":
				send(map[string]any{"id": id, "type": "result", "success": true, "result": nil})
				continue
			case "ping":
				send(map[string]any{"id": id, "type": "pong"})
				continue
			}
			f.mu.Lock()
			f.received = append(f.received, msg)
			h := f.ws[typ]
			f.mu.Unlock()
			if h == nil {
				f.t.Errorf("unexpected command %s", typ)
				send(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": "unknown_command", "message": typ}})
				continue
			}
			result, code := h(msg)
			if code != "" {
				send(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": code, "message": "fake " + code}})
				continue
			}
			send(map[string]any{"id": id, "type": "result", "success": true, "result": result})
		}
	}))
	f.t.Cleanup(ts.Close)
	return ts
}

func (f *organizeFake) sent(typ string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, m := range f.received {
		if m["type"] == typ {
			out = append(out, m)
		}
	}
	return out
}

func organizeConnect(t *testing.T, f *organizeFake, readOnly bool) *mcp.ClientSession {
	t.Helper()
	ts := f.serve()
	ha, err := homeassistant.New(ts.URL, organizeTestToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ha.Close() })
	s := New(ha, oauth.NewSigner("secret"), "https://mcp.example", "test", readOnly)
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := s.build().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func organizeCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
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
	return b.String(), res.IsError
}

func organizeOK(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	out, isErr := organizeCall(t, cs, name, args)
	if isErr {
		t.Fatalf("%s returned an error: %s", name, out)
	}
	return out
}

func organizeRefused(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	out, isErr := organizeCall(t, cs, name, args)
	if !isErr {
		t.Fatalf("%s was accepted: %s", name, out)
	}
	return out
}

func organizeWants(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

var (
	organizeReadTools  = []string{"ha_list_registry", "ha_list_helpers", "ha_list_integrations", "ha_get_dashboard", "ha_backup_info"}
	organizeWriteTools = []string{"ha_manage_registry", "ha_manage_helper", "ha_manage_integration", "ha_save_dashboard", "ha_create_backup"}
)

func TestOrganizeReadOnlyHidesWriteTools(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		res, err := organizeConnect(t, newOrganizeFake(t), readOnly).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		offered := map[string]*mcp.Tool{}
		for _, tool := range res.Tools {
			offered[tool.Name] = tool
		}
		for _, n := range organizeReadTools {
			if offered[n] == nil {
				t.Errorf("readOnly=%v: %s is missing", readOnly, n)
			} else if !offered[n].Annotations.ReadOnlyHint {
				t.Errorf("%s is not annotated read-only", n)
			}
		}
		for _, n := range organizeWriteTools {
			if readOnly && offered[n] != nil {
				t.Errorf("%s is offered in read-only mode", n)
			}
			if !readOnly && offered[n] == nil {
				t.Errorf("%s is missing", n)
			}
		}
		for _, n := range append(organizeReadTools, organizeWriteTools...) {
			if tool := offered[n]; tool != nil && len(tool.Description) < 200 {
				t.Errorf("%s has a %d-character description; a tool needs enough for a model to tell when to reach for it", n, len(tool.Description))
			}
		}
	}
}

func TestOrganizeListEntitiesFollowsDeviceAreaAndHidesDisabled(t *testing.T) {
	cs := organizeConnect(t, newOrganizeFake(t), false)
	out := organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "area": "kitchen"})
	organizeWants(t, out, "light.kitchen_lamp | Kitchen Lamp | hue | area Kitchen (from device)")
	if strings.Contains(out, "patio") {
		t.Errorf("an entity outside the area was listed:\n%s", out)
	}
	// A device named like the entity is left off the line: it repeats the name.
	if strings.Contains(out, "device Kitchen Lamp") {
		t.Errorf("the device name repeats the entity name:\n%s", out)
	}

	out = organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "integration": "zha"})
	organizeWants(t, out, "sensor.patio_temperature | Patio Sensor Temperature | zha | device Patio Sensor | area Garden | labels Outdoor",
		"1 disabled entity not shown")
	if strings.Contains(out, "patio_battery") {
		t.Errorf("a disabled entity was listed by default:\n%s", out)
	}
	out = organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "entities", "integration": "zha", "include_disabled": true, "limit": 1})
	organizeWants(t, out, "2 entities", "1 more not shown")
}

func TestOrganizeListDevices(t *testing.T) {
	cs := organizeConnect(t, newOrganizeFake(t), false)
	out := organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "devices", "label": "Outdoor"})
	organizeWants(t, out, "Patio Sensor (was Garden Sensor) | device_id dev2 | Aqara | integration zha | labels Outdoor | 2 entities")
	out = organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "devices", "integration": "hue"})
	organizeWants(t, out, "Kitchen Lamp | device_id dev1 | area Kitchen | Signify Hue White", "1 disabled device not shown")

	out = organizeRefused(t, cs, "ha_list_registry", map[string]any{"type": "devices", "area": "Attic"})
	organizeWants(t, out, `no area with id or name "Attic"`, "Garden, Kitchen")
}

func TestOrganizeListSmallRegistries(t *testing.T) {
	cs := organizeConnect(t, newOrganizeFake(t), false)
	organizeWants(t, organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "areas"}),
		"Garden | area_id garden | labels Outdoor | aliases yard", "Kitchen | area_id kitchen | floor Ground Floor | icon mdi:stove")
	organizeWants(t, organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "floors"}),
		"Ground Floor | floor_id ground | level 0 | areas Kitchen", "1 area is on no floor")
	organizeWants(t, organizeOK(t, cs, "ha_list_registry", map[string]any{"type": "categories"}),
		"Lighting | category_id 01CAT | scope automation")
	organizeRefused(t, cs, "ha_list_registry", map[string]any{"type": "rooms"})
}

func TestOrganizeListHelpers(t *testing.T) {
	f := newOrganizeFake(t)
	for _, d := range []string{"input_number", "input_select", "input_text", "input_datetime", "input_button", "counter", "timer"} {
		f.ws[d+"/list"] = func(map[string]any) (any, string) { return []any{}, "" }
	}
	out := organizeOK(t, organizeConnect(t, f, false), "ha_list_helpers", map[string]any{})
	organizeWants(t, out, "input_boolean (1):", "Guest mode | input_boolean.guest_mode | id guest_mode | icon mdi:account",
		"Heating | schedule.heating | id heating | monday 06:30-08:00, 17:00-22:00")
	if strings.Contains(out, "tuesday") || strings.Contains(out, "input_number") {
		t.Errorf("empty days or empty domains are noise:\n%s", out)
	}
}

func TestOrganizeUpdateHelperByEntityID(t *testing.T) {
	f := newOrganizeFake(t)
	out := organizeOK(t, organizeConnect(t, f, false), "ha_manage_helper", map[string]any{
		"domain": "input_boolean", "action": "update", "id": "input_boolean.guest_mode", "fields": map[string]any{"initial": true},
	})
	organizeWants(t, out, "Before: Guest mode", "After:  Guest mode | input_boolean.guest_mode | id guest_mode | icon mdi:account | initial true")
	sent := f.sent("input_boolean/update")
	if len(sent) != 1 || sent[0]["input_boolean_id"] != "guest_mode" || sent[0]["icon"] != "mdi:account" {
		t.Fatalf("update was not addressed to the helper id with its other settings kept: %v", sent)
	}
}

func TestOrganizeDeletesNeedConfirm(t *testing.T) {
	f := newOrganizeFake(t)
	cs := organizeConnect(t, f, false)
	organizeWants(t, organizeRefused(t, cs, "ha_manage_registry", map[string]any{"resource": "area", "action": "delete", "id": "Kitchen"}), "confirm:true")
	organizeWants(t, organizeRefused(t, cs, "ha_manage_helper", map[string]any{"domain": "input_boolean", "action": "delete", "id": "guest_mode"}), "confirm:true")
	organizeWants(t, organizeRefused(t, cs, "ha_manage_integration", map[string]any{"entry_id": "entry1", "action": "disable"}), "confirm:true")
	for _, typ := range []string{"config/area_registry/delete", "input_boolean/delete", "config_entries/disable"} {
		if len(f.sent(typ)) > 0 {
			t.Errorf("%s reached Home Assistant without confirm", typ)
		}
	}
	out := organizeOK(t, cs, "ha_manage_registry", map[string]any{"resource": "area", "action": "delete", "id": "Kitchen", "confirm": true})
	organizeWants(t, out, "Deleted area: Kitchen")
	if sent := f.sent("config/area_registry/delete"); len(sent) != 1 || sent[0]["area_id"] != "kitchen" {
		t.Fatalf("the area name was not resolved to its id: %v", sent)
	}
}

// Renaming an entity_id breaks what uses it, so the unconfirmed call must
// name those users and must not rename anything.
func TestOrganizeEntityRenameShowsReferences(t *testing.T) {
	f := newOrganizeFake(t)
	cs := organizeConnect(t, f, false)
	out := organizeRefused(t, cs, "ha_manage_registry", map[string]any{
		"resource": "entity", "action": "update", "id": "light.kitchen_lamp", "new_entity_id": "light.kitchen_main",
	})
	organizeWants(t, out, "automation automation.evening_lights", "confirm:true")
	if strings.Contains(out, "dev1") {
		t.Errorf("the containing device is not a reference:\n%s", out)
	}
	if len(f.sent("config/entity_registry/update")) > 0 {
		t.Fatal("the entity was renamed without confirm")
	}
	out = organizeOK(t, cs, "ha_manage_registry", map[string]any{
		"resource": "entity", "action": "update", "id": "light.kitchen_lamp", "new_entity_id": "light.kitchen_main", "confirm": true,
	})
	organizeWants(t, out, "After:  light.kitchen_main", "not rewritten")
}

func TestOrganizeEntityUpdateResolvesNamesAndClears(t *testing.T) {
	f := newOrganizeFake(t)
	out := organizeOK(t, organizeConnect(t, f, false), "ha_manage_registry", map[string]any{
		"resource": "entity", "action": "update", "id": "light.kitchen_lamp", "labels": []string{"Outdoor"}, "area": "", "hidden": true, "name": "Lamp",
	})
	organizeWants(t, out, "Before: light.kitchen_lamp | Kitchen Lamp | hue | area Kitchen (from device)",
		"After:  light.kitchen_lamp | Lamp (renamed) | hue | device Kitchen Lamp | area Kitchen (from device) | labels Outdoor | hidden by user")
	sent := f.sent("config/entity_registry/update")[0]
	if v, ok := sent["area_id"]; !ok || v != nil {
		t.Errorf("an empty area was not sent as null: %v", sent)
	}
	if labels, _ := sent["labels"].([]any); len(labels) != 1 || labels[0] != "outdoor" {
		t.Errorf("label name was not resolved to its id: %v", sent)
	}
	if _, ok := sent["icon"]; ok {
		t.Errorf("an unchanged field was sent: %v", sent)
	}
}

func TestOrganizeRegistryRejectsInvalidCombinations(t *testing.T) {
	cs := organizeConnect(t, newOrganizeFake(t), false)
	organizeWants(t, organizeRefused(t, cs, "ha_manage_registry", map[string]any{"resource": "device", "action": "delete", "id": "dev1", "confirm": true}), "only be updated")
	organizeWants(t, organizeRefused(t, cs, "ha_manage_registry", map[string]any{"resource": "entity", "action": "create", "name": "x"}), "integration")
	organizeWants(t, organizeRefused(t, cs, "ha_manage_registry", map[string]any{"resource": "person", "action": "create", "name": "x"}), "People")
	organizeWants(t, organizeRefused(t, cs, "ha_manage_registry", map[string]any{"resource": "category", "action": "create", "name": "x"}), "scope")
	organizeWants(t, organizeRefused(t, cs, "ha_manage_registry", map[string]any{"resource": "area", "action": "update", "id": "kitchen"}), "nothing to change")
}

func TestOrganizeListIntegrationsFlagsProblems(t *testing.T) {
	out := organizeOK(t, organizeConnect(t, newOrganizeFake(t), false), "ha_list_integrations", map[string]any{})
	organizeWants(t, out, "Zigbee | domain zha | setup_retry (Coordinator not found) | entry_id entry2", "1 enabled entry is not loaded")
}

func TestOrganizeReloadReportsRestart(t *testing.T) {
	f := newOrganizeFake(t)
	out := organizeOK(t, organizeConnect(t, f, false), "ha_manage_integration", map[string]any{"entry_id": "entry1", "action": "reload"})
	organizeWants(t, out, "Reloaded Hue Bridge (hue).", "needs a restart")
}

func TestOrganizeGetDashboard(t *testing.T) {
	cs := organizeConnect(t, newOrganizeFake(t), false)
	out := organizeOK(t, cs, "ha_get_dashboard", map[string]any{})
	organizeWants(t, out, "Overview | url_path lovelace | storage | default", "Energy | url_path energy-yaml | yaml", "Fresh | url_path fresh-dash | storage | hidden from sidebar")

	out = organizeOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": "default"})
	// HA's key order is kept so a card still reads "type" first, and a
	// multi-line string stays readable as a block.
	organizeWants(t, out, "Dashboard lovelace, 1 view.", "```yaml\ntitle: Home\nviews:\n  - title: Main\n    cards:\n      - type: markdown\n        content: |-\n          line one\n          line two\n")

	organizeWants(t, organizeOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": "fresh-dash"}), "never been edited")
	organizeWants(t, organizeOK(t, cs, "ha_get_dashboard", map[string]any{"url_path": "energy-yaml"}), "YAML mode")
}

func TestOrganizeSaveDashboard(t *testing.T) {
	f := newOrganizeFake(t)
	cs := organizeConnect(t, f, false)
	cfg := "views:\n  - title: New\n    cards: []\n"

	organizeWants(t, organizeRefused(t, cs, "ha_save_dashboard", map[string]any{"url_path": "lovelace", "config": cfg}), "confirm:true")
	organizeWants(t, organizeRefused(t, cs, "ha_save_dashboard", map[string]any{"url_path": "energy-yaml", "config": cfg, "confirm": true}), "YAML mode")
	organizeWants(t, organizeRefused(t, cs, "ha_save_dashboard", map[string]any{"url_path": "lovelace", "config": "type: entities\nentities: []\n", "confirm": true}), "views")
	organizeWants(t, organizeRefused(t, cs, "ha_save_dashboard", map[string]any{"url_path": "lovelace", "config": "views: [unclosed", "confirm": true}), "not valid YAML")
	if len(f.sent("lovelace/config/save")) > 0 {
		t.Fatal("a refused save reached Home Assistant")
	}

	out := organizeOK(t, cs, "ha_save_dashboard", map[string]any{"url_path": "lovelace", "config": cfg, "confirm": true})
	organizeWants(t, out, "Saved dashboard lovelace (1 view).", "previous config", "title: Home")
	saved := f.sent("lovelace/config/save")[0]
	views := saved["config"].(map[string]any)["views"].([]any)
	if views[0].(map[string]any)["title"] != "New" {
		t.Fatalf("YAML was not sent as a config object: %v", saved)
	}

	// A never-saved dashboard has nothing to lose, so it needs no confirm.
	out = organizeOK(t, cs, "ha_save_dashboard", map[string]any{"url_path": "fresh-dash", "config": map[string]any{"views": []any{}}})
	organizeWants(t, out, "Saved dashboard fresh-dash (0 views).", "auto-generated")
}

func TestOrganizeBackupInfo(t *testing.T) {
	out := organizeOK(t, organizeConnect(t, newOrganizeFake(t), false), "ha_backup_info", map[string]any{})
	organizeWants(t, out,
		"Automatic backups: last attempted 2026-09-29 03:00 UTC, last completed 2026-09-29 03:05 UTC, next 2026-09-30 03:00 UTC.",
		"2 backups, newest first:\n2026-09-29 03:00 UTC | Automatic backup",
		"FAILED to upload to cloud.cloud",
		"2026-09-01 03:00 UTC | Before upgrade | 50.0 MB | in backup.local (encrypted) | Home Assistant 2026.8.0 + database | backup_id old1")
}

func TestOrganizeCreateBackupExplainsMissingSettings(t *testing.T) {
	out := organizeRefused(t, organizeConnect(t, newOrganizeFake(t), false), "ha_create_backup", map[string]any{})
	organizeWants(t, out, "fake home_assistant_error", "Settings > System > Backups")
}

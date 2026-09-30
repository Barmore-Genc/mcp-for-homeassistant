package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/oauth"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	stateTestToken = "ha-admin-token"
	stateCamToken  = "c4m3r4t0k3n5ecret0123456789abcdef"
	stateTestJWT   = automationTestJWT
)

var stateTestNow = time.Date(2026, 3, 9, 15, 4, 0, 0, time.UTC)

// stateFakeHA serves canned REST and WebSocket answers for the endpoints the
// state tools use, and records the service calls it receives.
type stateFakeHA struct {
	srv         *httptest.Server
	mu          sync.Mutex
	calls       []string
	configValid bool
}

func (f *stateFakeHA) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *stateFakeHA) called(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

func stateCameraState() map[string]any {
	return map[string]any{
		"entity_id": "camera.porch", "state": "idle",
		"attributes": map[string]any{
			"friendly_name":  "Porch",
			"access_token":   stateCamToken,
			"entity_picture": "/api/camera_proxy/camera.porch?token=" + stateCamToken,
		},
		"last_changed": "2026-03-09T14:00:00Z", "last_updated": "2026-03-09T14:00:00Z",
	}
}

func stateTestStates() []any {
	return []any{
		map[string]any{"entity_id": "light.kitchen", "state": "on", "attributes": map[string]any{"friendly_name": "Kitchen Light", "brightness": 180, "effect": nil},
			"last_changed": "2026-03-09T12:00:00Z", "last_updated": "2026-03-09T12:30:00Z"},
		map[string]any{"entity_id": "light.bedroom", "state": "off", "attributes": map[string]any{"friendly_name": "Bedroom Light"},
			"last_changed": "2026-03-09T10:00:00Z", "last_updated": "2026-03-09T10:00:00Z"},
		map[string]any{"entity_id": "sensor.kitchen_temp", "state": "21.5", "attributes": map[string]any{"friendly_name": "Kitchen Temperature", "unit_of_measurement": "°C"},
			"last_changed": "2026-03-09T15:00:00Z", "last_updated": "2026-03-09T15:00:00Z"},
		stateCameraState(),
		map[string]any{"entity_id": "todo.shopping", "state": "2", "attributes": map[string]any{"friendly_name": "Shopping"},
			"last_changed": "2026-03-09T15:00:00Z", "last_updated": "2026-03-09T15:00:00Z"},
	}
}

func newStateFakeHA(t *testing.T) *stateFakeHA {
	t.Helper()
	f := &stateFakeHA{configValid: true}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/states", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, stateTestStates()) })
	mux.HandleFunc("GET /api/states/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, s := range stateTestStates() {
			if s.(map[string]any)["entity_id"] == r.PathValue("id") {
				writeJSON(w, s)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"message": "Entity not found."})
	})
	mux.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []any{
			map[string]any{"domain": "light", "services": map[string]any{
				"turn_on": map[string]any{
					"fields": map[string]any{
						"brightness_pct": map[string]any{"selector": map[string]any{"number": map[string]any{"min": 0, "max": 100, "unit_of_measurement": "%"}}},
						"advanced": map[string]any{"collapsed": true, "fields": map[string]any{
							"flash": map[string]any{"selector": map[string]any{"select": map[string]any{"options": []any{"long", "short"}}}},
						}},
					},
					"target": map[string]any{"entity": []any{map[string]any{"domain": []any{"light"}}}},
				},
				"turn_off": map[string]any{"fields": map[string]any{}},
			}},
			map[string]any{"domain": "todo", "services": map[string]any{
				"get_items": map[string]any{"fields": map[string]any{}, "response": map[string]any{"optional": false}},
				"add_item":  map[string]any{"fields": map[string]any{"item": map[string]any{"required": true, "example": "Milk", "selector": map[string]any{"text": map[string]any{}}}}},
			}},
		})
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"language": "de", "time_zone": "UTC", "version": "2026.9.4"})
	})
	mux.HandleFunc("POST /api/template", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "token is "+stateCamToken)
	})
	mux.HandleFunc("POST /api/services/{domain}/{service}", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.PathValue("domain") + "." + r.PathValue("service"))
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		changed := []any{stateCameraState()}
		if r.URL.Query().Has("return_response") {
			writeJSON(w, map[string]any{"changed_states": changed, "service_response": map[string]any{
				"camera.porch": map[string]any{"access_token": stateCamToken, "url": "/api/camera_proxy/camera.porch?token=" + stateCamToken},
			}})
			return
		}
		writeJSON(w, changed)
	})
	mux.HandleFunc("POST /api/config/core/check_config", func(w http.ResponseWriter, r *http.Request) {
		if f.configValid {
			writeJSON(w, map[string]any{"result": "valid", "errors": nil, "warnings": nil})
		} else {
			writeJSON(w, map[string]any{"result": "invalid", "errors": "Invalid config for 'light' at configuration.yaml, line 12", "warnings": nil})
		}
	})
	mux.HandleFunc("GET /api/camera_proxy/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nfakeimage"))
	})
	mux.HandleFunc("GET /api/calendars", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []any{map[string]any{"entity_id": "calendar.family", "name": "Family"}})
	})
	mux.HandleFunc("GET /api/calendars/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []any{
			map[string]any{"summary": "Dentist", "start": map[string]any{"dateTime": "2026-03-10T09:00:00Z"}, "end": map[string]any{"dateTime": "2026-03-10T10:00:00Z"}, "location": "Main St"},
			map[string]any{"summary": "Holiday", "start": map[string]any{"date": "2026-03-12"}, "end": map[string]any{"date": "2026-03-14"}},
		})
	})
	mux.HandleFunc("GET /api/error_log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "line one\nline two\nconnecting with ?access_token=SECRETTOK5 and "+stateTestJWT+"\n")
	})
	mux.HandleFunc("/api/websocket", func(w http.ResponseWriter, r *http.Request) { f.serveWS(t, w, r) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *stateFakeHA) serveWS(t *testing.T, w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := context.Background()
	var wmu sync.Mutex
	send := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
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
		result := func(v any) { send(map[string]any{"id": id, "type": "result", "success": true, "result": v}) }
		typ, _ := msg["type"].(string)
		f.record("ws:" + typ)
		switch typ {
		case "supported_features", "unsubscribe_events":
			result(nil)
		case "ping":
			send(map[string]any{"id": id, "type": "pong"})
		case "config/entity_registry/list":
			result([]any{
				map[string]any{"entity_id": "light.kitchen", "platform": "hue", "device_id": "dev1", "labels": []any{"night"}},
				map[string]any{"entity_id": "light.bedroom", "platform": "hue", "area_id": "bedroom", "hidden_by": "user"},
				map[string]any{"entity_id": "sensor.kitchen_temp", "platform": "zha", "device_id": "dev1"},
				map[string]any{"entity_id": "camera.porch", "platform": "onvif"},
			})
		case "config/device_registry/list":
			result([]any{map[string]any{"id": "dev1", "name": "Kitchen Hub", "area_id": "kitchen", "model": "Hub 2", "manufacturer": "Acme"}})
		case "config/area_registry/list":
			result([]any{map[string]any{"area_id": "kitchen", "name": "Kitchen"}, map[string]any{"area_id": "bedroom", "name": "Bedroom"}})
		case "config/label_registry/list":
			result([]any{map[string]any{"label_id": "night", "name": "Night mode"}})
		case "history/history_during_period":
			base := float64(stateTestNow.Add(-24 * time.Hour).Unix())
			var temps []any
			for i := range 100 {
				temps = append(temps, map[string]any{"s": []string{"20", "21", "22", "23"}[i%4], "lu": base + float64(i)*600})
			}
			temps[0].(map[string]any)["a"] = map[string]any{"friendly_name": "Kitchen Temperature", "unit_of_measurement": "°C"}
			result(map[string]any{
				"sensor.kitchen_temp": temps,
				"light.kitchen": []any{
					map[string]any{"s": "off", "lu": base, "a": map[string]any{"friendly_name": "Kitchen Light"}},
					map[string]any{"s": "on", "lu": base + 18*3600},
				},
			})
		case "logbook/get_events":
			result([]any{
				map[string]any{"when": float64(stateTestNow.Add(-time.Hour).Unix()), "entity_id": "light.kitchen", "name": "Kitchen Light", "state": "on",
					"context_entity_id": "automation.evening", "context_entity_id_name": "Evening lights", "context_event_type": "automation_triggered"},
			})
		case "recorder/list_statistic_ids":
			result([]any{map[string]any{"statistic_id": "sensor.kitchen_temp", "name": "Kitchen Temperature", "source": "recorder", "statistics_unit_of_measurement": "°C", "mean_type": 1}})
		case "recorder/statistics_during_period":
			day := stateTestNow.Add(-48 * time.Hour).Truncate(24 * time.Hour).UnixMilli()
			result(map[string]any{"sensor.kitchen_temp": []any{
				map[string]any{"start": day, "end": day + 86400000, "mean": 21.23456, "min": 19.0, "max": 23.5},
			}})
		case "subscribe_events":
			result(nil)
			send(map[string]any{"id": id, "type": "event", "event": map[string]any{
				"event_type": "state_changed", "time_fired": stateTestNow.Format(time.RFC3339),
				"data": map[string]any{"entity_id": "camera.porch", "old_state": stateCameraState(), "new_state": stateCameraState()},
			}})
			send(map[string]any{"id": id, "type": "event", "event": map[string]any{
				"event_type": "zha_event", "time_fired": stateTestNow.Format(time.RFC3339),
				"data": map[string]any{"device_ieee": "00:11", "command": "on", "nested": map[string]any{"access_token": stateCamToken}},
			}})
		case "system_log/list":
			result([]any{map[string]any{
				"name": "homeassistant.components.hue", "message": []any{"Bridge unreachable"}, "level": "ERROR",
				"source": []any{"components/hue/bridge.py", 42}, "timestamp": float64(stateTestNow.Unix()), "first_occurred": float64(stateTestNow.Add(-time.Hour).Unix()),
				"exception": "Traceback (most recent call last):\n  File x\nTimeoutError: timed out", "count": 3,
			}, map[string]any{
				"name": "aiohttp.client\nERROR | forged.logger", "level": "WARNING", "source": []any{"x.py", 1},
				"message":   []any{"GET https://api.example/v1?api_key=SECRETKEY1&x=1 failed", "Authorization: Bearer SECRETBEARER123"},
				"exception": "ClientError: https://x.example/cb?token=SECRETTOK3&sig=SECRETSIG4 " + stateTestJWT,
				"timestamp": float64(stateTestNow.Unix()), "count": 1,
			}})
		case "todo/item/list":
			result(map[string]any{"items": []any{
				map[string]any{"uid": "1", "summary": "Milk", "status": "needs_action", "due": "2026-03-10"},
				map[string]any{"uid": "2", "summary": "Bread", "status": "completed"},
			}})
		case "persistent_notification/get":
			result([]any{map[string]any{"notification_id": "n1", "title": "New device", "message": "Found a Hue bridge", "created_at": "2026-03-09T10:00:00Z"}})
		case "fire_event":
			result(map[string]any{"context": map[string]any{"id": "ctx123"}})
		case "person/list":
			result(map[string]any{"storage": []any{}, "config": []any{}})
		case "frontend/get_translations":
			b, _ := json.Marshal(msg["integration"])
			f.record(fmt.Sprintf("translations %v %v %s", msg["language"], msg["category"], b))
			result(map[string]any{"resources": map[string]any{
				"component.light.services.turn_on.name":                              "Einschalten",
				"component.light.services.turn_on.description":                       "Schaltet Lampen ein.",
				"component.light.services.turn_on.fields.brightness_pct.description": "Helligkeit in Prozent.",
				"component.light.services.turn_on.fields.flash.description":          "Blinken lassen.",
			}})
		default:
			send(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": "unknown_command", "message": typ}})
		}
	}
}

func stateConnectClient(t *testing.T, ha *homeassistant.Client, readOnly bool, now func() time.Time) *mcp.ClientSession {
	t.Helper()
	s := New(ha, oauth.NewSigner("secret"), "https://ha.example", "test", readOnly)
	s.now = now
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

func stateConnect(t *testing.T, readOnly bool) (*mcp.ClientSession, *stateFakeHA) {
	t.Helper()
	fake := newStateFakeHA(t)
	ha, err := homeassistant.New(fake.srv.URL, stateTestToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ha.Close() })
	return stateConnectClient(t, ha, readOnly, func() time.Time { return stateTestNow }), fake
}

func stateCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, *mcp.CallToolResult) {
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
	return b.String(), res
}

func stateCallOK(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	out, res := stateCall(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s returned an error: %s", name, out)
	}
	return out
}

var (
	stateReadTools = []string{
		"ha_list_entities", "ha_get_state", "ha_list_services", "ha_render_template", "ha_history", "ha_logbook",
		"ha_statistics", "ha_listen_events", "ha_system_log", "ha_check_config", "ha_calendar_events",
		"ha_list_todo_items", "ha_camera_snapshot", "ha_list_notifications",
	}
	stateWriteTools = []string{"ha_call_service", "ha_fire_event", "ha_restart"}
)

func stateToolMap(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

func TestStateToolSurface(t *testing.T) {
	cs, _ := stateConnect(t, false)
	tools := stateToolMap(t, cs)
	for _, name := range append(append([]string{}, stateReadTools...), stateWriteTools...) {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("%s is not offered", name)
			continue
		}
		if len(tool.Description) < 120 {
			t.Errorf("%s has a %d-character description", name, len(tool.Description))
		}
	}
	for _, name := range stateReadTools {
		if tools[name] != nil && (tools[name].Annotations == nil || !tools[name].Annotations.ReadOnlyHint) {
			t.Errorf("%s is not annotated read-only", name)
		}
	}
}

func TestStateReadOnlyHidesWriteTools(t *testing.T) {
	cs, _ := stateConnect(t, true)
	tools := stateToolMap(t, cs)
	for _, name := range stateWriteTools {
		if _, ok := tools[name]; ok {
			t.Errorf("%s is offered in read-only mode", name)
		}
	}
	for _, name := range stateReadTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("%s is missing in read-only mode", name)
		}
	}
}

func stateNoToken(t *testing.T, tool, out string) {
	t.Helper()
	if strings.Contains(out, stateCamToken) {
		t.Fatalf("%s leaked the camera access token: %s", tool, out)
	}
}

func TestStateListEntities(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_list_entities", map[string]any{})
	stateNoToken(t, "ha_list_entities", out)
	if !strings.Contains(out, "sensor.kitchen_temp | Kitchen Temperature | 21.5 °C | Kitchen") {
		t.Fatalf("sensor line not rendered with unit and the device's area: %s", out)
	}
	if !strings.Contains(out, "light.bedroom | Bedroom Light | off | Bedroom | hidden") {
		t.Fatalf("entity area or hidden flag missing: %s", out)
	}

	out = stateCallOK(t, cs, "ha_list_entities", map[string]any{"area": "kitchen", "domain": "light"})
	if !strings.Contains(out, "1 entities") || !strings.Contains(out, "light.kitchen") {
		t.Fatalf("area and domain filters did not combine: %s", out)
	}
	out = stateCallOK(t, cs, "ha_list_entities", map[string]any{"label": "Night mode"})
	if !strings.Contains(out, "1 entities") || !strings.Contains(out, "light.kitchen") {
		t.Fatalf("label filter by name failed: %s", out)
	}
	out = stateCallOK(t, cs, "ha_list_entities", map[string]any{"search": "kitchen temp"})
	if !strings.Contains(out, "1 entities") {
		t.Fatalf("search words did not AND: %s", out)
	}
	out = stateCallOK(t, cs, "ha_list_entities", map[string]any{"limit": 2})
	if !strings.Contains(out, "5 entities match; showing the first 2") || !strings.Contains(out, "By domain: light 2") {
		t.Fatalf("truncation not reported: %s", out)
	}
	if out, res := stateCall(t, cs, "ha_list_entities", map[string]any{"area": "garage"}); !res.IsError || !strings.Contains(out, "Bedroom, Kitchen") {
		t.Fatalf("an unknown area should fail and name the areas: %s", out)
	}
}

func TestStateGetStateStripsCameraToken(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_get_state", map[string]any{"entity_ids": []string{"camera.porch", "light.kitchen", "light.nope"}})
	stateNoToken(t, "ha_get_state", out)
	if strings.Contains(out, "access_token") || strings.Contains(out, "entity_picture") {
		t.Fatalf("token-bearing attributes were printed: %s", out)
	}
	for _, want := range []string{
		"light.kitchen (Kitchen Light)", "brightness: 180", "area Kitchen (kitchen)", "device Kitchen Hub, Hub 2 by Acme (id dev1)",
		"labels Night mode", "last_updated: 2026-03-09 12:30:00", "light.nope: no such entity",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "effect") {
		t.Errorf("null attributes should be left out: %s", out)
	}
}

func TestStateSanitize(t *testing.T) {
	in := map[string]any{
		"access_token":         "x",
		"entity_picture":       "/api/camera_proxy/camera.a?token=abc",
		"entity_picture_local": "/api/media_player_proxy/media_player.a?token=abc&cache=1",
		"icon_url":             "/api/brands/integration/demo/icon.png",
		"nested":               []any{map[string]any{"access_token": "y", "url": "http://x/y?a=1&token=secret"}},
	}
	out, _ := json.Marshal(sanitizeValue(in))
	s := string(out)
	for _, bad := range []string{"access_token", "entity_picture", "token=abc", "secret"} {
		if strings.Contains(s, bad) {
			t.Errorf("%q survived: %s", bad, s)
		}
	}
	if !strings.Contains(s, "icon.png") || !strings.Contains(s, "token=REDACTED") {
		t.Errorf("harmless values were removed: %s", s)
	}
}

func TestStateListServices(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_list_services", map[string]any{})
	if !strings.Contains(out, "light: turn_off, turn_on") || !strings.Contains(out, "todo: add_item, get_items") {
		t.Fatalf("domain overview wrong: %s", out)
	}
	out = stateCallOK(t, cs, "ha_list_services", map[string]any{"domain": "light.turn_on,todo"})
	for _, want := range []string{
		"light.turn_on (Einschalten) | target: entity (light)", "brightness_pct: number 0–100 %", "flash: one of long|short",
		"todo.get_items | returns data: call with return_response:true", "item: required, text, e.g. Milk",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "turn_off") {
		t.Errorf("domain.service did not narrow to one service: %s", out)
	}
}

func TestStateListServicesTranslations(t *testing.T) {
	cs, f := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_list_services", map[string]any{"domain": "light.turn_on,light.turn_off,todo"})
	for _, want := range []string{
		"light.turn_on (Einschalten) | target: entity (light)\n  Schaltet Lampen ein.\n",
		"brightness_pct: number 0–100 % | Helligkeit in Prozent.",
		"flash: one of long|short | Blinken lassen.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !f.called(`translations de services ["light","todo"]`) {
		t.Errorf("translations were not requested for just the listed integrations in the home's language: %v", f.calls)
	}

	stateCallOK(t, cs, "ha_list_services", map[string]any{})
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "translations") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the domain overview loaded translations it does not show")
	}
}

func TestStateRenderTemplateRedactsToken(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_render_template", map[string]any{"template": "{{ state_attr('camera.porch','access_token') }}"})
	stateNoToken(t, "ha_render_template", out)
	if out != "token is REDACTED" {
		t.Fatalf("got %q", out)
	}
}

func TestStateHistory(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_history", map[string]any{"entity_ids": []string{"sensor.kitchen_temp", "light.kitchen"}, "max_points": 10})
	for _, want := range []string{
		"sensor.kitchen_temp (Kitchen Temperature): 100 values, min 20 °C", "max 23 °C", "10 buckets of 2h24m",
		"light.kitchen (Kitchen Light): 1 changes; time in state: off 18h, on 6h",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "\n  2026-"); n > 12 {
		t.Errorf("a long series was not reduced: %d lines\n%s", n, out)
	}
}

func TestStateLogbookNamesCause(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_logbook", map[string]any{"start": "-6h"})
	if !strings.Contains(out, "Kitchen Light (light.kitchen) changed to on | triggered by Evening lights (automation.evening)") {
		t.Fatalf("cause not rendered: %s", out)
	}
}

func TestStateStatistics(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_statistics", map[string]any{"search": "kitchen"})
	if !strings.Contains(out, "sensor.kitchen_temp | Kitchen Temperature | °C | mean/min/max") {
		t.Fatalf("listing wrong: %s", out)
	}
	out = stateCallOK(t, cs, "ha_statistics", map[string]any{"statistic_ids": []string{"sensor.kitchen_temp", "sensor.nope"}})
	for _, want := range []string{"Statistics per day", "in °C: 1 rows", "mean 21.235 min 19 max 23.5", "sensor.nope: no such statistic"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestStateListenEventsSanitizes(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_listen_events", map[string]any{"seconds": 1})
	stateNoToken(t, "ha_listen_events", out)
	if !strings.Contains(out, "state_changed camera.porch: idle → idle") || !strings.Contains(out, `zha_event {"command":"on"`) {
		t.Fatalf("events not rendered: %s", out)
	}
	out = stateCallOK(t, cs, "ha_listen_events", map[string]any{"seconds": 1, "entity_id": "camera.porch"})
	if strings.Contains(out, "zha_event") || !strings.Contains(out, "1 events") {
		t.Fatalf("entity filter did not apply: %s", out)
	}
	if _, res := stateCall(t, cs, "ha_listen_events", map[string]any{"seconds": 600}); !res.IsError {
		t.Fatal("an unbounded listen was accepted")
	}
}

func TestStateSystemLog(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_system_log", map[string]any{"raw_log": true})
	for _, want := range []string{"ERROR ×3 | homeassistant.components.hue | components/hue/bridge.py:42", "Bridge unreachable", "| TimeoutError: timed out", "line two"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestStateCalendarTodoNotifications(t *testing.T) {
	cs, _ := stateConnect(t, false)
	if out := stateCallOK(t, cs, "ha_calendar_events", map[string]any{}); !strings.Contains(out, "calendar.family | Family") {
		t.Errorf("calendars not listed: %s", out)
	}
	out := stateCallOK(t, cs, "ha_calendar_events", map[string]any{"entity_id": "calendar.family"})
	if !strings.Contains(out, "2026-03-10 09:00–10:00 | Dentist | at Main St") || !strings.Contains(out, "2026-03-12 → 2026-03-13 all day | Holiday") {
		t.Errorf("events wrong: %s", out)
	}
	if out := stateCallOK(t, cs, "ha_list_todo_items", map[string]any{}); !strings.Contains(out, "todo.shopping | Shopping | 2 open") {
		t.Errorf("todo lists wrong: %s", out)
	}
	out = stateCallOK(t, cs, "ha_list_todo_items", map[string]any{"entity_id": "todo.shopping"})
	if !strings.Contains(out, "[ ] Milk | due 2026-03-10") || !strings.Contains(out, "[x] Bread") {
		t.Errorf("todo items wrong: %s", out)
	}
	if out := stateCallOK(t, cs, "ha_list_notifications", map[string]any{}); !strings.Contains(out, "n1 | 2026-03-09 10:00:00 | New device: Found a Hue bridge") {
		t.Errorf("notifications wrong: %s", out)
	}
}

func TestStateCameraSnapshotReturnsImage(t *testing.T) {
	cs, _ := stateConnect(t, false)
	_, res := stateCall(t, cs, "ha_camera_snapshot", map[string]any{"entity_id": "camera.porch", "width": 640})
	if res.IsError {
		t.Fatal("snapshot failed")
	}
	var img *mcp.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			img = ic
		}
	}
	if img == nil || img.MIMEType != "image/png" || !strings.HasPrefix(string(img.Data), "\x89PNG") {
		t.Fatalf("no image content: %+v", res.Content)
	}
}

func TestStateCallServiceSanitizesAndReports(t *testing.T) {
	cs, fake := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_call_service", map[string]any{
		"domain": "camera", "service": "snapshot", "target": map[string]any{"entity_id": []string{"camera.porch"}}, "return_response": true,
	})
	stateNoToken(t, "ha_call_service", out)
	if !fake.called("camera.snapshot") || !strings.Contains(out, "camera.porch | Porch | idle") || !strings.Contains(out, "Response:") {
		t.Fatalf("call not reported: %s", out)
	}
	stateCallOK(t, cs, "ha_call_service", map[string]any{"domain": "", "service": "light.turn_on"})
	if !fake.called("light.turn_on") {
		t.Fatal("domain.service form was not accepted")
	}
	if _, res := stateCall(t, cs, "ha_call_service", map[string]any{"domain": "homeassistant", "service": "restart"}); !res.IsError || fake.called("homeassistant.restart") {
		t.Fatal("a restart went around ha_restart's config check")
	}
}

func TestStateFireEvent(t *testing.T) {
	cs, fake := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_fire_event", map[string]any{"event_type": "mcp_state_test", "data": map[string]any{"a": 1}})
	if !strings.Contains(out, "ctx123") || !fake.called("ws:fire_event") {
		t.Fatalf("event not fired: %s", out)
	}
}

func TestStateRestartChecksConfigFirst(t *testing.T) {
	cs, fake := stateConnect(t, false)
	if _, res := stateCall(t, cs, "ha_restart", map[string]any{}); !res.IsError {
		t.Fatal("an unconfirmed restart was accepted")
	}
	fake.configValid = false
	out, res := stateCall(t, cs, "ha_restart", map[string]any{"confirm": true})
	if !res.IsError || !strings.Contains(out, "line 12") {
		t.Fatalf("an invalid config did not block the restart: %s", out)
	}
	if fake.called("homeassistant.restart") {
		t.Fatal("restart was called despite an invalid config")
	}
	fake.configValid = true
	stateCallOK(t, cs, "ha_restart", map[string]any{"confirm": true})
	if !fake.called("homeassistant.restart") {
		t.Fatal("restart was not called")
	}
}

func TestStateCheckConfig(t *testing.T) {
	cs, fake := stateConnect(t, false)
	fake.configValid = false
	if out := stateCallOK(t, cs, "ha_check_config", map[string]any{}); !strings.Contains(out, "Configuration is invalid") || !strings.Contains(out, "line 12") {
		t.Fatalf("got %s", out)
	}
}

func TestStateParseTime(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"-6h", "2026-03-09 09:04"},
		{"-30min", "2026-03-09 14:34"},
		{"now", "2026-03-09 15:04"},
		{"2026-03-01 08:30", "2026-03-01 08:30"},
		{"2026-03-01T08:30:00Z", "2026-03-01 08:30"},
		{"yesterday", "2026-03-08 00:00"},
		{"-7d", "2026-03-02 00:00"},
	} {
		got, err := stateParseTime(c.in, stateTestNow)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%q = %s, want %s", c.in, got.Format("2006-01-02 15:04"), c.want)
		}
	}
	if _, err := stateParseTime("last tuesday", stateTestNow); err == nil {
		t.Error("nonsense was accepted")
	}
}

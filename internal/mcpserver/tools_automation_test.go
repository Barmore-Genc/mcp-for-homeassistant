package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/oauth"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var automationTestNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// automationFake is a Home Assistant with just enough REST and WebSocket
// behaviour for the automation tools: stored configs, states derived from
// them, service calls, and canned answers for the rest.
type automationFake struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	configs map[string]map[string]map[string]any
	// raw is what GET returns for each config, so the key order HA keeps can
	// be tested.
	raw      map[string]map[string][]byte
	extra    []map[string]any
	services []string
	ws       []map[string]any
}

func newAutomationFake(t *testing.T) *automationFake {
	t.Helper()
	f := &automationFake{t: t, configs: map[string]map[string]map[string]any{}, raw: map[string]map[string][]byte{}}
	for kind, items := range map[string]map[string]string{
		"automation": {"1700000000001": `{"id":"1700000000001","alias":"Porch light at sunset",` +
			`"triggers":[{"trigger":"sun","event":"sunset"}],` +
			`"actions":[{"action":"light.turn_on","target":{"entity_id":"light.porch"}}],"mode":"single"}`},
		"script": {"morning": `{"alias":"Morning","sequence":[{"delay":1}]}`},
		"scene":  {"1700000000002": `{"id":"1700000000002","name":"Movie","entities":{"light.tv":"off"}}`},
	} {
		f.configs[kind], f.raw[kind] = map[string]map[string]any{}, map[string][]byte{}
		for id, js := range items {
			f.store(kind, id, []byte(js))
		}
	}
	// An automation defined in configuration.yaml: it has an id, but the
	// config API does not know it.
	f.extra = []map[string]any{
		{"entity_id": "automation.yaml_only", "state": "on", "attributes": map[string]any{"id": "yaml1", "friendly_name": "YAML only", "last_triggered": nil}},
		{"entity_id": "automation.no_id", "state": "off", "attributes": map[string]any{"friendly_name": "No id", "last_triggered": nil}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/websocket", f.serveWS)
	mux.HandleFunc("GET /api/states", func(w http.ResponseWriter, r *http.Request) {
		automationWriteJSON(w, f.states())
	})
	mux.HandleFunc("GET /api/states/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, s := range f.states() {
			if s["entity_id"] == r.PathValue("id") {
				automationWriteJSON(w, s)
				return
			}
		}
		http.Error(w, `{"message":"Entity not found."}`, http.StatusNotFound)
	})
	mux.HandleFunc("/api/config/{kind}/config/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		kind, id := r.PathValue("kind"), r.PathValue("id")
		switch r.Method {
		case http.MethodGet:
			if raw, ok := f.raw[kind][id]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
				return
			}
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var c map[string]any
			_ = json.Unmarshal(body, &c)
			if _, ok := c["entities"].(string); ok {
				http.Error(w, `{"message":"Message malformed: expected dict at 'entities'"}`, http.StatusBadRequest)
				return
			}
			f.store(kind, id, body)
			automationWriteJSON(w, map[string]any{"result": "ok"})
			return
		case http.MethodDelete:
			if _, ok := f.configs[kind][id]; ok {
				delete(f.configs[kind], id)
				delete(f.raw[kind], id)
				automationWriteJSON(w, map[string]any{"result": "ok"})
				return
			}
		}
		http.Error(w, `{"message":"Resource not found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("POST /api/services/{domain}/{service}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		f.mu.Lock()
		f.services = append(f.services, r.PathValue("domain")+"."+r.PathValue("service")+" "+string(b))
		f.mu.Unlock()
		automationWriteJSON(w, []any{})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *automationFake) store(kind, id string, raw []byte) {
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		f.t.Fatalf("bad config JSON %s: %v", raw, err)
	}
	f.configs[kind][id] = c
	f.raw[kind][id] = raw
}

func automationWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *automationFake) states() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for id, c := range f.configs["automation"] {
		out = append(out, map[string]any{
			"entity_id": "automation." + automationSlug(c["alias"].(string)), "state": "on",
			"attributes": map[string]any{"id": id, "friendly_name": c["alias"], "last_triggered": "2026-09-29T18:30:00+00:00", "current": 0},
		})
	}
	for key, c := range f.configs["script"] {
		out = append(out, map[string]any{
			"entity_id": "script." + key, "state": "off",
			"attributes": map[string]any{"friendly_name": c["alias"], "last_triggered": nil},
		})
	}
	for id, c := range f.configs["scene"] {
		out = append(out, map[string]any{
			"entity_id": "scene." + automationSlug(c["name"].(string)), "state": "unknown",
			"attributes": map[string]any{"id": id, "friendly_name": c["name"], "entity_id": []any{"light.tv"}},
		})
	}
	return append(out, f.extra...)
}

func (f *automationFake) wsCommands(typ string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, m := range f.ws {
		if m["type"] == typ {
			out = append(out, m)
		}
	}
	return out
}

func (f *automationFake) serveWS(w http.ResponseWriter, r *http.Request) {
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
		f.mu.Lock()
		f.ws = append(f.ws, msg)
		f.mu.Unlock()
		id := msg["id"]
		result, errCode := f.answer(msg)
		if errCode != "" {
			send(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": errCode, "message": result}})
			continue
		}
		send(map[string]any{"id": id, "type": "result", "success": true, "result": result})
	}
}

func (f *automationFake) answer(msg map[string]any) (any, string) {
	switch msg["type"] {
	case "supported_features":
		return nil, ""
	case "config/entity_registry/list":
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{map[string]any{"id": "reg-light", "entity_id": "light.porch", "platform": "demo", "unique_id": "p"}}
		for key := range f.configs["script"] {
			out = append(out, map[string]any{"id": "reg-" + key, "entity_id": "script." + key, "platform": "script", "unique_id": key})
		}
		return out, ""
	case "config/entity_registry/get":
		eid := msg["entity_id"].(string)
		key := strings.TrimPrefix(eid, "script.")
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.configs["script"][key]; ok {
			return map[string]any{"id": "reg-" + key, "entity_id": eid, "platform": "script", "unique_id": key}, ""
		}
		return "Entity not found", "not_found"
	case "validate_config":
		out := map[string]any{}
		for _, sec := range []string{"triggers", "conditions", "actions"} {
			v, ok := msg[sec]
			if !ok {
				continue
			}
			b, _ := json.Marshal(v)
			if strings.Contains(string(b), "targett") {
				out[sec] = map[string]any{"valid": false, "error": "not a valid option, did you mean 'target'? at '[0].targett'"}
			} else {
				out[sec] = map[string]any{"valid": true, "error": nil}
			}
		}
		return out, ""
	case "trace/list":
		if msg["domain"] != "automation" {
			return []any{}, ""
		}
		return []any{
			map[string]any{"domain": "automation", "item_id": "1700000000001", "run_id": "run-old", "state": "stopped",
				"script_execution": "failed_conditions", "last_step": "condition/0",
				"timestamp": map[string]any{"start": "2026-09-29T10:00:00+00:00", "finish": "2026-09-29T10:00:00.004+00:00"},
				"trigger":   "sunset"},
			map[string]any{"domain": "automation", "item_id": "1700000000001", "run_id": "run-new", "state": "stopped",
				"script_execution": "error", "last_step": "action/1", "error": "Action nonexistent.service not found",
				"timestamp": map[string]any{"start": "2026-09-30T10:00:00+00:00", "finish": "2026-09-30T10:00:00.011+00:00"},
				"trigger":   "sunset"},
		}, ""
	case "trace/get":
		if msg["run_id"] != "run-new" {
			return "The trace could not be found", "not_found"
		}
		return automationSampleTrace(), ""
	case "blueprint/list":
		if msg["domain"] == "script" {
			return map[string]any{"broken.yaml": map[string]any{"error": "Invalid blueprint: missing domain"}}, ""
		}
		return map[string]any{"homeassistant/motion_light.yaml": map[string]any{"metadata": map[string]any{
			"name": "Motion-activated Light", "description": "Turn on a light when motion is detected.", "domain": "automation",
			"input": map[string]any{
				"motion_entity":  map[string]any{"name": "Motion Sensor", "selector": map[string]any{"entity": map[string]any{}}},
				"no_motion_wait": map[string]any{"name": "Wait time", "default": 120, "selector": map[string]any{"number": map[string]any{}}},
				"advanced": map[string]any{"name": "Advanced", "input": map[string]any{
					"extra": map[string]any{"name": "Extra", "default": "", "selector": map[string]any{"text": map[string]any{}}},
				}},
			},
		}}}, ""
	case "blueprint/import":
		return map[string]any{
			"suggested_filename": "someone/fancy", "raw_data": "blueprint:\n  name: Fancy\n  domain: automation\n",
			"blueprint":         map[string]any{"metadata": map[string]any{"name": "Fancy", "domain": "automation", "input": map[string]any{}}},
			"validation_errors": nil, "exists": false,
		}, ""
	case "blueprint/save":
		return map[string]any{"overrides_existing": false}, ""
	case "blueprint/delete":
		return nil, ""
	case "device_automation/trigger/list":
		return []any{map[string]any{"platform": "device", "type": "turned_on", "device_id": "dev1", "entity_id": "reg-light", "domain": "light", "metadata": map[string]any{"secondary": false}}}, ""
	case "device_automation/condition/list", "device_automation/action/list":
		return []any{}, ""
	case "device_automation/trigger/capabilities":
		return map[string]any{"extra_fields": []any{map[string]any{"type": "positive_time_period_dict", "name": "for", "required": false, "optional": true}}}, ""
	case "search/related":
		return map[string]any{"device": []any{"dev1"}, "automation": []any{"automation.porch_light_at_sunset"}}, ""
	case "config/device_registry/list":
		return []any{map[string]any{"id": "dev1", "name": "Porch Light"}}, ""
	}
	f.t.Errorf("unexpected websocket command %v", msg["type"])
	return "unknown command", "unknown_command"
}

func automationSampleTrace() map[string]any {
	return map[string]any{
		"domain": "automation", "item_id": "1700000000001", "run_id": "run-new", "state": "stopped",
		"script_execution": "error", "last_step": "action/1", "error": "Action nonexistent.service not found",
		"timestamp": map[string]any{"start": "2026-09-30T10:00:00+00:00", "finish": "2026-09-30T10:00:00.011+00:00"},
		"trigger":   "sunset",
		"trace": map[string]any{
			"action/1": []any{map[string]any{"path": "action/1", "timestamp": "2026-09-30T10:00:00.010+00:00",
				"error":  "Action nonexistent.service not found",
				"result": map[string]any{"params": map[string]any{"domain": "nonexistent", "service": "service", "service_data": map[string]any{}, "target": map[string]any{}}, "running_script": false}}},
			"trigger/0": []any{map[string]any{"path": "trigger/0", "timestamp": "2026-09-30T10:00:00.001+00:00",
				"changed_variables": map[string]any{"this": map[string]any{"huge": strings.Repeat("x", 5000)},
					"trigger": map[string]any{"platform": "sun", "description": "sunset", "id": "0", "idx": "0"}}}},
			"action/0": []any{map[string]any{"path": "action/0", "timestamp": "2026-09-30T10:00:00.005+00:00",
				"changed_variables": map[string]any{"context": map[string]any{"id": "c"}, "brightness": 80}}},
			"condition/0": []any{map[string]any{"path": "condition/0", "timestamp": "2026-09-30T10:00:00.002+00:00",
				"result": map[string]any{"result": true, "entities": []any{}}}},
		},
		"config": map[string]any{
			"triggers":   []any{map[string]any{"trigger": "sun", "event": "sunset"}},
			"conditions": []any{map[string]any{"condition": "state", "entity_id": "input_boolean.away", "state": "off"}},
			"actions":    []any{map[string]any{"variables": map[string]any{"brightness": 80}}, map[string]any{"action": "nonexistent.service"}},
		},
	}
}

// automationConnect wires a client to the server over the in-memory
// transport, which exercises the real tool schemas and dispatch.
func automationConnect(t *testing.T, ha *homeassistant.Client, readOnly bool) *mcp.ClientSession {
	t.Helper()
	s := New(ha, oauth.NewSigner("secret"), "https://ha.example", "test", readOnly)
	s.now = func() time.Time { return automationTestNow }
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

func automationFakeSession(t *testing.T, readOnly bool) (*mcp.ClientSession, *automationFake) {
	t.Helper()
	f := newAutomationFake(t)
	ha, err := homeassistant.New(f.srv.URL, "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ha.Close() })
	return automationConnect(t, ha, readOnly), f
}

// automationCall returns the tool's text and whether it reported an error.
func automationCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func automationMustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	out, isErr := automationCall(t, cs, name, args)
	if isErr {
		t.Fatalf("%s(%v) failed: %s", name, args, out)
	}
	return out
}

func automationWant(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func automationToolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

var (
	automationReadTools  = []string{"ha_get_automation", "ha_traces", "ha_validate_config", "ha_list_device_automations", "ha_find_related", "ha_list_blueprints"}
	automationWriteTools = []string{"ha_manage_automation", "ha_manage_blueprint"}
)

func TestAutomationToolsReadOnlyHidesWriteTools(t *testing.T) {
	rw, _ := automationFakeSession(t, false)
	names := automationToolNames(t, rw)
	for _, n := range append(slices.Clone(automationReadTools), automationWriteTools...) {
		if !slices.Contains(names, n) {
			t.Errorf("%s missing in read-write mode", n)
		}
	}
	ro, _ := automationFakeSession(t, true)
	names = automationToolNames(t, ro)
	for _, n := range automationReadTools {
		if !slices.Contains(names, n) {
			t.Errorf("%s missing in read-only mode", n)
		}
	}
	for _, n := range automationWriteTools {
		if slices.Contains(names, n) {
			t.Errorf("%s offered in read-only mode", n)
		}
	}
}

func TestAutomationGetListsAndReadsConfig(t *testing.T) {
	cs, _ := automationFakeSession(t, false)

	out := automationMustCall(t, cs, "ha_get_automation", nil)
	automationWant(t, out, "3 automations:",
		`automation.porch_light_at_sunset [id 1700000000001] "Porch light at sunset" on, last ran 2026-09-29 18:30:00 UTC`,
		"automation.no_id [no config id: not editable here]")

	out = automationMustCall(t, cs, "ha_get_automation", map[string]any{"query": "porch"})
	automationWant(t, out, "1 automation:")

	out = automationMustCall(t, cs, "ha_get_automation", map[string]any{"entity_id": "automation.porch_light_at_sunset"})
	automationWant(t, out, "# automation.porch_light_at_sunset (id 1700000000001): on",
		"id: \"1700000000001\"\nalias: Porch light at sunset\n",
		"triggers:\n  - trigger: sun\n    event: sunset\n")
	if strings.Index(out, "triggers:") > strings.Index(out, "actions:") {
		t.Errorf("triggers should come before actions:\n%s", out)
	}

	out = automationMustCall(t, cs, "ha_get_automation", map[string]any{"id": "morning"})
	automationWant(t, out, "# script.morning (id morning)", "sequence:\n  - delay: 1")

	out, isErr := automationCall(t, cs, "ha_get_automation", map[string]any{"id": "automation.yaml_only"})
	if !isErr {
		t.Fatalf("expected an error for a YAML-only automation: %s", out)
	}
	automationWant(t, out, "not stored in automations.yaml")

	out, isErr = automationCall(t, cs, "ha_get_automation", map[string]any{"entity_id": "automation.no_id"})
	if !isErr {
		t.Fatalf("expected an error for an automation without id: %s", out)
	}
	automationWant(t, out, "has no config id")

	out = automationMustCall(t, cs, "ha_get_automation", map[string]any{"kind": "scene"})
	automationWant(t, out, `scene.movie [id 1700000000002] "Movie" 1 entity, never activated`)
}

func TestAutomationTraces(t *testing.T) {
	cs, _ := automationFakeSession(t, false)

	out := automationMustCall(t, cs, "ha_traces", map[string]any{"id": "1700000000001"})
	automationWant(t, out, "Runs of automation.porch_light_at_sunset (id 1700000000001), newest first:",
		"run-new 2026-09-30 10:00:00 UTC failed with an error in 11ms at action/1: Action nonexistent.service not found; trigger: sunset",
		"run-old 2026-09-29 10:00:00 UTC stopped: conditions not met in 4ms at condition/0")
	if strings.Index(out, "run-new") > strings.Index(out, "run-old") {
		t.Errorf("runs are not newest first:\n%s", out)
	}

	out = automationMustCall(t, cs, "ha_traces", nil)
	automationWant(t, out, "Recent automation runs", "run-new 2026-09-30 10:00:00 UTC automation.porch_light_at_sunset [id 1700000000001]")

	out = automationMustCall(t, cs, "ha_traces", map[string]any{"entity_id": "automation.porch_light_at_sunset", "run_id": "run-new"})
	automationWant(t, out,
		"error: Action nonexistent.service not found",
		"- trigger/0 (sun): sunset\n- condition/0 (state input_boolean.away): result true\n- action/0 (variables): set brightness=80\n- action/1: nonexistent.service; ERROR: Action nonexistent.service not found")
	if strings.Contains(out, "xxxx") {
		t.Errorf("the this variable leaked into the summary:\n%s", out)
	}

	out = automationMustCall(t, cs, "ha_traces", map[string]any{"id": "1700000000001", "run_id": "run-new", "step": "trigger/0"})
	automationWant(t, out, `"changed_variables"`, "xxxx")

	out, isErr := automationCall(t, cs, "ha_traces", map[string]any{"id": "1700000000001", "run_id": "gone"})
	if !isErr {
		t.Fatalf("expected error: %s", out)
	}
	automationWant(t, out, "HA keeps only the last few")
}

func TestAutomationValidate(t *testing.T) {
	cs, f := automationFakeSession(t, false)
	out := automationMustCall(t, cs, "ha_validate_config", map[string]any{
		"triggers": "- trigger: state\n  entity_id: light.porch\n",
		"actions":  `[{"action": "light.turn_on", "targett": {}}]`,
	})
	automationWant(t, out, "Invalid.", "triggers: valid", "actions: invalid: not a valid option, did you mean 'target'?")
	sent := f.wsCommands("validate_config")
	if len(sent) != 1 {
		t.Fatalf("expected one validate_config, got %d", len(sent))
	}
	if trig, ok := sent[0]["triggers"].([]any); !ok || trig[0].(map[string]any)["entity_id"] != "light.porch" {
		t.Errorf("YAML triggers were not sent as a list: %v", sent[0]["triggers"])
	}

	out = automationMustCall(t, cs, "ha_validate_config", map[string]any{"config": map[string]any{"alias": "x", "sequence": []any{map[string]any{"delay": 1}}}})
	automationWant(t, out, "Valid.", "actions: valid")
}

func TestAutomationSaveValidatesDiffsAndCreates(t *testing.T) {
	cs, f := automationFakeSession(t, false)

	out, isErr := automationCall(t, cs, "ha_manage_automation", map[string]any{
		"action": "save", "id": "1700000000001",
		"config": "alias: Porch\ntriggers:\n  - trigger: sun\n    event: sunset\nactions:\n  - action: light.turn_on\n    targett: {}\n",
	})
	if !isErr {
		t.Fatalf("expected invalid config to be refused: %s", out)
	}
	automationWant(t, out, "was not saved", "actions: invalid")
	if f.configs["automation"]["1700000000001"]["alias"] != "Porch light at sunset" {
		t.Fatal("an invalid config was saved")
	}

	out = automationMustCall(t, cs, "ha_manage_automation", map[string]any{
		"action": "save", "entity_id": "automation.porch_light_at_sunset",
		"config": "alias: Porch light at sunset\ntriggers:\n  - trigger: sun\n    event: sunset\n    offset: \"-00:10:00\"\nactions:\n  - action: light.turn_on\n    target:\n      entity_id: light.porch\nmode: single\n",
	})
	automationWant(t, out, "Updated automation.porch_light_at_sunset (id 1700000000001).", "Changes (- before, + after):", `+     offset: "-00:10:00"`)
	if strings.Contains(out, "- alias") {
		t.Errorf("unchanged lines reported as removed:\n%s", out)
	}
	f.mu.Lock()
	stored := string(f.raw["automation"]["1700000000001"])
	f.mu.Unlock()
	if want := `{"id":"1700000000001","alias":"Porch light at sunset","triggers":[{"trigger":"sun","event":"sunset","offset":"-00:10:00"}],`; !strings.HasPrefix(stored, want) {
		t.Errorf("the save did not keep the written key order with the id first:\n%s", stored)
	}

	out = automationMustCall(t, cs, "ha_manage_automation", map[string]any{
		"action": "save",
		"config": map[string]any{"alias": "Hall motion", "triggers": []any{map[string]any{"trigger": "state", "entity_id": "binary_sensor.hall"}}, "actions": []any{}},
	})
	id := "1790769600000"
	automationWant(t, out, "Created automation.hall_motion (id "+id+").")
	if f.configs["automation"][id]["id"] != id {
		t.Errorf("new automation not stored under the generated id: %v", f.configs["automation"][id])
	}

	out = automationMustCall(t, cs, "ha_manage_automation", map[string]any{
		"action": "save", "kind": "script", "config": "alias: Good Night\nsequence:\n  - action: light.turn_off\n",
	})
	automationWant(t, out, "Created script.good_night (id good_night).")

	out, isErr = automationCall(t, cs, "ha_manage_automation", map[string]any{
		"action": "save", "kind": "script", "config": "alias: Good Night\nsequence: []\n",
	})
	if !isErr {
		t.Fatalf("expected a clash with the existing script key: %s", out)
	}
	automationWant(t, out, `pass id:"good_night" to replace it`)

	out, isErr = automationCall(t, cs, "ha_manage_automation", map[string]any{
		"action": "save", "kind": "scene", "config": map[string]any{"name": "Bad", "entities": "nope"},
	})
	if !isErr {
		t.Fatalf("expected HA's rejection to surface: %s", out)
	}
	automationWant(t, out, "expected dict at 'entities'", "Nothing was saved")
}

func TestAutomationDeleteNeedsConfirm(t *testing.T) {
	cs, f := automationFakeSession(t, false)
	out, isErr := automationCall(t, cs, "ha_manage_automation", map[string]any{"action": "delete", "id": "1700000000001"})
	if !isErr {
		t.Fatalf("delete without confirm succeeded: %s", out)
	}
	if _, ok := f.configs["automation"]["1700000000001"]; !ok {
		t.Fatal("deleted without confirm")
	}
	out = automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "delete", "id": "1700000000001", "confirm": true})
	automationWant(t, out, "Deleted automation.porch_light_at_sunset (id 1700000000001). Its config was:", "alias: Porch light at sunset")
	if _, ok := f.configs["automation"]["1700000000001"]; ok {
		t.Fatal("not deleted")
	}
}

func TestAutomationControlActionsCallServices(t *testing.T) {
	cs, f := automationFakeSession(t, false)
	automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "disable", "id": "1700000000001"})
	out := automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "trigger", "entity_id": "automation.porch_light_at_sunset", "skip_condition": false})
	automationWant(t, out, "Triggered automation.porch_light_at_sunset.", "Run run-new failed with an error")
	automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "run", "id": "morning", "kind": "script", "variables": map[string]any{"who": "kaan"}})
	automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "activate", "entity_id": "scene.movie"})

	want := []string{
		`automation.turn_off {"entity_id":["automation.porch_light_at_sunset"]}`,
		`automation.trigger {"entity_id":["automation.porch_light_at_sunset"],"skip_condition":false}`,
		`script.turn_on {"entity_id":["script.morning"],"variables":{"who":"kaan"}}`,
		`scene.turn_on {"entity_id":["scene.movie"]}`,
	}
	if !slices.Equal(f.services, want) {
		t.Errorf("service calls:\n got %q\nwant %q", f.services, want)
	}

	out, isErr := automationCall(t, cs, "ha_manage_automation", map[string]any{"action": "run", "entity_id": "automation.porch_light_at_sunset"})
	if !isErr {
		t.Fatalf("run on an automation should fail: %s", out)
	}
}

func TestAutomationDeviceRelatedBlueprints(t *testing.T) {
	cs, f := automationFakeSession(t, false)

	out := automationMustCall(t, cs, "ha_list_device_automations", map[string]any{"device_id": "dev1", "capabilities": true})
	automationWant(t, out, "triggers:\n- {platform: device, entity_id: reg-light, device_id: dev1, domain: light, type: turned_on}  # light.porch\n  extra fields: for (positive_time_period_dict, optional)",
		"conditions:\n(none)")
	if strings.Contains(out, "metadata") {
		t.Errorf("metadata should be dropped:\n%s", out)
	}

	out = automationMustCall(t, cs, "ha_find_related", map[string]any{"item_type": "automation", "item_id": "1700000000001"})
	automationWant(t, out, "Related to automation automation.porch_light_at_sunset:", `device: dev1 "Porch Light"`)
	if got := f.wsCommands("search/related"); got[0]["item_id"] != "automation.porch_light_at_sunset" {
		t.Errorf("config id not resolved to entity id: %v", got[0])
	}

	out = automationMustCall(t, cs, "ha_list_blueprints", nil)
	automationWant(t, out, `- homeassistant/motion_light.yaml "Motion-activated Light": Turn on a light when motion is detected.`,
		"inputs: extra (text, default \"\"); motion_entity \"Motion Sensor\" (entity, required); no_motion_wait \"Wait time\" (number, default 120)",
		"- broken.yaml: failed to load: Invalid blueprint: missing domain")

	out = automationMustCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "import", "url": "https://example.com/fancy.yaml"})
	automationWant(t, out, `Installed the automation blueprint "Fancy" at someone/fancy.yaml.`, "use_blueprint: {path: someone/fancy.yaml")
	save := f.wsCommands("blueprint/save")
	if len(save) != 1 || save[0]["path"] != "someone/fancy.yaml" || save[0]["source_url"] != "https://example.com/fancy.yaml" {
		t.Errorf("unexpected save: %v", save)
	}

	if out, isErr := automationCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "delete", "domain": "automation", "path": "someone/fancy.yaml"}); !isErr {
		t.Fatalf("delete without confirm succeeded: %s", out)
	}
	automationMustCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "delete", "domain": "automation", "path": "someone/fancy.yaml", "confirm": true})
	if len(f.wsCommands("blueprint/delete")) != 1 {
		t.Error("blueprint not deleted")
	}
}

func TestAutomationDiff(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	after := "a\nb\nc\nD\ne\nf\ng\nh\ni\nj\nk\n"
	got := automationDiff(before, after)
	want := "  b\n  c\n- d\n+ D\n  e\n  f\n…\n  i\n  j\n+ k"
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if automationDiff(before, before) != "" {
		t.Error("identical input should give no diff")
	}
}

func TestAutomationNodeAt(t *testing.T) {
	cfg := map[string]any{
		"actions": []any{
			map[string]any{"choose": []any{map[string]any{
				"conditions": []any{map[string]any{"condition": "state", "entity_id": "sun.sun"}},
				"sequence":   []any{map[string]any{"alias": "Lights on", "action": "light.turn_on"}},
			}}},
			map[string]any{"if": []any{map[string]any{"condition": "template"}}, "then": []any{map[string]any{"delay": 1}}},
			map[string]any{"parallel": []any{map[string]any{"action": "notify.me"}}},
		},
	}
	for path, want := range map[string]string{
		"action/0":                          "choose",
		"action/0/choose/0/conditions/0":    "state sun.sun",
		"action/0/choose/0/sequence/0":      `"Lights on"`,
		"action/1/if/condition/0":           "template",
		"action/1/then/0":                   "delay",
		"action/2/parallel/0/sequence/0":    "notify.me",
		"action/9":                          "",
		"action/0/choose/0/sequence/0/nope": "",
	} {
		if got := automationDescribeStep(automationNodeAt(cfg, path)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

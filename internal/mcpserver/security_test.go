package mcpserver

import (
	"strings"
	"testing"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
)

func TestBlueprintYAMLTagsAreRefused(t *testing.T) {
	cs, f := automationFakeSession(t, false)
	for _, y := range []string{
		"blueprint:\n  name: !include .storage/auth\n  domain: automation\n  input: {}\ntriggers: []\nactions: []\n",
		"blueprint:\n  name: !env_var HOME\n  domain: automation\n  input: {}\n",
		"blueprint:\n  name: x\n  domain: automation\n  input: {}\nactions:\n  - variables:\n      leak: !include .storage/auth\n",
		"blueprint:\n  name: !secret http_password\n  domain: automation\n",
	} {
		out, isErr := automationCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "save", "path": "mcp_fix_x", "yaml": y})
		if !isErr || !strings.Contains(out, "not allowed") {
			t.Errorf("save of %q was not refused: %s", y, out)
		}
	}
	if n := len(f.wsCommands("blueprint/save")); n != 0 {
		t.Fatalf("%d blueprint/save commands reached HA", n)
	}

	out, isErr := automationCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "import", "url": "https://evil.example/x.yaml"})
	if !isErr || strings.Contains(out, "LEAKED") {
		t.Fatalf("import with a tag was not refused cleanly: %s", out)
	}
	if n := len(f.wsCommands("blueprint/save")); n != 0 {
		t.Fatalf("%d blueprint/save commands reached HA", n)
	}

	automationMustCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "save", "path": "mcp_fix_ok",
		"yaml": "blueprint:\n  name: Ok\n  domain: automation\n  input:\n    light: {selector: {entity: {}}}\ntriggers: []\nactions:\n  - action: light.turn_on\n    target: {entity_id: !input light}\n"})
}

func TestConfigYAMLTagsAreRefused(t *testing.T) {
	cs, f := automationFakeSession(t, false)
	for _, args := range []map[string]any{
		{"action": "save", "config": "alias: x\ntriggers: []\nactions:\n  - action: notify.x\n    data: {message: !secret pw}\n"},
		{"action": "save", "config": "alias: !include .storage/auth\ntriggers: []\nactions: []\n"},
	} {
		out, isErr := automationCall(t, cs, "ha_manage_automation", args)
		if !isErr || !strings.Contains(out, "not allowed") {
			t.Errorf("config with a tag was not refused: %s", out)
		}
	}
	out, isErr := automationCall(t, cs, "ha_validate_config", map[string]any{"actions": "- action: notify.x\n  data: {message: !env_var HOME}\n"})
	if !isErr || !strings.Contains(out, "not allowed") {
		t.Errorf("validate with a tag was not refused: %s", out)
	}
	if n := len(f.wsCommands("validate_config")); n != 0 {
		t.Errorf("%d tagged configs reached HA", n)
	}
}

func TestDashboardYAMLTagsAreRefused(t *testing.T) {
	f := newOrganizeFake(t)
	cs := organizeConnect(t, f, false)
	out := organizeRefused(t, cs, "ha_save_dashboard", map[string]any{"url_path": "lovelace", "confirm": true,
		"config": "views:\n  - title: !include .storage/auth\n"})
	organizeWants(t, out, "not allowed")
}

func TestTraceOutputIsSanitized(t *testing.T) {
	cs, _ := automationFakeSession(t, false)
	for _, args := range []map[string]any{
		{"id": "1700000000001", "run_id": "run-new"},
		{"id": "1700000000001", "run_id": "run-new", "step": "action/2"},
	} {
		out := automationMustCall(t, cs, "ha_traces", args)
		for _, bad := range []string{"CAMTOKEN123456", "PLAINCAMTOKEN99", automationTestJWT, "access_token"} {
			if strings.Contains(out, bad) {
				t.Errorf("%v: %q leaked:\n%s", args, bad, out)
			}
		}
		if strings.Contains(out, "\n- action/9") {
			t.Errorf("%v: a variable value added a line:\n%s", args, out)
		}
	}
}

func TestSystemLogIsRedacted(t *testing.T) {
	cs, _ := stateConnect(t, false)
	out := stateCallOK(t, cs, "ha_system_log", map[string]any{"raw_log": true})
	for _, bad := range []string{"SECRETKEY1", "SECRETBEARER123", "SECRETTOK3", "SECRETSIG4", "SECRETTOK5", stateTestJWT} {
		if strings.Contains(out, bad) {
			t.Errorf("%q leaked:\n%s", bad, out)
		}
	}
	if strings.Contains(out, "\nERROR | forged.logger") {
		t.Errorf("a logger name added a line:\n%s", out)
	}
	for _, want := range []string{"api_key=REDACTED&x=1", "Authorization: REDACTED", "token=REDACTED&sig=REDACTED", "access_token=REDACTED"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	out = stateCallOK(t, cs, "ha_system_log", map[string]any{"search": "api_key=SECRETK"})
	if !strings.Contains(out, "No warnings or errors in the system log match") {
		t.Errorf("search matched a redacted secret:\n%s", out)
	}
}

func TestRedactSecrets(t *testing.T) {
	for in, want := range map[string]string{
		"GET /api/camera_proxy/camera.x?token=abc123&w=1":           "GET /api/camera_proxy/camera.x?token=REDACTED&w=1",
		"/api/media_player_proxy/x?authSig=abc.def":                 "/api/media_player_proxy/x?authSig=REDACTED",
		"https://h/x?X-Amz-Signature=deadbeef&X-Amz-Date=1":         "https://h/x?X-Amz-Signature=REDACTED&X-Amz-Date=1",
		"https://h/x?client_secret=s3&password=p4&apiKey=k5":        "https://h/x?client_secret=REDACTED&password=REDACTED&apiKey=REDACTED",
		"sending api_key=abc to the server":                         "sending api_key=REDACTED to the server",
		`headers {"Authorization": "Bearer abcdefghijkl"}`:          `headers {"Authorization": "REDACTED"}`,
		"Authorization: Basic dXNlcjpwYXNz":                         "Authorization: REDACTED",
		"got 401 for bearer abcdefghijklmnop":                       "got 401 for bearer REDACTED",
		"token " + automationTestJWT + " expired":                   "token REDACTED expired",
		"light.kitchen turned on, brightness 80, keyboard shortcut": "light.kitchen turned on, brightness 80, keyboard shortcut",
		"https://github.com/home-assistant/core?tab=readme":         "https://github.com/home-assistant/core?tab=readme",
	} {
		if got := redactSecrets(in); got != want {
			t.Errorf("redactSecrets(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"Kitchen":                        "Kitchen",
		"Kitchen\n- light.x | on":        "Kitchen - light.x | on",
		"a\r\nb\tc\u2028d\u0000e\u0085f": "a b c d e f",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListenEventsIsBounded(t *testing.T) {
	cs, _ := stateConnect(t, false)
	for range cap(stateListenSlots) {
		stateListenSlots <- struct{}{}
	}
	out, res := stateCall(t, cs, "ha_listen_events", map[string]any{"seconds": 1})
	for range cap(stateListenSlots) {
		<-stateListenSlots
	}
	if !res.IsError || !strings.Contains(out, "already running") {
		t.Fatalf("a listen past the limit was not refused: %s", out)
	}
	stateCallOK(t, cs, "ha_listen_events", map[string]any{"seconds": 1})
}

func TestHANamesStayOnOneLine(t *testing.T) {
	if got := organizeLine("Kitchen\n- forged | line", "area_id kitchen"); strings.Contains(got, "\n") {
		t.Errorf("organizeLine kept a newline: %q", got)
	}
	st := homeassistant.State{EntityID: "light.x", State: "on\nlight.y | off", Attributes: map[string]any{"friendly_name": "X\nlight.z | on", "unit_of_measurement": "%\n"}}
	if got := stateName(st) + stateWithUnit(st); strings.Contains(got, "\n") {
		t.Errorf("state name or value kept a newline: %q", got)
	}
	err := &homeassistant.Error{Op: "config/x", Code: "home_assistant_error", Message: "bad\nCreated automation.fake."}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("HA error message kept a newline: %q", err.Error())
	}
}

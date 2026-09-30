package mcpserver

import (
	"strings"
	"testing"
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

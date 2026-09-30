//go:build integration

package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
)

// These run against a real Home Assistant (HA_URL, HA_TOKEN). Others may use
// the same instance, so everything created here is named mcp_auto_ and removed
// again.

func automationIntegrationClient(t *testing.T) *homeassistant.Client {
	t.Helper()
	url, token := os.Getenv("HA_URL"), os.Getenv("HA_TOKEN")
	if url == "" || token == "" {
		t.Skip("HA_URL and HA_TOKEN must be set")
	}
	c, err := homeassistant.New(url, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAutomationIntegration(t *testing.T) {
	ha := automationIntegrationClient(t)
	cs := automationConnect(t, ha, false)
	suffix := fmt.Sprint(time.Now().UnixNano() % 1_000_000_000)
	autoID := "mcp_auto_it_" + suffix
	scriptID := "mcp_auto_script_" + suffix
	sceneID := "mcp_auto_scene_" + suffix
	bpPath := "mcp_auto_bp/it_" + suffix + ".yaml"
	bpAutoID := "mcp_auto_bpuse_" + suffix
	event := "mcp_auto_event_" + suffix
	t.Cleanup(func() {
		ctx := context.Background()
		_ = ha.DeleteConfigItem(ctx, homeassistant.KindAutomation, autoID)
		_ = ha.DeleteConfigItem(ctx, homeassistant.KindAutomation, bpAutoID)
		_ = ha.DeleteConfigItem(ctx, homeassistant.KindScript, scriptID)
		_ = ha.DeleteConfigItem(ctx, homeassistant.KindScene, sceneID)
		_ = ha.DeleteBlueprint(ctx, "automation", bpPath)
		_ = ha.DeleteBlueprint(ctx, "automation", "mcp_auto_bp/imported_"+suffix+".yaml")
	})
	show := func(name string, args map[string]any) string {
		t.Helper()
		out := automationMustCall(t, cs, name, args)
		t.Logf("%s %v\n%s\n", name, args, out)
		return out
	}
	showErr := func(name string, args map[string]any) string {
		t.Helper()
		out, isErr := automationCall(t, cs, name, args)
		if !isErr {
			t.Fatalf("%s(%v) should have failed:\n%s", name, args, out)
		}
		t.Logf("%s %v (error)\n%s\n", name, args, out)
		return out
	}

	automationWant(t, show("ha_get_automation", nil), "[id 1700000000001]")
	automationWant(t, show("ha_get_automation", map[string]any{"id": "1700000000001"}), "alias: Test toggle kitchen light", "event_type: mcp_test_event")
	automationWant(t, show("ha_get_automation", map[string]any{"kind": "script"}), "script.seed_script [id seed_script]")
	automationWant(t, show("ha_get_automation", map[string]any{"entity_id": "script.seed_script"}), "sequence:")

	cfg := fmt.Sprintf(`alias: mcp_auto integration %[1]s
description: Created by the automation tool integration test
triggers:
  - trigger: event
    event_type: %[2]s
conditions:
  - condition: template
    value_template: "{{ trigger.event.data.go | default(false) }}"
actions:
  - variables:
      level: 42
  - choose:
      - conditions:
          - condition: template
            value_template: "{{ level > 10 }}"
        sequence:
          - alias: Announce
            event: %[2]s_done
            event_data:
              level: "{{ level }}"
  - action: nonexistent.service
mode: single
`, suffix, event)
	showErr("ha_manage_automation", map[string]any{"action": "save", "id": autoID,
		"config": strings.Replace(cfg, "event_type:", "event_typo:", 1)})
	automationWant(t, show("ha_manage_automation", map[string]any{"action": "save", "id": autoID, "config": cfg}), "Created automation.")
	automationWant(t, show("ha_manage_automation", map[string]any{"action": "save", "id": autoID,
		"config": strings.Replace(cfg, "level: 42", "level: 43", 1)}), "- ", "+ ", "level: 43")
	got := show("ha_get_automation", map[string]any{"id": autoID})
	automationWant(t, got, "level: 43", "value_template: '{{ trigger.event.data.go | default(false) }}'")

	automationWant(t, show("ha_manage_automation", map[string]any{"action": "disable", "id": autoID}), "is now off")
	automationWant(t, show("ha_manage_automation", map[string]any{"action": "enable", "id": autoID}), "is now on")
	automationWant(t, show("ha_manage_automation", map[string]any{"action": "trigger", "id": autoID,
		"variables": map[string]any{"who": "test"}}), "failed with an error", "nonexistent.service")

	if _, err := ha.FireEvent(context.Background(), event, map[string]any{"go": false}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	list := show("ha_traces", map[string]any{"id": autoID})
	automationWant(t, list, "stopped: conditions not met", "failed with an error")
	skipped := strings.Fields(strings.Split(list, "\n")[1])[0]
	automationWant(t, show("ha_traces", map[string]any{"id": autoID, "run_id": skipped}),
		`trigger/0: event '`+event+`' data {"go":false}`, "condition/0 (template): result false")
	runID := strings.Fields(strings.Split(list, "\n")[2])[0]
	trace := show("ha_traces", map[string]any{"id": autoID, "run_id": runID})
	automationWant(t, trace, "action/1/choose/0/sequence/0 (\"Announce\")", "ERROR: Action nonexistent.service not found", "set level=43")
	show("ha_traces", map[string]any{"id": autoID, "run_id": runID, "step": "action/1/choose/0/sequence/0"})
	show("ha_traces", nil)

	show("ha_validate_config", map[string]any{"config": cfg})
	automationWant(t, show("ha_validate_config", map[string]any{
		"triggers":   `{"trigger": "state", "entity_idd": "light.kitchen_lights"}`,
		"conditions": "- condition: state\n",
		"actions":    []any{map[string]any{"action": "light.turn_on", "target": map[string]any{"entity_id": "light.kitchen_lights"}}},
	}), "Invalid.", "did you mean 'entity_id'", "actions: valid")

	automationWant(t, show("ha_manage_automation", map[string]any{"action": "save", "kind": "script", "id": scriptID,
		"config": "alias: mcp_auto script\nfields:\n  who:\n    description: name\nsequence:\n  - event: " + event + "_script\n    event_data:\n      who: \"{{ who }}\"\n"}), "Created script.")
	automationWant(t, show("ha_manage_automation", map[string]any{"action": "run", "id": scriptID, "kind": "script", "variables": map[string]any{"who": "it"}}), "Started script.")
	time.Sleep(300 * time.Millisecond)
	slist := show("ha_traces", map[string]any{"kind": "script", "id": scriptID})
	srun := strings.Fields(strings.Split(slist, "\n")[1])[0]
	automationWant(t, show("ha_traces", map[string]any{"kind": "script", "id": scriptID, "run_id": srun}), "sequence/0")

	automationWant(t, show("ha_manage_automation", map[string]any{"action": "save", "kind": "scene", "id": sceneID,
		"config": map[string]any{"name": "mcp_auto scene " + suffix, "entities": map[string]any{"input_boolean.mcp_auto_missing": "on"}}}), "Created scene.")
	show("ha_get_automation", map[string]any{"kind": "scene", "query": "mcp_auto"})
	show("ha_manage_automation", map[string]any{"action": "activate", "id": sceneID, "kind": "scene"})

	reg, err := ha.GetEntityRegistryEntry(context.Background(), "light.kitchen_lights")
	if err != nil || reg.DeviceID == nil {
		t.Fatalf("kitchen light device: %v", err)
	}
	automationWant(t, show("ha_list_device_automations", map[string]any{"device_id": *reg.DeviceID, "capabilities": true}), "turned_on", "# light.kitchen_lights", "brightness_pct")
	showErr("ha_list_device_automations", map[string]any{"device_id": "nope"})
	automationWant(t, show("ha_find_related", map[string]any{"item_type": "entity", "item_id": "light.kitchen_lights"}), "automation.test_toggle_kitchen_light", "device:")
	automationWant(t, show("ha_find_related", map[string]any{"item_type": "automation", "item_id": "1700000000001"}), "light.kitchen_lights")

	show("ha_list_blueprints", nil)
	bp := `blueprint:
  name: mcp_auto test blueprint
  description: Fires an event when another fires.
  domain: automation
  input:
    source_event:
      name: Source event
      selector:
        text:
    target_event:
      name: Target event
      default: mcp_auto_target
      selector:
        text:
triggers:
  - trigger: event
    event_type: !input source_event
actions:
  - event: !input target_event
`
	automationWant(t, show("ha_manage_blueprint", map[string]any{"action": "save", "path": bpPath, "yaml": bp}), "Saved the automation blueprint", "source_event")
	automationWant(t, show("ha_list_blueprints", map[string]any{"domain": "automation"}), bpPath)
	automationWant(t, show("ha_manage_automation", map[string]any{"action": "save", "id": bpAutoID,
		"config": fmt.Sprintf("alias: mcp_auto from blueprint %s\nuse_blueprint:\n  path: %s\n  input:\n    source_event: %s_bp\n", suffix, bpPath, event)}), "Created automation.")
	show("ha_find_related", map[string]any{"item_type": "automation_blueprint", "item_id": bpPath})
	showErr("ha_manage_blueprint", map[string]any{"action": "delete", "domain": "automation", "path": bpPath, "confirm": true})

	showErr("ha_manage_automation", map[string]any{"action": "delete", "id": bpAutoID})
	show("ha_manage_automation", map[string]any{"action": "delete", "id": bpAutoID, "confirm": true})
	show("ha_manage_blueprint", map[string]any{"action": "delete", "domain": "automation", "path": bpPath, "confirm": true})

	if out, isErr := automationCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "import",
		"url":  "https://github.com/home-assistant/core/blob/dev/homeassistant/components/automation/blueprints/motion_light.yaml",
		"path": "mcp_auto_bp/imported_" + suffix}); isErr {
		t.Logf("import failed (the HA container may have no internet access): %s", out)
	} else {
		t.Logf("import\n%s", out)
		show("ha_manage_blueprint", map[string]any{"action": "delete", "domain": "automation", "path": "mcp_auto_bp/imported_" + suffix, "confirm": true})
	}

	show("ha_manage_automation", map[string]any{"action": "delete", "kind": "scene", "id": sceneID, "confirm": true})
	show("ha_manage_automation", map[string]any{"action": "delete", "kind": "script", "id": scriptID, "confirm": true})
	show("ha_manage_automation", map[string]any{"action": "delete", "id": autoID, "confirm": true})
	showErr("ha_get_automation", map[string]any{"kind": "automation", "id": autoID})
}

//go:build integration

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
)

// Regression tests for leaks found in review, run against a real Home
// Assistant. Everything created here is named mcp_fix_ and removed again.

func TestIntegrationBlueprintTagsDoNotLeak(t *testing.T) {
	ha := automationIntegrationClient(t)
	cs := automationConnect(t, ha, false)
	paths := []string{"mcp_fix_a", "mcp_fix_b", "mcp_fix_c", "mcp_fix_d"}
	t.Cleanup(func() {
		for _, p := range paths {
			_ = ha.DeleteBlueprint(context.Background(), "automation", p+".yaml")
		}
	})
	for i, y := range []string{
		"blueprint:\n  name: !include .storage/auth\n  domain: automation\n  input: {}\ntriggers: []\nactions: []\n",
		"blueprint:\n  name: !env_var HOME\n  domain: automation\n  input: {}\ntriggers: []\nactions: []\n",
		"blueprint:\n  name: !include /etc/hostname\n  domain: automation\n  input: {}\ntriggers: []\nactions: []\n",
		"blueprint:\n  name: mcp_fix_d\n  domain: automation\n  input: {}\ntriggers:\n  - trigger: event\n    event_type: mcp_fix_evt\n" +
			"actions:\n  - variables:\n      leak: !include .storage/auth\n  - stop: done\n",
	} {
		out, isErr := automationCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "save", "path": paths[i], "yaml": y})
		if !isErr || !strings.Contains(out, "not allowed") {
			t.Errorf("save %s was not refused: %s", paths[i], out)
		}
		for _, leak := range []string{"refresh_token", "jwt_key", "/root", "/config"} {
			if strings.Contains(out, leak) {
				t.Errorf("save %s leaked %q: %s", paths[i], leak, out)
			}
		}
	}
	out := automationMustCall(t, cs, "ha_list_blueprints", map[string]any{"domain": "automation"})
	if strings.Contains(out, "mcp_fix_") {
		t.Errorf("a refused blueprint was saved:\n%s", out)
	}
}

func TestIntegrationBlueprintImportHosts(t *testing.T) {
	ha := automationIntegrationClient(t)
	cs := automationConnect(t, ha, false)
	for _, u := range []string{
		"https://localhost./manifest.json", "https://127.1/x.yaml", "https://0x7f.1/x.yaml", "https://nas.local./x.yaml",
		"https://router.lan./x.yaml", "https://127.0.0.1.nip.io/x.yaml",
	} {
		out, isErr := automationCall(t, cs, "ha_manage_blueprint", map[string]any{"action": "import", "url": u})
		if !isErr || !strings.Contains(out, "invalid argument") {
			t.Errorf("import of %s was not refused before reaching HA: %s", u, out)
		}
	}
}

func TestIntegrationTracesHideCameraTokens(t *testing.T) {
	ha := automationIntegrationClient(t)
	cs := automationConnect(t, ha, false)
	ctx := context.Background()
	st, err := ha.GetState(ctx, "camera.demo_camera")
	if err != nil {
		t.Skipf("the demo camera is needed: %v", err)
	}
	const id = "mcp_fix_trace"
	t.Cleanup(func() { _ = ha.DeleteConfigItem(context.Background(), homeassistant.KindAutomation, id) })
	automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "save", "id": id, "kind": "automation", "config": map[string]any{
		"alias":    id,
		"triggers": []any{map[string]any{"trigger": "event", "event_type": "mcp_fix_evt"}},
		"actions": []any{map[string]any{"variables": map[string]any{
			"tok":   "{{ state_attr('camera.demo_camera', 'access_token') }}",
			"pic":   "{{ state_attr('camera.demo_camera', 'entity_picture') }}",
			"state": "{{ states.camera.demo_camera.attributes }}",
		}}, map[string]any{"stop": "done"}},
	}})
	automationMustCall(t, cs, "ha_manage_automation", map[string]any{"action": "trigger", "id": id})
	var runID string
	for range 20 {
		if traces, err := ha.ListTraces(ctx, homeassistant.TraceAutomation, id); err == nil && len(traces) > 0 {
			runID = traces[0].RunID
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("no trace recorded")
	}
	tok, _ := st.Attributes["access_token"].(string)
	now, _ := ha.GetState(ctx, "camera.demo_camera")
	tokNow, _ := now.Attributes["access_token"].(string)
	for _, args := range []map[string]any{
		{"id": id, "run_id": runID},
		{"id": id, "run_id": runID, "step": "action/0"},
	} {
		out := automationMustCall(t, cs, "ha_traces", args)
		for _, bad := range []string{tok, tokNow} {
			if bad != "" && strings.Contains(out, bad) {
				t.Errorf("%v leaked %q:\n%s", args, bad, out)
			}
		}
	}
}

func TestIntegrationRenderTemplatePlainTokenHidden(t *testing.T) {
	ha := automationIntegrationClient(t)
	cs := automationConnect(t, ha, true)
	st, err := ha.GetState(context.Background(), "camera.demo_camera")
	if err != nil {
		t.Skipf("the demo camera is needed: %v", err)
	}
	tok, _ := st.Attributes["access_token"].(string)
	out := automationMustCall(t, cs, "ha_render_template", map[string]any{"template": "{{ state_attr('camera.demo_camera','access_token') }} {{ state_attr('camera.demo_camera','entity_picture') }}"})
	if tok == "" || strings.Contains(out, tok) {
		t.Fatalf("the plain token was not hidden: %s", out)
	}
}

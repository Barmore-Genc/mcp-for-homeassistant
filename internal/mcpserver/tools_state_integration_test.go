//go:build integration

package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These run against the Home Assistant in HA_URL with the demo integration
// loaded. They only toggle demo entities and create things named mcp_state_*,
// and never restart, because other test runs share the instance.
func stateIntegration(t *testing.T) *mcp.ClientSession {
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
	return stateConnectClient(t, ha, false, time.Now)
}

func stateLive(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, want ...string) string {
	t.Helper()
	out := stateCallOK(t, cs, name, args)
	t.Logf("%s %v\n%s", name, args, out)
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s: missing %q", name, w)
		}
	}
	return out
}

func TestIntegrationStateRead(t *testing.T) {
	cs := stateIntegration(t)
	stateLive(t, cs, "ha_list_entities", map[string]any{"limit": 40}, "entities match")
	stateLive(t, cs, "ha_list_entities", map[string]any{"domain": "light"}, "light.bed_light")
	stateLive(t, cs, "ha_list_entities", map[string]any{"search": "outside"}, "sensor.outside_temperature")

	out := stateLive(t, cs, "ha_get_state", map[string]any{"entity_ids": []string{"camera.demo_camera", "media_player.living_room", "light.living_room_rgbww_lights"}},
		"camera.demo_camera (Demo camera)", "registry:")
	for _, bad := range []string{"access_token", "token="} {
		if strings.Contains(out, bad) && !strings.Contains(out, "token=REDACTED") {
			t.Errorf("ha_get_state leaked %q", bad)
		}
	}
	if strings.Contains(out, "access_token") {
		t.Error("access_token attribute printed")
	}

	stateLive(t, cs, "ha_list_services", map[string]any{}, "light: ")
	stateLive(t, cs, "ha_list_services", map[string]any{"domain": "light,todo.add_item"}, "light.turn_on", "todo.add_item")

	out = stateLive(t, cs, "ha_render_template", map[string]any{"template": "{{ states('sensor.outside_temperature') }} / {{ state_attr('camera.demo_camera','access_token') }}"}, "REDACTED")
	if strings.Contains(out, "None") {
		t.Error("template did not read the camera attribute at all")
	}

	stateLive(t, cs, "ha_history", map[string]any{"entity_ids": []string{"sensor.outside_temperature", "light.bed_light", "climate.hvac"}, "start": "2020-01-01"}, "History")
	stateLive(t, cs, "ha_history", map[string]any{"entity_ids": []string{"climate.hvac"}, "attribute": "current_temperature", "start": "2020-01-01"}, "attribute current_temperature")
	stateLive(t, cs, "ha_logbook", map[string]any{"start": "2020-01-01", "limit": 30}, "Logbook")
	stateLive(t, cs, "ha_statistics", map[string]any{}, "statistics")
	stateLive(t, cs, "ha_statistics", map[string]any{"statistic_ids": []string{"sensor.outside_temperature", "sensor.total_energy_kwh"}, "start": "2020-01-01", "period": "month"}, "Statistics per month")
	stateLive(t, cs, "ha_system_log", map[string]any{"raw_log": true, "raw_log_lines": 15}, "home-assistant.log")
	stateLive(t, cs, "ha_check_config", map[string]any{}, "Configuration is valid")
	stateLive(t, cs, "ha_calendar_events", map[string]any{}, "calendar.calendar_1")
	stateLive(t, cs, "ha_calendar_events", map[string]any{"entity_id": "calendar.calendar_1", "start": "2020-01-01", "end": "2030-01-01"}, "calendar.calendar_1:")
	stateLive(t, cs, "ha_list_todo_items", map[string]any{}, "todo.shopping_list")
	stateLive(t, cs, "ha_list_notifications", map[string]any{})

	_, res := stateCall(t, cs, "ha_camera_snapshot", map[string]any{"entity_id": "camera.demo_camera", "width": 320})
	if res.IsError {
		t.Fatalf("snapshot failed: %v", res.Content)
	}
	found := false
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok && strings.HasPrefix(ic.MIMEType, "image/") && len(ic.Data) > 100 {
			found = true
			t.Logf("snapshot %s %d bytes", ic.MIMEType, len(ic.Data))
		}
	}
	if !found {
		t.Error("snapshot had no image")
	}
}

func TestIntegrationStateWrite(t *testing.T) {
	cs := stateIntegration(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1e9)

	// Listening while acting shows the payload round trip.
	listen := func(args map[string]any, act func()) string {
		var wg sync.WaitGroup
		var heard string
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ha_listen_events", Arguments: args})
			if err == nil && len(res.Content) > 0 {
				heard = res.Content[0].(*mcp.TextContent).Text
			}
		}()
		time.Sleep(time.Second)
		act()
		wg.Wait()
		t.Logf("ha_listen_events %v\n%s", args, heard)
		return heard
	}
	heard := listen(map[string]any{"event_type": "mcp_state_ping", "seconds": 4}, func() {
		stateLive(t, cs, "ha_fire_event", map[string]any{"event_type": "mcp_state_ping", "data": map[string]any{"n": suffix}}, "Fired mcp_state_ping")
	})
	if !strings.Contains(heard, `"n":"`+suffix+`"`) {
		t.Errorf("listen did not see the fired event: %s", heard)
	}

	before := stateLive(t, cs, "ha_get_state", map[string]any{"entity_ids": []string{"light.bed_light"}})
	heard = listen(map[string]any{"seconds": 4, "entity_id": "light.bed_light"}, func() {
		stateLive(t, cs, "ha_call_service", map[string]any{"domain": "light", "service": "toggle", "target": map[string]any{"entity_id": []string{"light.bed_light"}}},
			"Called light.toggle", "light.bed_light")
	})
	if !strings.Contains(heard, "state_changed light.bed_light:") || !strings.Contains(heard, "call_service") {
		t.Errorf("listen on all events missed the toggle: %s", heard)
	}
	restore := "turn_off"
	if strings.Contains(before, "state: on") {
		restore = "turn_on"
	}
	stateLive(t, cs, "ha_call_service", map[string]any{"service": "light." + restore, "target": map[string]any{"entity_id": []string{"light.bed_light"}}})

	item := "mcp_state_item_" + suffix
	stateLive(t, cs, "ha_call_service", map[string]any{"domain": "todo", "service": "add_item", "target": map[string]any{"entity_id": []string{"todo.shopping_list"}}, "data": map[string]any{"item": item}})
	stateLive(t, cs, "ha_list_todo_items", map[string]any{"entity_id": "todo.shopping_list"}, "[ ] "+item)
	stateLive(t, cs, "ha_call_service", map[string]any{"domain": "todo", "service": "get_items", "target": map[string]any{"entity_id": []string{"todo.shopping_list"}}, "return_response": true}, item)
	stateLive(t, cs, "ha_call_service", map[string]any{"domain": "todo", "service": "remove_item", "target": map[string]any{"entity_id": []string{"todo.shopping_list"}}, "data": map[string]any{"item": item}})

	nid := "mcp_state_note_" + suffix
	stateLive(t, cs, "ha_call_service", map[string]any{"domain": "persistent_notification", "service": "create", "data": map[string]any{"notification_id": nid, "title": "MCP test", "message": "hello"}})
	stateLive(t, cs, "ha_list_notifications", map[string]any{}, nid+" | ")
	stateLive(t, cs, "ha_call_service", map[string]any{"domain": "persistent_notification", "service": "dismiss", "data": map[string]any{"notification_id": nid}})

	out, res := stateCall(t, cs, "ha_call_service", map[string]any{"domain": "todo", "service": "get_items", "target": map[string]any{"entity_id": []string{"todo.shopping_list"}}})
	t.Logf("get_items without return_response: %s", out)
	if !res.IsError {
		t.Error("a data-only service without return_response succeeded")
	}

	if _, res := stateCall(t, cs, "ha_restart", map[string]any{}); !res.IsError {
		t.Error("unconfirmed restart accepted")
	}
}

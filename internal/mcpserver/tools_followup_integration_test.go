//go:build integration

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
)

// Everything created here is named mcp_fu_ and removed again.

func TestIntegrationServiceDescriptionsAndBackupSettings(t *testing.T) {
	cs, _ := organizeLive(t)
	out := organizeLiveOK(t, cs, "ha_list_services", map[string]any{"domain": "light.turn_on"}, "light.turn_on (Turn on light)")
	if !strings.Contains(out, "brightness_pct: number 0–100 % | ") {
		t.Errorf("brightness_pct has no description:\n%s", out)
	}
	organizeLiveOK(t, cs, "ha_backup_info", map[string]any{}, "Schedule: ", "Automatic backups contain: ", "Storage locations: ")
}

func TestIntegrationKeyOrderKept(t *testing.T) {
	cs, ha := organizeLive(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano() % 1_000_000_000)

	id := "mcp_fu_" + suffix
	t.Cleanup(func() { _ = ha.DeleteConfigItem(context.Background(), homeassistant.KindAutomation, id) })
	cfg := "alias: MCP FU " + suffix + "\ntriggers:\n  - trigger: event\n    event_type: mcp_fu_event\nactions:\n" +
		"  - action: persistent_notification.create\n    data:\n      title: z first\n      message: a second\nmode: single\n"
	organizeLiveOK(t, cs, "ha_manage_automation", map[string]any{"action": "save", "id": id, "config": cfg}, "Created automation.")
	organizeLiveOK(t, cs, "ha_get_automation", map[string]any{"id": id},
		"id: "+id+"\nalias: MCP FU "+suffix+"\ntriggers:\n  - trigger: event\n    event_type: mcp_fu_event\n",
		"    data:\n      title: z first\n      message: a second\n")

	path := "mcp-fu-" + suffix
	res := organizeLiveWS(t, "lovelace/dashboards/create", map[string]any{"url_path": path, "title": "mcp_fu_" + suffix, "mode": "storage"})
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res, &created)
	t.Cleanup(func() { organizeLiveWS(t, "lovelace/dashboards/delete", map[string]any{"dashboard_id": created.ID}) })
	organizeLiveOK(t, cs, "ha_save_dashboard", map[string]any{"url_path": path,
		"config": "views:\n  - title: B\n    path: b\n    cards:\n      - type: tile\n        entity: sun.sun\ntitle: A\n"}, "Saved dashboard")
	raw, err := ha.GetDashboardConfig(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	if want := `{"views":[{"title":"B","path":"b","cards":[{"type":"tile","entity":"sun.sun"}]}],"title":"A"}`; compact.String() != want {
		t.Fatalf("dashboard key order lost:\n%s", compact.String())
	}
}

//go:build integration

package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func integrationClient(t *testing.T) *Client {
	t.Helper()
	url, token := os.Getenv("HA_URL"), os.Getenv("HA_TOKEN")
	if url == "" || token == "" {
		t.Skip("HA_URL and HA_TOKEN must be set")
	}
	c, err := New(url, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// must(f())(t) fails the test if f returned an error.
func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func must2[A, B any](a A, b B, err error) func(*testing.T) (A, B) {
	return func(t *testing.T) (A, B) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func suffix() string { return fmt.Sprintf("%d", time.Now().UnixNano()%1e9) }

func TestIntegrationCore(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)

	v := must(c.HAVersion(ctx))(t)
	t.Logf("Home Assistant %s", v)

	cfg := must(c.GetConfig(ctx))(t)
	if cfg.Version != v || cfg.State != "RUNNING" {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	states := must(c.ListStates(ctx))(t)
	if len(states) < 20 {
		t.Fatalf("expected demo entities, got %d states", len(states))
	}
	st := must(c.GetState(ctx, "light.kitchen_lights"))(t)
	if st.Domain() != "light" || st.LastUpdated.IsZero() {
		t.Fatalf("bad state %+v", st)
	}
	if _, err := c.GetState(ctx, "light.does_not_exist"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}

	services := must(c.ListServices(ctx))(t)
	var sawTodoResponse bool
	for _, d := range services {
		if d.Domain == "todo" && d.Services["get_items"].Response != nil {
			sawTodoResponse = true
		}
	}
	if !sawTodoResponse {
		t.Fatal("todo.get_items response flag missing")
	}

	res := must(c.CallService(ctx, ServiceCall{
		Domain: "light", Service: "turn_on",
		Target: &Target{EntityID: []string{"light.kitchen_lights"}},
		Data:   map[string]any{"brightness": 120},
	}))(t)
	t.Logf("turn_on changed %d states", len(res.ChangedStates))

	resp := must(c.CallService(ctx, ServiceCall{
		Domain: "todo", Service: "get_items", ReturnResponse: true,
		Target: &Target{EntityID: []string{"todo.shopping_list"}},
	}))(t)
	if !strings.Contains(string(resp.Response), "todo.shopping_list") {
		t.Fatalf("unexpected response %s", resp.Response)
	}
	if _, err := c.CallService(ctx, ServiceCall{Domain: "todo", Service: "get_items", Target: &Target{EntityID: []string{"todo.shopping_list"}}}); err == nil {
		t.Fatal("expected error calling response-only service without ReturnResponse")
	} else {
		t.Logf("expected error: %v", err)
	}

	out := must(c.RenderTemplate(ctx, "{{ 1 + 1 }} {{ x }}", map[string]any{"x": "ok"}))(t)
	if out != "2 ok" {
		t.Fatalf("template = %q", out)
	}
	if _, err := c.RenderTemplate(ctx, "{{ 1 + }}", nil); err == nil {
		t.Fatal("expected template error")
	}

	chk := must(c.CheckConfig(ctx))(t)
	if chk.Result != "valid" {
		t.Fatalf("config check: %+v", chk)
	}

	log := must(c.GetErrorLog(ctx, 2048))(t)
	if len(log.Text) == 0 || len(log.Text) > 2048 {
		t.Fatalf("error log tail length %d", len(log.Text))
	}
	must(c.ListSystemLog(ctx))(t)
}

func TestIntegrationEvents(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	evType := "mcp_it_event_" + suffix()

	var (
		wg  sync.WaitGroup
		got *CollectedEvents
		err error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, err = c.CollectEvents(ctx, CollectEventsOptions{EventType: evType, Duration: 10 * time.Second, MaxEvents: 2})
	}()
	time.Sleep(time.Second)
	for i := range 3 {
		must(c.FireEvent(ctx, evType, map[string]any{"n": i}))(t)
	}
	wg.Wait()
	ok(t, err)
	if len(got.Events) != 2 || !got.LimitReached || got.Events[0].EventType != evType {
		t.Fatalf("collected %+v", got)
	}

	short := must(c.CollectEvents(ctx, CollectEventsOptions{EventType: evType, Duration: 500 * time.Millisecond}))(t)
	if len(short.Events) != 0 || short.LimitReached {
		t.Fatalf("expected no events, got %+v", short)
	}

	sctx, cancel := context.WithCancel(ctx)
	sub := must(c.SubscribeEvents(sctx, "state_changed"))(t)
	must(c.CallService(ctx, ServiceCall{Domain: "light", Service: "toggle", Target: &Target{EntityID: []string{"light.ceiling_lights"}}}))(t)
	ev, open, err := sub.Next(ctx)
	ok(t, err)
	if !open || ev.EventType != "state_changed" {
		t.Fatalf("bad event %+v", ev)
	}
	cancel()
	for range sub.Events() {
	}
	ok(t, sub.Close())
}

func TestIntegrationAutomationScriptScene(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	id := "mcp_it_" + suffix()

	ok(t, c.SaveConfigItem(ctx, KindAutomation, id, map[string]any{
		"alias":       "MCP IT " + id,
		"description": "integration test",
		"triggers":    []any{map[string]any{"trigger": "event", "event_type": "mcp_it_trigger_" + id}},
		"actions":     []any{map[string]any{"action": "light.toggle", "target": map[string]any{"entity_id": "light.kitchen_lights"}}},
		"mode":        "single",
	}))
	got := must(c.GetConfigItem(ctx, KindAutomation, id))(t)
	if got["id"] != id || got["alias"] != "MCP IT "+id {
		t.Fatalf("automation config %v", got)
	}
	if err := c.SaveConfigItem(ctx, KindAutomation, id, map[string]any{"triggers": "nonsense"}); err == nil {
		t.Fatal("expected validation error")
	}

	var entityID string
	waitFor(t, 10*time.Second, func() bool {
		items := must(c.ListConfigItems(ctx, KindAutomation))(t)
		for _, it := range items {
			if it.ConfigID == id {
				entityID = it.EntityID
				return true
			}
		}
		return false
	})

	must(c.FireEvent(ctx, "mcp_it_trigger_"+id, nil))(t)
	var traces []TraceSummary
	waitFor(t, 10*time.Second, func() bool {
		traces = must(c.ListTraces(ctx, TraceAutomation, id))(t)
		return len(traces) > 0 && traces[0].State == "stopped"
	})
	tr := must(c.GetTrace(ctx, TraceAutomation, id, traces[0].RunID))(t)
	var full map[string]any
	ok(t, json.Unmarshal(tr, &full))
	if full["config"] == nil || full["trace"] == nil {
		t.Fatalf("trace missing fields: %v", full)
	}
	ctxs := must(c.TraceContexts(ctx, TraceAutomation, id))(t)
	if len(ctxs) == 0 {
		t.Fatal("no trace contexts")
	}
	must(c.TraceContexts(ctx, "", ""))(t)
	if traces[0].Trigger == nil {
		t.Fatal("automation trace has no trigger description")
	}

	rel := must(c.SearchRelated(ctx, SearchAutomation, entityID))(t)
	if !slices.Contains(rel[SearchEntity], "light.kitchen_lights") {
		t.Fatalf("search/related = %v", rel)
	}

	ok(t, c.DeleteConfigItem(ctx, KindAutomation, id))
	if _, err := c.GetConfigItem(ctx, KindAutomation, id); !IsNotFound(err) {
		t.Fatalf("expected not found after delete, got %v", err)
	}
	if err := c.DeleteConfigItem(ctx, KindAutomation, id); err == nil {
		t.Fatal("expected error deleting missing automation")
	}

	scriptID := "mcp_it_script_" + suffix()
	ok(t, c.SaveConfigItem(ctx, KindScript, scriptID, map[string]any{
		"alias":    "MCP IT script",
		"sequence": []any{map[string]any{"action": "light.turn_off", "target": map[string]any{"entity_id": "light.kitchen_lights"}}},
	}))
	must(c.GetConfigItem(ctx, KindScript, scriptID))(t)
	waitFor(t, 10*time.Second, func() bool {
		_, err := c.GetState(ctx, "script."+scriptID)
		return err == nil
	})
	scripts := must(c.ListConfigItems(ctx, KindScript))(t)
	if !slices.ContainsFunc(scripts, func(s ConfigItemSummary) bool { return s.ConfigID == scriptID }) {
		t.Fatalf("script %s not listed: %+v", scriptID, scripts)
	}
	must(c.CallService(ctx, ServiceCall{Domain: "script", Service: scriptID}))(t)
	waitFor(t, 10*time.Second, func() bool {
		return len(must(c.ListTraces(ctx, TraceScript, scriptID))(t)) > 0
	})
	ok(t, c.DeleteConfigItem(ctx, KindScript, scriptID))

	sceneID := "mcp_it_scene_" + suffix()
	ok(t, c.SaveConfigItem(ctx, KindScene, sceneID, map[string]any{
		"name":     "MCP IT scene",
		"entities": map[string]any{"light.kitchen_lights": map[string]any{"state": "on"}},
	}))
	sc := must(c.GetConfigItem(ctx, KindScene, sceneID))(t)
	if sc["id"] != sceneID {
		t.Fatalf("scene %v", sc)
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, s := range must(c.ListConfigItems(ctx, KindScene))(t) {
			if s.ConfigID == sceneID {
				return true
			}
		}
		return false
	})
	ok(t, c.DeleteConfigItem(ctx, KindScene, sceneID))

	val := must(c.ValidateConfig(ctx, ValidateConfigRequest{
		Triggers: []any{map[string]any{"trigger": "state", "entity_id": "light.kitchen_lights"}},
		Actions:  []any{map[string]any{"action": "not_a_service"}},
	}))(t)
	if !val.Triggers.Valid || val.Actions.Valid || val.Conditions != nil {
		t.Fatalf("validate_config = %+v", val)
	}
	t.Logf("invalid action error: %s", *val.Actions.Error)
}

func TestIntegrationDevices(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	devs := must(c.ListDevices(ctx))(t)
	if len(devs) == 0 {
		t.Fatal("no devices")
	}
	var trigCount int
	var dev Device
	for _, d := range devs {
		trig := must(c.ListDeviceAutomations(ctx, DeviceTrigger, d.ID))(t)
		if len(trig) > 0 {
			dev = d
			trigCount = len(trig)
			caps := must(c.DeviceAutomationCapabilities(ctx, DeviceTrigger, trig[0]))(t)
			t.Logf("device %s: %d triggers, first capabilities %s", d.DisplayName(), len(trig), caps)
			break
		}
	}
	if trigCount == 0 {
		t.Fatal("no device with triggers")
	}
	must(c.ListDeviceAutomations(ctx, DeviceCondition, dev.ID))(t)
	must(c.ListDeviceAutomations(ctx, DeviceAction, dev.ID))(t)
	must(c.SearchRelated(ctx, SearchDevice, dev.ID))(t)

	up := must(c.UpdateDevice(ctx, dev.ID, DeviceUpdate{NameByUser: Set("MCP renamed")}))(t)
	if up.NameByUser == nil || *up.NameByUser != "MCP renamed" {
		t.Fatalf("device update %+v", up)
	}
	up = must(c.UpdateDevice(ctx, dev.ID, DeviceUpdate{NameByUser: Null[string]()}))(t)
	if up.NameByUser != nil {
		t.Fatalf("name_by_user not cleared: %v", *up.NameByUser)
	}
}

func TestIntegrationRegistries(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	s := suffix()

	floor := must(c.CreateFloor(ctx, FloorCreate{Name: "MCP Floor " + s, Level: new(2)}))(t)
	floor = must(c.UpdateFloor(ctx, floor.FloorID, FloorUpdate{Icon: Set("mdi:home-floor-2"), Level: Null[int]()}))(t)
	if floor.Level != nil || floor.Icon == nil {
		t.Fatalf("floor update %+v", floor)
	}
	if !slices.ContainsFunc(must(c.ListFloors(ctx))(t), func(f Floor) bool { return f.FloorID == floor.FloorID }) {
		t.Fatal("floor not listed")
	}

	label := must(c.CreateLabel(ctx, LabelCreate{Name: "MCP Label " + s, Color: "red"}))(t)
	label = must(c.UpdateLabel(ctx, label.LabelID, LabelUpdate{Description: Set("desc"), Color: Null[string]()}))(t)
	if label.Color != nil || label.Description == nil {
		t.Fatalf("label update %+v", label)
	}
	must(c.ListLabels(ctx))(t)

	area := must(c.CreateArea(ctx, AreaCreate{Name: "MCP Area " + s, FloorID: floor.FloorID, Labels: []string{label.LabelID}}))(t)
	if area.FloorID == nil || *area.FloorID != floor.FloorID || len(area.Labels) != 1 || area.CreatedAt.IsZero() {
		t.Fatalf("area create %+v", area)
	}
	area = must(c.UpdateArea(ctx, area.AreaID, AreaUpdate{FloorID: Null[string](), Labels: []string{}, Aliases: []string{"mcp alias"}}))(t)
	if area.FloorID != nil || len(area.Labels) != 0 || len(area.Aliases) != 1 {
		t.Fatalf("area update %+v", area)
	}
	must(c.ListAreas(ctx))(t)
	rel := must(c.SearchRelated(ctx, SearchArea, area.AreaID))(t)
	t.Logf("area related: %v", rel)

	cat := must(c.CreateCategory(ctx, "automation", CategoryCreate{Name: "MCP Cat " + s, Icon: "mdi:robot"}))(t)
	cat = must(c.UpdateCategory(ctx, "automation", cat.CategoryID, CategoryUpdate{Name: "MCP Cat2 " + s, Icon: Null[string]()}))(t)
	if cat.Icon != nil || !strings.HasPrefix(cat.Name, "MCP Cat2") {
		t.Fatalf("category update %+v", cat)
	}
	if !slices.ContainsFunc(must(c.ListCategories(ctx, "automation"))(t), func(x Category) bool { return x.CategoryID == cat.CategoryID }) {
		t.Fatal("category not listed")
	}

	ents := must(c.ListEntityRegistry(ctx))(t)
	if len(ents) == 0 {
		t.Fatal("empty entity registry")
	}
	const eid = "light.kitchen_lights"
	e := must(c.GetEntityRegistryEntry(ctx, eid))(t)
	if e.EntityID != eid || e.Capabilities == nil {
		t.Fatalf("entity %+v", e)
	}
	upd := must(c.UpdateEntityRegistryEntry(ctx, eid, EntityUpdate{
		Name: Set("MCP Kitchen"), AreaID: Set(area.AreaID), Labels: []string{label.LabelID},
		Categories: map[string]*string{"automation": new(cat.CategoryID)},
	}))(t)
	if upd.Entry.Name == nil || *upd.Entry.Name != "MCP Kitchen" || upd.Entry.AreaID == nil || upd.Entry.Categories["automation"] != cat.CategoryID {
		t.Fatalf("entity update %+v", upd.Entry)
	}
	upd = must(c.UpdateEntityRegistryEntry(ctx, eid, EntityUpdate{
		Name: Null[string](), AreaID: Null[string](), Labels: []string{}, Categories: map[string]*string{"automation": nil},
	}))(t)
	if upd.Entry.Name != nil || upd.Entry.AreaID != nil || len(upd.Entry.Labels) != 0 || len(upd.Entry.Categories) != 0 {
		t.Fatalf("entity reset %+v", upd.Entry)
	}
	upd = must(c.UpdateEntityRegistryEntry(ctx, "sensor.outside_temperature", EntityUpdate{
		OptionsDomain: "sensor", Options: map[string]any{"display_precision": 2},
	}))(t)
	t.Logf("sensor options: %v", upd.Entry.Options)

	ok(t, c.DeleteCategory(ctx, "automation", cat.CategoryID))
	ok(t, c.DeleteArea(ctx, area.AreaID))
	ok(t, c.DeleteLabel(ctx, label.LabelID))
	ok(t, c.DeleteFloor(ctx, floor.FloorID))
	if err := c.DeleteFloor(ctx, floor.FloorID); err == nil {
		t.Fatal("expected error deleting missing floor")
	}
}

func TestIntegrationEntityRemove(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	h := must(c.CreateHelper(ctx, "input_boolean", map[string]any{"name": "MCP remove " + suffix()}))(t)
	id := h["id"].(string)
	eid := "input_boolean." + id
	waitFor(t, 10*time.Second, func() bool {
		_, err := c.GetEntityRegistryEntry(ctx, eid)
		return err == nil
	})
	ok(t, c.RemoveEntityRegistryEntry(ctx, eid))
	if _, err := c.GetEntityRegistryEntry(ctx, eid); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
	ok(t, c.DeleteHelper(ctx, "input_boolean", id))
}

func TestIntegrationHelpers(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	s := suffix()
	create := map[string]map[string]any{
		"input_boolean":  {"name": "MCP bool " + s},
		"input_number":   {"name": "MCP number " + s, "min": 0, "max": 50, "step": 5},
		"input_select":   {"name": "MCP select " + s, "options": []string{"a", "b"}},
		"input_text":     {"name": "MCP text " + s, "max": 20},
		"input_datetime": {"name": "MCP datetime " + s, "has_date": true, "has_time": true},
		"input_button":   {"name": "MCP button " + s},
		"counter":        {"name": "MCP counter " + s, "step": 2},
		"timer":          {"name": "MCP timer " + s, "duration": "00:01:00"},
		"schedule":       {"name": "MCP schedule " + s, "monday": []any{map[string]any{"from": "07:00:00", "to": "08:00:00"}}},
		"zone":           {"name": "MCP zone " + s, "latitude": 52.1, "longitude": 4.3, "radius": 150},
	}
	for _, domain := range HelperDomains {
		t.Run(domain, func(t *testing.T) {
			item := must(c.CreateHelper(ctx, domain, create[domain]))(t)
			id, _ := item["id"].(string)
			if id == "" {
				t.Fatalf("no id in %v", item)
			}
			upd := must(c.UpdateHelper(ctx, domain, id, map[string]any{"icon": "mdi:test-tube"}))(t)
			if upd["icon"] != "mdi:test-tube" || upd["name"] != create[domain]["name"] {
				t.Fatalf("update lost fields: %v", upd)
			}
			for k, want := range create[domain] {
				// HA normalizes durations ("00:01:00" -> "0:01:00") and schedule times.
				if k == "name" || k == "options" || k == "monday" || k == "duration" {
					continue
				}
				if fmt.Sprint(upd[k]) != fmt.Sprint(want) {
					t.Errorf("field %s = %v after update, want %v", k, upd[k], want)
				}
			}
			list := must(c.ListHelpers(ctx, domain))(t)
			if !slices.ContainsFunc(list, func(m map[string]any) bool { return m["id"] == id }) {
				t.Fatal("helper not listed")
			}
			ok(t, c.DeleteHelper(ctx, domain, id))
			if _, err := c.UpdateHelper(ctx, domain, id, map[string]any{"icon": "x"}); !IsNotFound(err) {
				t.Fatalf("expected not found, got %v", err)
			}
		})
	}
	must(c.ListPersons(ctx))(t)
	if _, err := c.CreateHelper(ctx, "input_boolean", map[string]any{"name": "x", "type": "config/auth/delete"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected reserved key rejection, got %v", err)
	}
}

func TestIntegrationHistory(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	start := time.Now().Add(-2 * time.Hour)

	h := must(c.History(ctx, HistoryRequest{EntityIDs: []string{"light.kitchen_lights", "sun.sun"}, Start: start}))(t)
	if len(h["light.kitchen_lights"]) == 0 || h["light.kitchen_lights"][0].LastChanged.IsZero() {
		t.Fatalf("history %v", h)
	}
	must(c.History(ctx, HistoryRequest{EntityIDs: []string{"light.kitchen_lights"}, Start: start, End: time.Now(), MinimalResponse: true, NoAttributes: true, AllChanges: true}))(t)

	lb := must(c.Logbook(ctx, LogbookRequest{Start: start}))(t)
	if len(lb) == 0 || lb[0].When.IsZero() {
		t.Fatalf("logbook %v", lb)
	}
	must(c.Logbook(ctx, LogbookRequest{Start: start, End: time.Now(), EntityIDs: []string{"light.kitchen_lights"}}))(t)

	ids := must(c.ListStatisticIDs(ctx, ""))(t)
	if len(ids) == 0 {
		t.Fatal("no statistics")
	}
	must(c.ListStatisticIDs(ctx, "sum"))(t)
	statID := "sensor.outside_temperature"
	stats := must(c.StatisticsDuringPeriod(ctx, StatisticsRequest{
		StatisticIDs: []string{statID}, Start: time.Now().Add(-2 * time.Hour), Period: "5minute",
	}))(t)
	// Short-term statistics exist only after HA has run for 5-10 minutes.
	t.Logf("statistic %s: %d rows", statID, len(stats[statID]))
	for _, row := range stats[statID] {
		if row.Start.IsZero() || row.Mean == nil {
			t.Fatalf("bad statistics row %+v", row)
		}
	}
	must(c.StatisticsDuringPeriod(ctx, StatisticsRequest{
		StatisticIDs: []string{statID}, Start: time.Now().Add(-7 * 24 * time.Hour), End: time.Now(), Period: "day",
		Types: []string{"mean", "max"}, Units: map[string]string{"temperature": "°F"},
	}))(t)
}

func TestIntegrationMisc(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)

	cals := must(c.ListCalendars(ctx))(t)
	if len(cals) == 0 {
		t.Fatal("no calendars")
	}
	evs := must(c.CalendarEvents(ctx, cals[0].EntityID, time.Now().Add(-24*time.Hour), time.Now().Add(7*24*time.Hour)))(t)
	if len(evs) == 0 || (evs[0].Start.Date == "" && evs[0].Start.DateTime == "") {
		t.Fatalf("calendar events %+v", evs)
	}

	item := "MCP item " + suffix()
	must(c.CallService(ctx, ServiceCall{Domain: "todo", Service: "add_item", Target: &Target{EntityID: []string{"todo.shopping_list"}}, Data: map[string]any{"item": item}}))(t)
	items := must(c.ListTodoItems(ctx, "todo.shopping_list"))(t)
	if !slices.ContainsFunc(items, func(i TodoItem) bool { return i.Summary == item && i.Status == "needs_action" && i.UID != "" }) {
		t.Fatalf("todo items %+v", items)
	}
	must(c.CallService(ctx, ServiceCall{Domain: "todo", Service: "update_item", Target: &Target{EntityID: []string{"todo.shopping_list"}}, Data: map[string]any{"item": item, "status": "completed"}}))(t)
	must(c.CallService(ctx, ServiceCall{Domain: "todo", Service: "remove_item", Target: &Target{EntityID: []string{"todo.shopping_list"}}, Data: map[string]any{"item": []string{item}}}))(t)

	img := must(c.CameraSnapshot(ctx, "camera.demo_camera", 0, 0))(t)
	if !strings.HasPrefix(img.ContentType, "image/") || len(img.Data) < 100 {
		t.Fatalf("snapshot %s %d bytes", img.ContentType, len(img.Data))
	}
	if _, err := c.CameraSnapshot(ctx, "light.kitchen_lights", 0, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument, got %v", err)
	}

	nid := "mcp_it_" + suffix()
	must(c.CallService(ctx, ServiceCall{Domain: "persistent_notification", Service: "create", Data: map[string]any{"notification_id": nid, "title": "MCP", "message": "hello"}}))(t)
	notes := must(c.ListNotifications(ctx))(t)
	if !slices.ContainsFunc(notes, func(n PersistentNotification) bool { return n.NotificationID == nid && !n.CreatedAt.IsZero() }) {
		t.Fatalf("notification missing: %+v", notes)
	}
	must(c.CallService(ctx, ServiceCall{Domain: "persistent_notification", Service: "dismiss", Data: map[string]any{"notification_id": nid}}))(t)

	must(c.ListDashboards(ctx))(t)
	if _, err := c.GetDashboardConfig(ctx, ""); err != nil {
		t.Logf("default dashboard before save: %v", err)
	}
	dash := map[string]any{"title": "MCP", "views": []any{map[string]any{"title": "Home", "cards": []any{map[string]any{"type": "entities", "entities": []string{"light.kitchen_lights"}}}}}}
	ok(t, c.SaveDashboardConfig(ctx, "", must(json.Marshal(dash))(t)))
	raw := must(c.GetDashboardConfig(ctx, ""))(t)
	if !strings.Contains(string(raw), "light.kitchen_lights") {
		t.Fatalf("dashboard config %s", raw)
	}
	if _, err := c.GetDashboardConfig(ctx, "no-such-dash"); err == nil {
		t.Fatal("expected error for unknown dashboard")
	}

	info := must(c.BackupInfo(ctx))(t)
	t.Logf("backup state %s, %d backups", info.State, len(info.Backups))
	ok(t, c.wsCall(ctx, "backup/config/update", map[string]any{
		"create_backup": map[string]any{"agent_ids": []string{"backup.local"}, "include_database": false, "password": nil},
	}, nil))
	job := must(c.GenerateBackup(ctx))(t)
	t.Logf("backup job %s", job)
	var backupID string
	waitFor(t, 60*time.Second, func() bool {
		info := must(c.BackupInfo(ctx))(t)
		for _, b := range info.Backups {
			if b.WithAutomaticSettings != nil && *b.WithAutomaticSettings && info.State == "idle" {
				backupID = b.BackupID
				return true
			}
		}
		return false
	})
	det, _ := must2(c.BackupDetails(ctx, backupID))(t)
	if det == nil || det.Agents["backup.local"].Size == 0 {
		t.Fatalf("backup details %+v", det)
	}
	ok(t, c.wsCall(ctx, "backup/delete", map[string]any{"backup_id": backupID}, nil))
	b, _, err := c.BackupDetails(ctx, "doesnotexist")
	ok(t, err)
	if b != nil {
		t.Fatal("expected nil backup")
	}

	entries := must(c.ListConfigEntries(ctx, ""))(t)
	if len(entries) == 0 {
		t.Fatal("no config entries")
	}
	var target ConfigEntry
	for _, e := range entries {
		if e.Domain == "sun" {
			target = e
		}
	}
	if target.EntryID == "" {
		t.Fatalf("no sun config entry in %+v", entries)
	}
	must(c.ListConfigEntries(ctx, "sun"))(t)
	got := must(c.GetConfigEntry(ctx, target.EntryID))(t)
	if got.Domain != "sun" {
		t.Fatalf("config entry %+v", got)
	}
	must(c.ReloadConfigEntry(ctx, target.EntryID))(t)
	must(c.SetConfigEntryDisabled(ctx, target.EntryID, true))(t)
	if e := must(c.GetConfigEntry(ctx, target.EntryID))(t); e.DisabledBy == nil {
		t.Fatal("entry not disabled")
	}
	must(c.SetConfigEntryDisabled(ctx, target.EntryID, false))(t)
	if _, err := c.ReloadConfigEntry(ctx, "01NOTANENTRY"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestIntegrationBlueprints(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)
	bps := must(c.ListBlueprints(ctx, "automation"))(t)
	if len(bps) == 0 {
		t.Fatal("no automation blueprints")
	}
	must(c.ListBlueprints(ctx, "script"))(t)

	path := "mcp_it/test_" + suffix()
	yaml := `blueprint:
  name: MCP test blueprint
  domain: automation
  input:
    target_light:
      selector:
        entity:
          domain: light
triggers:
  - trigger: event
    event_type: mcp_bp_test
actions:
  - action: light.toggle
    target:
      entity_id: !input target_light
`
	overrode := must(c.SaveBlueprint(ctx, SaveBlueprintRequest{Domain: "automation", Path: path, YAML: yaml}))(t)
	if overrode {
		t.Fatal("unexpected override")
	}
	if _, err := c.SaveBlueprint(ctx, SaveBlueprintRequest{Domain: "automation", Path: path, YAML: yaml}); err == nil {
		t.Fatal("expected already_exists")
	}
	bps = must(c.ListBlueprints(ctx, "automation"))(t)
	if _, ok := bps[path+".yaml"]; !ok {
		t.Fatalf("saved blueprint not listed: %v", bps)
	}
	ok(t, c.DeleteBlueprint(ctx, "automation", path+".yaml"))

	imp, err := c.ImportBlueprint(ctx, "https://github.com/home-assistant/core/blob/dev/homeassistant/components/automation/blueprints/motion_light.yaml")
	if err != nil {
		t.Logf("blueprint import failed (needs internet access from HA): %v", err)
	} else if imp.RawData == "" || imp.Blueprint.Metadata["domain"] != "automation" {
		t.Fatalf("import %+v", imp)
	}
	if _, err := c.ImportBlueprint(ctx, "http://192.168.1.1/x.yaml"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("expected URL rejection")
	}
}

// TestIntegrationZRestart runs last (tests run in file order) and checks that
// the WebSocket connection recovers after HA restarts.
func TestIntegrationZRestart(t *testing.T) {
	if os.Getenv("HA_TEST_RESTART") == "" {
		t.Skip("set HA_TEST_RESTART=1 to restart Home Assistant")
	}
	c := integrationClient(t)
	ctx := ctxT(t)
	must(c.ListAreas(ctx))(t)
	c.wsMu.Lock()
	before := c.ws
	c.wsMu.Unlock()
	ok(t, c.Restart(ctx))
	time.Sleep(5 * time.Second)
	waitFor(t, 90*time.Second, func() bool {
		cfg, err := c.GetConfig(ctx)
		return err == nil && cfg.State == "RUNNING"
	})
	waitFor(t, 30*time.Second, func() bool {
		_, err := c.ListAreas(ctx)
		return err == nil
	})
	c.wsMu.Lock()
	after := c.ws
	c.wsMu.Unlock()
	if after == before {
		t.Fatal("expected a new WebSocket connection after restart")
	}
}

func TestIntegrationTranslationsBackupConfigKeyOrder(t *testing.T) {
	c := integrationClient(t)
	ctx := ctxT(t)

	tr := must(c.GetTranslations(ctx, "en", "services", "light"))(t)
	if tr["component.light.services.turn_on.fields.brightness_pct.description"] == "" {
		t.Fatalf("no light.turn_on field description in %d strings", len(tr))
	}
	for k := range tr {
		if !strings.HasPrefix(k, "component.light.") {
			t.Fatalf("the integration filter was ignored: %s", k)
		}
	}

	bc := must(c.BackupConfig(ctx))(t)
	if bc.Schedule.Recurrence == "" {
		t.Fatalf("backup config without a schedule: %+v", bc)
	}

	id := "mcp_fu_" + suffix()
	t.Cleanup(func() { _ = c.DeleteConfigItem(context.Background(), KindAutomation, id) })
	body := `{"alias":"MCP FU ` + id + `","triggers":[{"trigger":"event","event_type":"mcp_fu_event","event_data":{"z":1,"a":2}}],` +
		`"actions":[],"mode":"single","id":"not_this_one"}`
	ok(t, c.SaveConfigItemRaw(ctx, KindAutomation, id, json.RawMessage(body)))
	raw := must(c.GetConfigItemRaw(ctx, KindAutomation, id))(t)
	want := `{"id":"` + id + `","alias":"MCP FU ` + id + `","triggers":[{"trigger":"event","event_type":"mcp_fu_event","event_data":{"z":1,"a":2}}]`
	var compact bytes.Buffer
	ok(t, json.Compact(&compact, raw))
	if !strings.HasPrefix(compact.String(), want) {
		t.Fatalf("stored key order lost:\n%s", raw)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

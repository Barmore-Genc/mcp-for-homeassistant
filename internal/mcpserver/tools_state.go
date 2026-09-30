package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addStateTools registers the tools for entity states, services, events, history and logs. Write tools are skipped when s.readOnly is set.
func (s *Server) addStateTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_entities",
		Annotations: readOnlyTool(),
		Description: "Find entities and their current state, one line each: entity_id, name, state with unit, area. " +
			"This is the starting point for almost every question about the home ('which lights are on', 'what is the " +
			"temperature upstairs', 'what is the id of the kitchen light'). Filters combine with AND: domain (light, " +
			"sensor, ...), area, label, device (id or name) and search (words matched against entity id and name). " +
			"Use ha_get_state for the attributes of specific entities, and ha_history for how a state changed over time.",
	}, s.stateListEntities)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_get_state",
		Annotations: readOnlyTool(),
		Description: "Everything about one or more entities: state, all attributes (brightness, color, setpoints, " +
			"supported features, forecast, ...), when it last changed, and its registry details (area, device, labels, " +
			"platform, disabled or hidden). Use it after ha_list_entities when the one-line summary is not enough, " +
			"for example to see which color modes a light supports before calling a service on it.",
	}, s.stateGetState)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_services",
		Annotations: readOnlyTool(),
		Description: "The services (actions) Home Assistant offers. Without a domain it lists every domain with its " +
			"service names. With a domain (or domain.service) it lists each service's fields, which are required, " +
			"their type, range and allowed options, what the service can target and whether it returns a response. " +
			"Check this before ha_call_service when you are not sure which fields a service takes.",
	}, s.stateListServices)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_render_template",
		Annotations: readOnlyTool(),
		Description: "Render a Home Assistant Jinja template and return the result, exactly as an automation would " +
			"see it. Use it to test a template before putting it in an automation, or to answer questions that need " +
			"computation across entities, e.g. \"{{ states.light | selectattr('state','eq','on') | list | count }}\" " +
			"or \"{{ area_entities('kitchen') }}\".",
	}, s.stateRenderTemplate)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_history",
		Annotations: readOnlyTool(),
		Description: "How the state (or one attribute) of entities changed over a time range, by default the last 24 " +
			"hours. Numeric sensors get min, max, average and last value, and a long series is reduced to time buckets " +
			"with min/avg/max each. Other entities get the time spent in each state and the list of changes. Use it " +
			"for 'when did the door open', 'how long was the heating on', 'what was the temperature overnight'. For " +
			"months of data use ha_statistics; for who or what caused a change use ha_logbook.",
	}, s.stateHistory)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_logbook",
		Annotations: readOnlyTool(),
		Description: "The logbook: what happened and what caused it (which automation, script, service call or user), " +
			"by default for the last 24 hours. Filter by entity or device ids. Use it to answer 'why did the light turn " +
			"on' or 'what happened while I was away'; ha_history is better for the values of a sensor over time.",
	}, s.stateLogbook)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_statistics",
		Annotations: readOnlyTool(),
		Description: "Long-term statistics, which Home Assistant keeps for sensors with a state class long after " +
			"regular history is purged. Without statistic_ids it lists the available statistics (filter with search). " +
			"With statistic_ids it returns one row per hour, day, week or month: mean/min/max for measurements, and " +
			"change (consumption in the period) for meters such as energy, gas and water. Use it for 'how much energy " +
			"did we use per day last month' or 'average temperature per week this year'.",
	}, s.stateStatistics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_listen_events",
		Annotations: readOnlyTool(),
		Description: "Listen on the event bus for a few seconds (at most 120) and return the events that fired. " +
			"The way to discover what an event looks like before writing an automation trigger for it: ask the person " +
			"to press the button, then read the zha_event, deconz_event or state_changed payload. Filter by event_type " +
			"and entity_id, or leave event_type empty to see everything.",
	}, s.stateListenEvents)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_system_log",
		Annotations: readOnlyTool(),
		Description: "Warnings and errors Home Assistant has logged, deduplicated with counts, source and the end of " +
			"the traceback, newest first. Set raw_log for the tail of home-assistant.log itself, which also has info " +
			"and debug lines. Use it when an integration, automation or device misbehaves.",
	}, s.stateSystemLog)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_check_config",
		Annotations: readOnlyTool(),
		Description: "Run Home Assistant's configuration check on the YAML configuration on disk and report errors and " +
			"warnings. Run it after changing YAML and before a restart; a restart with a broken configuration can leave " +
			"Home Assistant in recovery mode.",
	}, s.stateCheckConfig)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_calendar_events",
		Annotations: readOnlyTool(),
		Description: "Without entity_id, list the calendars. With a calendar entity_id, list its events in a range " +
			"(default: today and the next 6 days). Create events with ha_call_service calendar.create_event.",
	}, s.stateCalendarEvents)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_todo_items",
		Annotations: readOnlyTool(),
		Description: "Without entity_id, list the to-do lists (shopping list and others) with their open item counts. " +
			"With a todo entity_id, list its items with status, due date and description. Add, change, complete or " +
			"remove items with ha_call_service todo.add_item, todo.update_item and todo.remove_item.",
	}, s.stateListTodoItems)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_camera_snapshot",
		Annotations: readOnlyTool(),
		Description: "A current still image from a camera entity, returned as an image you can look at. Use it to " +
			"check what a camera sees ('is the garage door open', 'is there a package'). width scales the image down, " +
			"which is faster and smaller; 640 is plenty for most questions.",
	}, s.stateCameraSnapshot)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_notifications",
		Annotations: readOnlyTool(),
		Description: "The persistent notifications shown in the Home Assistant sidebar (repairs, new devices found, " +
			"messages from automations). Dismiss one with ha_call_service persistent_notification.dismiss and its " +
			"notification_id, or create one with persistent_notification.create.",
	}, s.stateListNotifications)

	if s.readOnly {
		return
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_call_service",
		Annotations: writeTool(true),
		Description: "Call a Home Assistant service (action). This is how anything is changed: light.turn_on / " +
			"turn_off / toggle (with brightness_pct, color_temp_kelvin, rgb_color), switch.turn_on, climate." +
			"set_temperature, cover.open_cover, lock.lock, media_player.play_media, scene.turn_on, script.turn_on, " +
			"automation.trigger, input_boolean.turn_on, todo.add_item / update_item / remove_item, " +
			"calendar.create_event, persistent_notification.create / dismiss, notify.<service>, update.install, and " +
			"any other service ha_list_services shows. Put entity, device, area, floor or label ids in target and the " +
			"service fields in data. Set return_response for services that return data (todo.get_items, " +
			"weather.get_forecasts, calendar.get_events). The entities whose state changed are reported back. Use " +
			"ha_restart to restart Home Assistant.",
	}, s.stateCallService)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_fire_event",
		Annotations: writeTool(false),
		Description: "Fire a custom event on the Home Assistant event bus with optional data. Automations with an " +
			"event trigger for that event_type will run. Use it to test such an automation, or to signal between " +
			"automations. Use ha_listen_events to see what real events look like.",
	}, s.stateFireEvent)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_restart",
		Annotations: writeTool(true),
		Description: "Restart Home Assistant. The configuration check runs first and the restart is refused if it " +
			"fails. Home Assistant is unavailable for a minute or more and every automation in progress is stopped, so " +
			"confirm with the person first and pass confirm:true. Many changes do not need a restart: automations, " +
			"scripts, scenes and helpers made through these tools apply immediately.",
	}, s.stateRestart)
}

// --- shared helpers ---

// Camera and media player states carry an access token in access_token and in
// the entity_picture URL. It opens the camera stream and proxy to anyone who
// holds it, without the admin token, so it must never reach the model or
// anything the model writes elsewhere.
var stateTokenParam = regexp.MustCompile(`([?&](?:token|authSig)=)[^&\s"'<>]+`)

func stateSanitize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "access_token" {
				continue
			}
			if str, ok := val.(string); ok && strings.HasPrefix(k, "entity_picture") && stateTokenParam.MatchString(str) {
				continue
			}
			out[k] = stateSanitize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stateSanitize(val)
		}
		return out
	case string:
		return stateTokenParam.ReplaceAllString(t, "${1}REDACTED")
	default:
		return v
	}
}

func stateSanitizeAttrs(attrs map[string]any) map[string]any {
	if attrs == nil {
		return nil
	}
	return stateSanitize(attrs).(map[string]any)
}

// stateSanitizeJSON returns sanitized compact JSON, or the input unchanged when
// it is not JSON.
func stateSanitizeJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return stateTokenParam.ReplaceAllString(string(raw), "${1}REDACTED")
	}
	b, _ := json.Marshal(stateSanitize(v))
	return string(b)
}

func stateValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func stateNum(f float64) string {
	return strconv.FormatFloat(math.Round(f*1000)/1000, 'f', -1, 64)
}

func stateDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m != 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	if h := int(d.Hours()) % 24; h != 0 {
		return fmt.Sprintf("%dd%dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}

func (s *Server) stateTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(s.now().Location()).Format("2006-01-02 15:04:05")
}

func (s *Server) stateAgo(t time.Time) string {
	return s.stateTime(t) + " (" + stateDuration(s.now().Sub(t)) + " ago)"
}

func (s *Server) stateZone() string {
	return s.now().Format("UTC-07:00")
}

var (
	stateRelTime  = regexp.MustCompile(`^-(\d+)(h|min)$`)
	stateDateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// stateParseTime extends parseDate with the sub-day forms history questions
// use ("-6h", "-30min", a date with a time). "m" stays months, as in
// parseDate, so the two never disagree about what "-3m" means.
func stateParseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "now") {
		return now, nil
	}
	if m := stateRelTime.FindStringSubmatch(strings.ToLower(s)); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := time.Hour
		if m[2] == "min" {
			unit = time.Minute
		}
		return now.Add(-time.Duration(n) * unit), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	t, err := parseDate(s, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("could not read %q as a time; use YYYY-MM-DD, 'YYYY-MM-DD HH:MM', an ISO timestamp, 'now', 'today', 'yesterday', or an offset like '-6h', '-30min', '-7d'", s)
	}
	return t, nil
}

// stateRange resolves start and end, with start defaulting to def before end.
func (s *Server) stateRange(start, end string, def time.Duration) (time.Time, time.Time, error) {
	now := s.now()
	e := now
	if end != "" {
		t, err := stateParseTime(end, now)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		e = t
	}
	st := e.Add(-def)
	if start != "" {
		t, err := stateParseTime(start, now)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		st = t
	}
	if !st.Before(e) {
		return time.Time{}, time.Time{}, fmt.Errorf("start %s is not before end %s", st.Format(time.RFC3339), e.Format(time.RFC3339))
	}
	return st, e, nil
}

// stateAll runs the calls one after another. Concurrent WebSocket commands
// can reach HA with message ids out of order, which it rejects with id_reuse.
func stateAll(fns ...func() error) error {
	for _, f := range fns {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

type stateRegistry struct {
	entities map[string]homeassistant.EntityRegistryEntry
	devices  map[string]homeassistant.Device
	areas    map[string]homeassistant.Area
	labels   map[string]homeassistant.Label
}

func (s *Server) stateLoadRegistry(ctx context.Context, extra ...func() error) (*stateRegistry, error) {
	var (
		ents    []homeassistant.EntityRegistryEntry
		devs    []homeassistant.Device
		areas   []homeassistant.Area
		labels  []homeassistant.Label
		loaders = []func() error{
			func() (err error) { ents, err = s.ha.ListEntityRegistry(ctx); return },
			func() (err error) { devs, err = s.ha.ListDevices(ctx); return },
			func() (err error) { areas, err = s.ha.ListAreas(ctx); return },
			func() (err error) { labels, err = s.ha.ListLabels(ctx); return },
		}
	)
	if err := stateAll(append(loaders, extra...)...); err != nil {
		return nil, err
	}
	r := &stateRegistry{
		entities: make(map[string]homeassistant.EntityRegistryEntry, len(ents)),
		devices:  make(map[string]homeassistant.Device, len(devs)),
		areas:    make(map[string]homeassistant.Area, len(areas)),
		labels:   make(map[string]homeassistant.Label, len(labels)),
	}
	for _, e := range ents {
		r.entities[e.EntityID] = e
	}
	for _, d := range devs {
		r.devices[d.ID] = d
	}
	for _, a := range areas {
		r.areas[a.AreaID] = a
	}
	for _, l := range labels {
		r.labels[l.LabelID] = l
	}
	return r, nil
}

// areaID follows HA's own rule: an entity's area overrides its device's.
func (r *stateRegistry) areaID(entityID string) string {
	e, ok := r.entities[entityID]
	if !ok {
		return ""
	}
	if e.AreaID != nil && *e.AreaID != "" {
		return *e.AreaID
	}
	if e.DeviceID != nil {
		if d, ok := r.devices[*e.DeviceID]; ok && d.AreaID != nil {
			return *d.AreaID
		}
	}
	return ""
}

func (r *stateRegistry) areaName(entityID string) string {
	id := r.areaID(entityID)
	if a, ok := r.areas[id]; ok {
		return a.Name
	}
	return id
}

func (r *stateRegistry) labelNames(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if l, ok := r.labels[id]; ok {
			out = append(out, l.Name)
		} else {
			out = append(out, id)
		}
	}
	return out
}

func stateName(st homeassistant.State) string {
	if n, ok := st.Attributes["friendly_name"].(string); ok && n != "" {
		return n
	}
	return st.EntityID
}

func stateWithUnit(st homeassistant.State) string {
	v := truncate(st.State, 80)
	if u, ok := st.Attributes["unit_of_measurement"].(string); ok && u != "" {
		v += " " + u
	}
	return v
}

func stateStrings(in []string) []string {
	var out []string
	for _, v := range in {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// --- ha_list_entities ---

type stateListEntitiesInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"only this domain, e.g. light, sensor, binary_sensor; several separated by commas"`
	Area   string `json:"area,omitempty" jsonschema:"only entities in this area (area id or name); an entity's own area overrides its device's"`
	Label  string `json:"label,omitempty" jsonschema:"only entities with this label (label id or name), directly or through their device"`
	Device string `json:"device,omitempty" jsonschema:"only entities of this device (device id or name)"`
	Search string `json:"search,omitempty" jsonschema:"words that must all appear in the entity id or name, case-insensitive"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum lines to return, default 200, at most 2000"`
}

func (s *Server) stateListEntities(ctx context.Context, _ *mcp.CallToolRequest, in stateListEntitiesInput) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 2000)

	var states []homeassistant.State
	reg, err := s.stateLoadRegistry(ctx, func() (err error) { states, err = s.ha.ListStates(ctx); return })
	if err != nil {
		return fail(err)
	}

	domains := stateStrings([]string{in.Domain})
	var areaIDs, labelIDs, deviceIDs map[string]bool
	if in.Area != "" {
		if areaIDs, err = stateMatchRegistry("area", in.Area, reg.areas, func(a homeassistant.Area) (string, string) { return a.AreaID, a.Name }); err != nil {
			return fail(err)
		}
	}
	if in.Label != "" {
		if labelIDs, err = stateMatchRegistry("label", in.Label, reg.labels, func(l homeassistant.Label) (string, string) { return l.LabelID, l.Name }); err != nil {
			return fail(err)
		}
	}
	if in.Device != "" {
		if deviceIDs, err = stateMatchRegistry("device", in.Device, reg.devices, func(d homeassistant.Device) (string, string) { return d.ID, d.DisplayName() }); err != nil {
			return fail(err)
		}
	}
	words := strings.Fields(strings.ToLower(in.Search))

	sort.Slice(states, func(i, j int) bool { return states[i].EntityID < states[j].EntityID })
	var matched []homeassistant.State
	for _, st := range states {
		if len(domains) > 0 && !slices.Contains(domains, st.Domain()) {
			continue
		}
		e, inReg := reg.entities[st.EntityID]
		if areaIDs != nil && !areaIDs[reg.areaID(st.EntityID)] {
			continue
		}
		if deviceIDs != nil && (!inReg || e.DeviceID == nil || !deviceIDs[*e.DeviceID]) {
			continue
		}
		if labelIDs != nil && !stateHasLabel(reg, e, labelIDs) {
			continue
		}
		if len(words) > 0 {
			hay := strings.ToLower(st.EntityID + " " + stateName(st))
			if !stateAllWords(hay, words) {
				continue
			}
		}
		matched = append(matched, st)
	}

	if len(matched) == 0 {
		return text("No entities match these filters."), nil, nil
	}
	var b strings.Builder
	if len(matched) > limit {
		fmt.Fprintf(&b, "%d entities match; showing the first %d.\n", len(matched), limit)
	} else {
		fmt.Fprintf(&b, "%d entities:\n", len(matched))
	}
	for _, st := range matched[:min(limit, len(matched))] {
		fmt.Fprintf(&b, "%s | %s | %s", st.EntityID, truncate(stateName(st), 60), stateWithUnit(st))
		if a := reg.areaName(st.EntityID); a != "" {
			fmt.Fprintf(&b, " | %s", a)
		}
		if e, ok := reg.entities[st.EntityID]; ok && e.HiddenBy != nil {
			b.WriteString(" | hidden")
		}
		b.WriteString("\n")
	}
	if len(matched) > limit {
		fmt.Fprintf(&b, "… %d more not shown. Narrow with domain, area or search, or raise limit.\n", len(matched)-limit)
		if len(domains) == 0 {
			counts := map[string]int{}
			for _, st := range matched {
				counts[st.Domain()]++
			}
			keys := make([]string, 0, len(counts))
			for k := range counts {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				return counts[keys[i]] > counts[keys[j]] || counts[keys[i]] == counts[keys[j]] && keys[i] < keys[j]
			})
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = fmt.Sprintf("%s %d", k, counts[k])
			}
			fmt.Fprintf(&b, "By domain: %s\n", strings.Join(parts, ", "))
		}
	}
	return text(b.String()), nil, nil
}

func stateAllWords(hay string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func stateHasLabel(reg *stateRegistry, e homeassistant.EntityRegistryEntry, want map[string]bool) bool {
	for _, l := range e.Labels {
		if want[l] {
			return true
		}
	}
	if e.DeviceID != nil {
		for _, l := range reg.devices[*e.DeviceID].Labels {
			if want[l] {
				return true
			}
		}
	}
	return false
}

// stateMatchRegistry resolves a filter given as an id or a name. An exact id or
// name wins; otherwise a name containing the text matches, which is how a
// person refers to "the kitchen" when the area is called "Kitchen & Dining".
func stateMatchRegistry[T any](kind, q string, items map[string]T, idName func(T) (string, string)) (map[string]bool, error) {
	ql := strings.ToLower(strings.TrimSpace(q))
	exact, partial := map[string]bool{}, map[string]bool{}
	var names []string
	for _, it := range items {
		id, name := idName(it)
		names = append(names, name)
		switch {
		case strings.ToLower(id) == ql || strings.ToLower(name) == ql:
			exact[id] = true
		case strings.Contains(strings.ToLower(name), ql):
			partial[id] = true
		}
	}
	if len(exact) > 0 {
		return exact, nil
	}
	if len(partial) > 0 {
		return partial, nil
	}
	sort.Strings(names)
	if kind == "device" {
		return nil, fmt.Errorf("no device matches %q; list devices by name or id with the device tools", q)
	}
	return nil, fmt.Errorf("no %s matches %q; the %ss are: %s", kind, q, kind, strings.Join(names, ", "))
}

// --- ha_get_state ---

type stateGetStateInput struct {
	EntityIDs []string `json:"entity_ids" jsonschema:"entity ids like light.kitchen, at most 50"`
}

func (s *Server) stateGetState(ctx context.Context, _ *mcp.CallToolRequest, in stateGetStateInput) (*mcp.CallToolResult, any, error) {
	ids := stateStrings(in.EntityIDs)
	if len(ids) == 0 {
		return fail(errors.New("entity_ids is required"))
	}
	if len(ids) > 50 {
		return fail(fmt.Errorf("%d entities is more than the 50 a call takes", len(ids)))
	}
	states := make([]*homeassistant.State, len(ids))
	errs := make([]error, len(ids))
	fns := make([]func() error, len(ids))
	for i, id := range ids {
		fns[i] = func() error { states[i], errs[i] = s.ha.GetState(ctx, id); return nil }
	}
	reg, err := s.stateLoadRegistry(ctx, fns...)
	if err != nil {
		return fail(err)
	}

	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteString("\n")
		}
		if errs[i] != nil {
			if homeassistant.IsNotFound(errs[i]) {
				if e, ok := reg.entities[id]; ok && e.DisabledBy != nil {
					fmt.Fprintf(&b, "%s: disabled (by %s), so it has no state\n", id, *e.DisabledBy)
				} else {
					fmt.Fprintf(&b, "%s: no such entity\n", id)
				}
				continue
			}
			fmt.Fprintf(&b, "%s: %v\n", id, errs[i])
			continue
		}
		st := states[i]
		fmt.Fprintf(&b, "%s (%s)\n", st.EntityID, stateName(*st))
		fmt.Fprintf(&b, "state: %s\n", stateWithUnit(*st))
		fmt.Fprintf(&b, "last_changed: %s\n", s.stateAgo(st.LastChanged))
		if !st.LastUpdated.Equal(st.LastChanged) {
			fmt.Fprintf(&b, "last_updated: %s\n", s.stateAgo(st.LastUpdated))
		}
		if e, ok := reg.entities[id]; ok {
			s.stateWriteRegistry(&b, reg, e)
		} else {
			b.WriteString("registry: not in the entity registry (no unique id), so it cannot have an area or labels\n")
		}
		attrs := stateSanitizeAttrs(st.Attributes)
		keys := make([]string, 0, len(attrs))
		for k, v := range attrs {
			if k == "friendly_name" || k == "unit_of_measurement" || v == nil {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			b.WriteString("attributes:\n")
			for _, k := range keys {
				fmt.Fprintf(&b, "  %s: %s\n", k, truncate(stateValue(attrs[k]), 2000))
			}
		}
	}
	return text(b.String()), nil, nil
}

func (s *Server) stateWriteRegistry(b *strings.Builder, reg *stateRegistry, e homeassistant.EntityRegistryEntry) {
	var parts []string
	if a := reg.areaName(e.EntityID); a != "" {
		parts = append(parts, fmt.Sprintf("area %s (%s)", a, reg.areaID(e.EntityID)))
	}
	if e.DeviceID != nil {
		if d, ok := reg.devices[*e.DeviceID]; ok {
			desc := d.DisplayName()
			if d.Model != nil && *d.Model != "" {
				desc += ", " + *d.Model
			}
			if d.Manufacturer != nil && *d.Manufacturer != "" {
				desc += " by " + *d.Manufacturer
			}
			parts = append(parts, fmt.Sprintf("device %s (id %s)", desc, d.ID))
		}
	}
	if len(e.Labels) > 0 {
		parts = append(parts, "labels "+strings.Join(reg.labelNames(e.Labels), ", "))
	}
	parts = append(parts, "platform "+e.Platform)
	if e.EntityCategory != nil {
		parts = append(parts, "category "+*e.EntityCategory)
	}
	if e.DisabledBy != nil {
		parts = append(parts, "disabled by "+*e.DisabledBy)
	}
	if e.HiddenBy != nil {
		parts = append(parts, "hidden by "+*e.HiddenBy)
	}
	fmt.Fprintf(b, "registry: %s\n", strings.Join(parts, " | "))
}

// --- ha_list_services ---

type stateListServicesInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"a domain like light, or domain.service like light.turn_on; several separated by commas. Omit to list all domains"`
}

type stateServiceField struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Required    bool                       `json:"required"`
	Example     any                        `json:"example"`
	Default     any                        `json:"default"`
	Selector    map[string]json.RawMessage `json:"selector"`
	Fields      map[string]json.RawMessage `json:"fields"`
}

func (s *Server) stateListServices(ctx context.Context, _ *mcp.CallToolRequest, in stateListServicesInput) (*mcp.CallToolResult, any, error) {
	all, err := s.ha.ListServices(ctx)
	if err != nil {
		return fail(err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Domain < all[j].Domain })

	wanted := stateStrings([]string{in.Domain})
	if len(wanted) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d domains. Call again with a domain for the fields of its services.\n", len(all))
		for _, d := range all {
			fmt.Fprintf(&b, "%s: %s\n", d.Domain, strings.Join(stateSortedKeys(d.Services), ", "))
		}
		return text(b.String()), nil, nil
	}

	var b strings.Builder
	for _, w := range wanted {
		domain, service, _ := strings.Cut(w, ".")
		i := slices.IndexFunc(all, func(d homeassistant.ServiceDomain) bool { return d.Domain == domain })
		if i < 0 {
			fmt.Fprintf(&b, "%s: no such domain\n", domain)
			continue
		}
		d := all[i]
		for _, name := range stateSortedKeys(d.Services) {
			if service != "" && name != service {
				continue
			}
			stateWriteService(&b, d.Domain, name, d.Services[name])
		}
		if service != "" {
			if _, ok := d.Services[service]; !ok {
				fmt.Fprintf(&b, "%s.%s: no such service; %s has %s\n", domain, service, domain, strings.Join(stateSortedKeys(d.Services), ", "))
			}
		}
	}
	return text(b.String()), nil, nil
}

func stateSortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stateWriteService(b *strings.Builder, domain, name string, svc homeassistant.ServiceDescription) {
	fmt.Fprintf(b, "%s.%s", domain, name)
	if svc.Name != "" && !strings.EqualFold(svc.Name, name) {
		fmt.Fprintf(b, " (%s)", svc.Name)
	}
	if t := stateDescribeTarget(svc.Target); t != "" {
		fmt.Fprintf(b, " | target: %s", t)
	}
	if svc.Response != nil {
		if svc.Response.Optional {
			b.WriteString(" | can return a response (return_response)")
		} else {
			b.WriteString(" | returns data: call with return_response:true")
		}
	}
	b.WriteString("\n")
	if svc.Description != "" {
		fmt.Fprintf(b, "  %s\n", truncate(svc.Description, 300))
	}
	stateWriteFields(b, svc.Fields, "  ")
}

func stateWriteFields(b *strings.Builder, fields map[string]json.RawMessage, indent string) {
	for _, name := range stateSortedKeys(fields) {
		var f stateServiceField
		if json.Unmarshal(fields[name], &f) != nil {
			continue
		}
		// Sections ("advanced options") group more fields; they are called
		// with those fields directly, so flatten them.
		if f.Fields != nil && f.Selector == nil {
			stateWriteFields(b, f.Fields, indent)
			continue
		}
		var parts []string
		if f.Required {
			parts = append(parts, "required")
		}
		if sel := stateDescribeSelector(f.Selector); sel != "" {
			parts = append(parts, sel)
		}
		if f.Default != nil {
			parts = append(parts, "default "+truncate(stateValue(f.Default), 60))
		}
		if f.Example != nil {
			parts = append(parts, "e.g. "+truncate(stateValue(f.Example), 80))
		}
		fmt.Fprintf(b, "%s%s: %s", indent, name, strings.Join(parts, ", "))
		if f.Description != "" {
			fmt.Fprintf(b, " | %s", truncate(f.Description, 200))
		}
		b.WriteString("\n")
	}
}

func stateDescribeSelector(sel map[string]json.RawMessage) string {
	for kind, raw := range sel {
		var opts map[string]any
		_ = json.Unmarshal(raw, &opts)
		switch kind {
		case "number":
			out := "number"
			if mn, ok := opts["min"].(float64); ok {
				out += " " + stateNum(mn)
				if mx, ok := opts["max"].(float64); ok {
					out += "–" + stateNum(mx)
				}
			}
			if u, ok := opts["unit_of_measurement"].(string); ok {
				out += " " + u
			}
			return out
		case "select":
			var vals []string
			if list, ok := opts["options"].([]any); ok {
				for _, o := range list {
					switch v := o.(type) {
					case string:
						vals = append(vals, v)
					case map[string]any:
						vals = append(vals, stateValue(v["value"]))
					}
				}
			}
			if len(vals) == 0 {
				return "select"
			}
			return "one of " + truncate(strings.Join(vals, "|"), 300)
		case "entity":
			if f, ok := opts["filter"]; ok {
				return "entity id " + truncate(stateValue(f), 120)
			}
			if d, ok := opts["domain"]; ok {
				return "entity id (" + stateValue(d) + ")"
			}
			return "entity id"
		case "boolean":
			return "true/false"
		case "text":
			if m, ok := opts["multiple"].(bool); ok && m {
				return "list of text"
			}
			return "text"
		case "color_rgb":
			return "[r, g, b]"
		case "color_temp":
			if u, ok := opts["unit"].(string); ok {
				return "color temperature (" + u + ")"
			}
			return "color temperature"
		case "object":
			return "object"
		case "state":
			if a, ok := opts["attribute"].(string); ok {
				return "a value of the entity's " + a + " attribute"
			}
			return "a state of the target entity"
		case "constant":
			if v, ok := opts["value"]; ok {
				return "constant " + stateValue(v)
			}
			return "constant"
		default:
			return kind
		}
	}
	return ""
}

func stateDescribeTarget(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var t map[string][]map[string]any
	if json.Unmarshal(raw, &t) != nil {
		return "entities"
	}
	var parts []string
	for _, kind := range []string{"entity", "device"} {
		sels, ok := t[kind]
		if !ok {
			continue
		}
		var doms []string
		for _, s := range sels {
			for _, k := range []string{"domain", "integration"} {
				switch v := s[k].(type) {
				case []any:
					for _, x := range v {
						doms = append(doms, stateValue(x))
					}
				case string:
					doms = append(doms, v)
				}
			}
		}
		if len(doms) > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", kind, strings.Join(slices.Compact(doms), ", ")))
		} else {
			parts = append(parts, kind)
		}
	}
	if len(parts) == 0 {
		return "entities, devices, areas"
	}
	return strings.Join(parts, "; ")
}

// --- ha_render_template ---

type stateRenderTemplateInput struct {
	Template  string         `json:"template" jsonschema:"the Jinja template, e.g. {{ states('sensor.outside_temperature') }}"`
	Variables map[string]any `json:"variables,omitempty" jsonschema:"variables available to the template"`
}

func (s *Server) stateRenderTemplate(ctx context.Context, _ *mcp.CallToolRequest, in stateRenderTemplateInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Template) == "" {
		return fail(errors.New("template is required"))
	}
	var (
		out    string
		states []homeassistant.State
	)
	err := stateAll(
		func() (err error) { out, err = s.ha.RenderTemplate(ctx, in.Template, in.Variables); return },
		func() (err error) { states, err = s.ha.ListStates(ctx); return },
	)
	if err != nil {
		return fail(err)
	}
	// A template can read any attribute, so the camera tokens are scrubbed
	// from the output by value as well as by the URL pattern.
	for _, st := range states {
		if tok, ok := st.Attributes["access_token"].(string); ok && len(tok) >= 8 {
			out = strings.ReplaceAll(out, tok, "REDACTED")
		}
	}
	out = stateTokenParam.ReplaceAllString(out, "${1}REDACTED")
	if len(out) > 50000 {
		out = out[:50000] + "\n… output cut at 50000 characters"
	}
	if out == "" {
		return text("(the template rendered to an empty string)"), nil, nil
	}
	return text(out), nil, nil
}

// --- ha_history ---

type stateHistoryInput struct {
	EntityIDs []string `json:"entity_ids" jsonschema:"entity ids, at most 20"`
	Start     string   `json:"start,omitempty" jsonschema:"start of the range: ISO timestamp, 'YYYY-MM-DD HH:MM', 'today', 'yesterday', or an offset like '-6h', '-30min', '-7d'; default 24 hours before end"`
	End       string   `json:"end,omitempty" jsonschema:"end of the range, same forms as start; default now"`
	Attribute string   `json:"attribute,omitempty" jsonschema:"follow this attribute (e.g. current_temperature, brightness) instead of the state"`
	MaxPoints int      `json:"max_points,omitempty" jsonschema:"most lines per entity, default 48; a longer numeric series is reduced to this many time buckets, a longer list of changes keeps the newest"`
}

type stateHistPoint struct {
	t time.Time
	v string
}

func (s *Server) stateHistory(ctx context.Context, _ *mcp.CallToolRequest, in stateHistoryInput) (*mcp.CallToolResult, any, error) {
	ids := stateStrings(in.EntityIDs)
	if len(ids) == 0 {
		return fail(errors.New("entity_ids is required"))
	}
	if len(ids) > 20 {
		return fail(fmt.Errorf("%d entities is more than the 20 a call takes", len(ids)))
	}
	start, end, err := s.stateRange(in.Start, in.End, 24*time.Hour)
	if err != nil {
		return fail(err)
	}
	maxPoints := in.MaxPoints
	if maxPoints <= 0 {
		maxPoints = 48
	}
	maxPoints = min(maxPoints, 500)

	req := homeassistant.HistoryRequest{EntityIDs: ids, Start: start, End: end, MinimalResponse: true}
	if in.Attribute != "" {
		req.AllChanges, req.MinimalResponse = true, false
	}
	hist, err := s.ha.History(ctx, req)
	if err != nil {
		return fail(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "History %s → %s (times %s)\n", s.stateTime(start), s.stateTime(end), s.stateZone())
	for _, id := range ids {
		b.WriteString("\n")
		entries := hist[id]
		if len(entries) == 0 {
			fmt.Fprintf(&b, "%s: no recorded history in this range (unknown entity, excluded from the recorder, or purged; try ha_statistics for older data)\n", id)
			continue
		}
		name, unit := id, ""
		if a := entries[0].Attributes; a != nil {
			if n, ok := a["friendly_name"].(string); ok {
				name = n
			}
			if u, ok := a["unit_of_measurement"].(string); ok {
				unit = u
			}
		}
		var pts []stateHistPoint
		for _, e := range entries {
			v := e.State
			if in.Attribute != "" {
				av, ok := e.Attributes[in.Attribute]
				if !ok {
					continue
				}
				v = stateValue(stateSanitize(av))
				unit = ""
			}
			t := e.LastChanged
			if in.Attribute != "" {
				t = e.LastUpdated
			}
			if len(pts) > 0 && pts[len(pts)-1].v == v {
				continue
			}
			if t.Before(start) {
				t = start
			}
			pts = append(pts, stateHistPoint{t, v})
		}
		label := id
		if name != id {
			label = fmt.Sprintf("%s (%s)", id, name)
		}
		if in.Attribute != "" {
			label += " attribute " + in.Attribute
		}
		if len(pts) == 0 {
			fmt.Fprintf(&b, "%s: the attribute never appears in this range\n", label)
			continue
		}
		if stateNumeric(pts) {
			s.stateWriteNumeric(&b, label, unit, pts, end, maxPoints)
		} else {
			s.stateWriteDiscrete(&b, label, unit, pts, end, maxPoints)
		}
	}
	return text(b.String()), nil, nil
}

func stateIsGap(v string) bool { return v == "unavailable" || v == "unknown" || v == "" || v == "null" }

func stateNumeric(pts []stateHistPoint) bool {
	n := 0
	for _, p := range pts {
		if stateIsGap(p.v) {
			continue
		}
		if _, err := strconv.ParseFloat(p.v, 64); err != nil {
			return false
		}
		n++
	}
	return n > 0
}

func (s *Server) stateWriteNumeric(b *strings.Builder, label, unit string, pts []stateHistPoint, end time.Time, maxPoints int) {
	u := ""
	if unit != "" {
		u = " " + unit
	}
	type num struct {
		t time.Time
		f float64
	}
	var nums []num
	gaps := 0
	for _, p := range pts {
		if stateIsGap(p.v) {
			gaps++
			continue
		}
		f, _ := strconv.ParseFloat(p.v, 64)
		nums = append(nums, num{p.t, f})
	}
	lo, hi := nums[0], nums[0]
	var weighted, total float64
	for i, n := range nums {
		if n.f < lo.f {
			lo = n
		}
		if n.f > hi.f {
			hi = n
		}
		next := end
		if i+1 < len(nums) {
			next = nums[i+1].t
		}
		d := next.Sub(n.t).Seconds()
		weighted += n.f * d
		total += d
	}
	avg := nums[len(nums)-1].f
	if total > 0 {
		avg = weighted / total
	}
	fmt.Fprintf(b, "%s: %d values, min %s%s at %s, max %s%s at %s, time-weighted avg %s%s, last %s%s at %s\n",
		label, len(nums), stateNum(lo.f), u, s.stateTime(lo.t), stateNum(hi.f), u, s.stateTime(hi.t),
		stateNum(avg), u, stateNum(nums[len(nums)-1].f), u, s.stateTime(nums[len(nums)-1].t))
	if gaps > 0 {
		fmt.Fprintf(b, "  unavailable or unknown %d times\n", gaps)
	}
	if len(pts) <= maxPoints {
		for _, p := range pts {
			fmt.Fprintf(b, "  %s %s\n", s.stateTime(p.t), p.v)
		}
		return
	}
	first := pts[0].t
	step := end.Sub(first) / time.Duration(maxPoints)
	if step <= 0 {
		step = time.Second
	}
	fmt.Fprintf(b, "  %d buckets of %s (bucket start: min/avg/max, n):\n", maxPoints, stateDuration(step))
	for i := range maxPoints {
		bs, be := first.Add(time.Duration(i)*step), first.Add(time.Duration(i+1)*step)
		var mn, mx, sum float64
		n := 0
		for _, x := range nums {
			if x.t.Before(bs) || !x.t.Before(be) {
				continue
			}
			if n == 0 || x.f < mn {
				mn = x.f
			}
			if n == 0 || x.f > mx {
				mx = x.f
			}
			sum += x.f
			n++
		}
		if n == 0 {
			continue
		}
		fmt.Fprintf(b, "  %s %s/%s/%s (%d)\n", s.stateTime(bs), stateNum(mn), stateNum(sum/float64(n)), stateNum(mx), n)
	}
}

func (s *Server) stateWriteDiscrete(b *strings.Builder, label, unit string, pts []stateHistPoint, end time.Time, maxPoints int) {
	durations := map[string]time.Duration{}
	for i, p := range pts {
		next := end
		if i+1 < len(pts) {
			next = pts[i+1].t
		}
		durations[p.v] += next.Sub(p.t)
	}
	keys := stateSortedKeys(durations)
	sort.SliceStable(keys, func(i, j int) bool { return durations[keys[i]] > durations[keys[j]] })
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %s", truncate(k, 60), stateDuration(durations[k]))
	}
	fmt.Fprintf(b, "%s: %d changes; time in state: %s\n", label, len(pts)-1, strings.Join(parts, ", "))
	shown := pts
	if len(pts) > maxPoints {
		fmt.Fprintf(b, "  … %d earlier changes not shown\n", len(pts)-maxPoints)
		shown = pts[len(pts)-maxPoints:]
	}
	u := ""
	if unit != "" {
		u = " " + unit
	}
	for i, p := range shown {
		next := end
		if i+1 < len(shown) {
			next = shown[i+1].t
		}
		fmt.Fprintf(b, "  %s %s%s (for %s)\n", s.stateTime(p.t), truncate(p.v, 200), u, stateDuration(next.Sub(p.t)))
	}
}

// --- ha_logbook ---

type stateLogbookInput struct {
	Start     string   `json:"start,omitempty" jsonschema:"start of the range: ISO timestamp, 'YYYY-MM-DD HH:MM', 'today', 'yesterday', or an offset like '-6h', '-7d'; default 24 hours before end"`
	End       string   `json:"end,omitempty" jsonschema:"end of the range, same forms as start; default now"`
	EntityIDs []string `json:"entity_ids,omitempty" jsonschema:"only entries for these entities"`
	DeviceIDs []string `json:"device_ids,omitempty" jsonschema:"only entries for these devices"`
	Limit     int      `json:"limit,omitempty" jsonschema:"most entries to return, newest kept, default 200, at most 2000"`
}

func (s *Server) stateLogbook(ctx context.Context, _ *mcp.CallToolRequest, in stateLogbookInput) (*mcp.CallToolResult, any, error) {
	start, end, err := s.stateRange(in.Start, in.End, 24*time.Hour)
	if err != nil {
		return fail(err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 2000)
	entries, err := s.ha.Logbook(ctx, homeassistant.LogbookRequest{
		Start: start, End: end, EntityIDs: stateStrings(in.EntityIDs), DeviceIDs: stateStrings(in.DeviceIDs),
	})
	if err != nil {
		return fail(err)
	}
	if len(entries) == 0 {
		return text(fmt.Sprintf("No logbook entries %s → %s.", s.stateTime(start), s.stateTime(end))), nil, nil
	}

	users := map[string]string{}
	if slices.ContainsFunc(entries, func(e homeassistant.LogbookEntry) bool { return e.ContextUserID != "" }) {
		if p, err := s.ha.ListPersons(ctx); err == nil {
			for _, person := range append(p.Storage, p.Config...) {
				if person.UserID != nil {
					users[*person.UserID] = person.Name
				}
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Logbook %s → %s (times %s): %d entries", s.stateTime(start), s.stateTime(end), s.stateZone(), len(entries))
	if len(entries) > limit {
		fmt.Fprintf(&b, ", showing the newest %d", limit)
		entries = entries[len(entries)-limit:]
	}
	b.WriteString("\n")
	for _, e := range entries {
		what := e.Message
		if what == "" && e.State != "" {
			what = "changed to " + e.State
		}
		who := e.Name
		switch {
		case e.EntityID != "" && e.Name != "" && e.Name != e.EntityID:
			who = fmt.Sprintf("%s (%s)", e.Name, e.EntityID)
		case e.EntityID != "":
			who = e.EntityID
		}
		fmt.Fprintf(&b, "%s | %s %s", s.stateTime(e.When), who, truncate(what, 200))
		if c := stateLogbookCause(e, users); c != "" {
			fmt.Fprintf(&b, " | %s", c)
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil, nil
}

func stateLogbookCause(e homeassistant.LogbookEntry, users map[string]string) string {
	var parts []string
	switch {
	case e.ContextEntityID != "":
		name := e.ContextEntityIDName
		if name == "" {
			name = e.ContextName
		}
		if name != "" && name != e.ContextEntityID {
			parts = append(parts, fmt.Sprintf("triggered by %s (%s)", name, e.ContextEntityID))
		} else {
			parts = append(parts, "triggered by "+e.ContextEntityID)
		}
		if e.ContextMessage != "" {
			parts = append(parts, truncate(e.ContextMessage, 120))
		}
	case e.ContextEventType == "call_service" && e.ContextDomain != "":
		parts = append(parts, fmt.Sprintf("service %s.%s", e.ContextDomain, e.ContextService))
	case e.ContextEventType != "" && e.ContextEventType != "state_changed":
		parts = append(parts, "event "+e.ContextEventType)
	}
	if e.ContextUserID != "" {
		if n, ok := users[e.ContextUserID]; ok {
			parts = append(parts, "by "+n)
		} else {
			parts = append(parts, "by user "+e.ContextUserID)
		}
	}
	return strings.Join(parts, ", ")
}

// --- ha_statistics ---

type stateStatisticsInput struct {
	StatisticIDs []string `json:"statistic_ids,omitempty" jsonschema:"statistic ids (usually entity ids like sensor.energy_total); omit to list the available statistics"`
	Search       string   `json:"search,omitempty" jsonschema:"when listing, only statistics whose id or name contains all these words"`
	Start        string   `json:"start,omitempty" jsonschema:"start of the range: 'YYYY-MM-DD', ISO timestamp, or an offset like '-7d', '-3m', '-1y'; default 7 days before end"`
	End          string   `json:"end,omitempty" jsonschema:"end of the range, same forms as start; default now"`
	Period       string   `json:"period,omitempty" jsonschema:"hour, day, week or month (also 5minute, year); default picked from the range length"`
	Types        []string `json:"types,omitempty" jsonschema:"values to return: mean, min, max, sum, change, state; default mean/min/max for measurements and change for meters"`
}

func (s *Server) stateStatistics(ctx context.Context, _ *mcp.CallToolRequest, in stateStatisticsInput) (*mcp.CallToolResult, any, error) {
	meta, err := s.ha.ListStatisticIDs(ctx, "")
	if err != nil {
		return fail(err)
	}
	metaByID := make(map[string]homeassistant.StatisticMetadata, len(meta))
	for _, m := range meta {
		metaByID[m.StatisticID] = m
	}

	ids := stateStrings(in.StatisticIDs)
	if len(ids) == 0 {
		return s.stateListStatistics(meta, in.Search)
	}
	if len(ids) > 20 {
		return fail(fmt.Errorf("%d statistics is more than the 20 a call takes", len(ids)))
	}
	start, end, err := s.stateRange(in.Start, in.End, 7*24*time.Hour)
	if err != nil {
		return fail(err)
	}
	period := in.Period
	if period == "" {
		switch span := end.Sub(start); {
		case span <= 2*24*time.Hour:
			period = "hour"
		case span <= 92*24*time.Hour:
			period = "day"
		case span <= 400*24*time.Hour:
			period = "week"
		default:
			period = "month"
		}
	}
	types := stateStrings(in.Types)
	if len(types) == 0 {
		types = []string{"mean", "min", "max", "change"}
	}
	stats, err := s.ha.StatisticsDuringPeriod(ctx, homeassistant.StatisticsRequest{
		StatisticIDs: ids, Start: start, End: end, Period: period, Types: types,
	})
	if err != nil {
		return fail(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Statistics per %s, %s → %s (times %s)\n", period, s.stateTime(start), s.stateTime(end), s.stateZone())
	for _, id := range ids {
		b.WriteString("\n")
		m, known := metaByID[id]
		rows := stats[id]
		if !known {
			fmt.Fprintf(&b, "%s: no such statistic (only sensors with a state_class have statistics; call without statistic_ids to list them)\n", id)
			continue
		}
		unit := ""
		if m.StatisticsUnitOfMeasurement != nil {
			unit = *m.StatisticsUnitOfMeasurement
		}
		if m.DisplayUnitOfMeasurement != nil && *m.DisplayUnitOfMeasurement != "" {
			unit = *m.DisplayUnitOfMeasurement
		}
		name := id
		if m.Name != nil && *m.Name != "" {
			name = fmt.Sprintf("%s (%s)", id, *m.Name)
		}
		if unit != "" {
			name += " in " + unit
		}
		if len(rows) == 0 {
			fmt.Fprintf(&b, "%s: no data in this range\n", name)
			continue
		}
		fmt.Fprintf(&b, "%s: %d rows\n", name, len(rows))
		var totalChange float64
		hasChange := false
		shown := rows
		if len(rows) > 500 {
			fmt.Fprintf(&b, "  … %d earlier rows not shown; use a longer period\n", len(rows)-500)
			shown = rows[len(rows)-500:]
		}
		for _, r := range rows {
			if r.Change != nil {
				totalChange += *r.Change
				hasChange = true
			}
		}
		for _, r := range shown {
			var parts []string
			for _, f := range []struct {
				name string
				v    *float64
			}{{"mean", r.Mean}, {"min", r.Min}, {"max", r.Max}, {"change", r.Change}, {"sum", r.Sum}, {"state", r.State}} {
				if f.v != nil {
					parts = append(parts, f.name+" "+stateNum(*f.v))
				}
			}
			fmt.Fprintf(&b, "  %s %s\n", s.stateStatLabel(r.Start, period), strings.Join(parts, " "))
		}
		if hasChange && len(rows) > 1 {
			fmt.Fprintf(&b, "  total change %s %s\n", stateNum(totalChange), unit)
		}
	}
	return text(b.String()), nil, nil
}

func (s *Server) stateStatLabel(t time.Time, period string) string {
	t = t.In(s.now().Location())
	switch period {
	case "day":
		return t.Format("2006-01-02 Mon")
	case "week":
		return "week of " + t.Format("2006-01-02")
	case "month":
		return t.Format("2006-01")
	case "year":
		return t.Format("2006")
	default:
		return t.Format("2006-01-02 15:04")
	}
}

func (s *Server) stateListStatistics(meta []homeassistant.StatisticMetadata, search string) (*mcp.CallToolResult, any, error) {
	words := strings.Fields(strings.ToLower(search))
	sort.Slice(meta, func(i, j int) bool { return meta[i].StatisticID < meta[j].StatisticID })
	var lines []string
	for _, m := range meta {
		name := ""
		if m.Name != nil {
			name = *m.Name
		}
		if len(words) > 0 && !stateAllWords(strings.ToLower(m.StatisticID+" "+name), words) {
			continue
		}
		kind := "mean/min/max"
		if m.HasSum {
			kind = "meter: change/sum"
		}
		unit := ""
		if m.StatisticsUnitOfMeasurement != nil {
			unit = *m.StatisticsUnitOfMeasurement
		}
		line := m.StatisticID
		if name != "" {
			line += " | " + name
		}
		lines = append(lines, fmt.Sprintf("%s | %s | %s | source %s", line, unit, kind, m.Source))
	}
	if len(lines) == 0 {
		return text("No statistics match."), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d statistics", len(lines))
	if len(lines) > 300 {
		fmt.Fprintf(&b, ", showing the first 300; narrow with search")
		lines = lines[:300]
	}
	b.WriteString(":\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n")
	return text(b.String()), nil, nil
}

// --- ha_listen_events ---

type stateListenEventsInput struct {
	EventType string `json:"event_type,omitempty" jsonschema:"event type such as zha_event, state_changed, call_service, automation_triggered; empty for all events"`
	EntityID  string `json:"entity_id,omitempty" jsonschema:"only events whose data names this entity"`
	Seconds   int    `json:"seconds,omitempty" jsonschema:"how long to listen, default 10, at most 120"`
	MaxEvents int    `json:"max_events,omitempty" jsonschema:"stop after this many events, default 25, at most 200"`
}

func (s *Server) stateListenEvents(ctx context.Context, _ *mcp.CallToolRequest, in stateListenEventsInput) (*mcp.CallToolResult, any, error) {
	secs := in.Seconds
	if secs <= 0 {
		secs = 10
	}
	if secs > 120 {
		return fail(errors.New("seconds must be at most 120"))
	}
	maxEvents := in.MaxEvents
	if maxEvents <= 0 {
		maxEvents = 25
	}
	maxEvents = min(maxEvents, 200)

	// The subscription gets the request context rather than the listening
	// window so that the window ending does not close it from another
	// goroutine; it is closed here, on return.
	sub, err := s.ha.SubscribeEvents(ctx, in.EventType)
	if err != nil {
		return fail(err)
	}
	defer sub.Close()
	lctx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()

	var events []homeassistant.Event
	byType := map[string]int{}
	seen := 0
	limitHit := false
	for !limitHit {
		ev, ok, err := sub.Next(lctx)
		if !ok {
			if err != nil && lctx.Err() == nil {
				return fail(err)
			}
			break
		}
		if err != nil {
			continue
		}
		if in.EntityID != "" && !stateEventMentions(ev.Data, in.EntityID) {
			continue
		}
		seen++
		byType[ev.EventType]++
		events = append(events, ev)
		limitHit = len(events) >= maxEvents
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}

	var b strings.Builder
	what := "all events"
	if in.EventType != "" {
		what = in.EventType
	}
	if in.EntityID != "" {
		what += " for " + in.EntityID
	}
	if limitHit {
		fmt.Fprintf(&b, "Stopped at %d events of %s (the max_events limit).\n", len(events), what)
	} else {
		fmt.Fprintf(&b, "Listened %ds for %s: %d events.\n", secs, what, seen)
	}
	if d := sub.Dropped(); d > 0 {
		fmt.Fprintf(&b, "%d events were dropped because they arrived too fast.\n", d)
	}
	if in.EventType == "" && len(byType) > 1 {
		keys := stateSortedKeys(byType)
		sort.SliceStable(keys, func(i, j int) bool { return byType[keys[i]] > byType[keys[j]] })
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s %d", k, byType[k])
		}
		fmt.Fprintf(&b, "By type: %s\n", strings.Join(parts, ", "))
	}
	if len(events) == 0 && in.EventType != "" {
		b.WriteString("Nothing fired. If you are waiting for a device, ask the person to trigger it while you listen.\n")
	}
	loc := s.now().Location()
	for _, ev := range events {
		fmt.Fprintf(&b, "%s %s %s\n", ev.TimeFired.In(loc).Format("15:04:05.000"), ev.EventType, stateDescribeEvent(ev))
	}
	return text(b.String()), nil, nil
}

func stateEventMentions(data json.RawMessage, entityID string) bool {
	var d struct {
		EntityID    any `json:"entity_id"`
		ServiceData struct {
			EntityID any `json:"entity_id"`
		} `json:"service_data"`
	}
	if json.Unmarshal(data, &d) != nil {
		return false
	}
	for _, v := range []any{d.EntityID, d.ServiceData.EntityID} {
		switch t := v.(type) {
		case string:
			if t == entityID {
				return true
			}
		case []any:
			for _, x := range t {
				if x == entityID {
					return true
				}
			}
		}
	}
	return false
}

// stateDescribeEvent keeps state_changed to one readable line, because the
// full payload is two complete states with every attribute and that is what
// fills a listen result.
func stateDescribeEvent(ev homeassistant.Event) string {
	if ev.EventType == "state_changed" {
		var d struct {
			EntityID string               `json:"entity_id"`
			Old      *homeassistant.State `json:"old_state"`
			New      *homeassistant.State `json:"new_state"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			old, nw := "(none)", "(removed)"
			if d.Old != nil {
				old = truncate(d.Old.State, 80)
			}
			if d.New != nil {
				nw = truncate(d.New.State, 80)
			}
			line := fmt.Sprintf("%s: %s → %s", d.EntityID, old, nw)
			if d.Old != nil && d.New != nil && d.Old.State == d.New.State {
				line += " (attributes changed: " + strings.Join(stateChangedAttrs(d.Old.Attributes, d.New.Attributes), ", ") + ")"
			}
			return line
		}
	}
	return truncate(stateSanitizeJSON(ev.Data), 1500)
}

func stateChangedAttrs(a, b map[string]any) []string {
	var out []string
	for k, v := range b {
		if k == "access_token" || strings.HasPrefix(k, "entity_picture") {
			continue
		}
		if stateValue(a[k]) != stateValue(v) {
			out = append(out, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// --- ha_system_log ---

type stateSystemLogInput struct {
	Level       string `json:"level,omitempty" jsonschema:"'error' for errors and critical only, 'warning' for everything (the default)"`
	Search      string `json:"search,omitempty" jsonschema:"only entries whose logger name, source or message contains this text"`
	RawLog      bool   `json:"raw_log,omitempty" jsonschema:"also return the tail of home-assistant.log"`
	RawLogLines int    `json:"raw_log_lines,omitempty" jsonschema:"lines of home-assistant.log to return with raw_log, default 100, at most 2000"`
}

func (s *Server) stateSystemLog(ctx context.Context, _ *mcp.CallToolRequest, in stateSystemLogInput) (*mcp.CallToolResult, any, error) {
	entries, err := s.ha.ListSystemLog(ctx)
	if err != nil {
		return fail(err)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp.After(entries[j].Timestamp.Time) })
	q := strings.ToLower(in.Search)
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if strings.EqualFold(in.Level, "error") && e.Level != "ERROR" && e.Level != "CRITICAL" {
			continue
		}
		src := stateLogSource(e.Source)
		msg := strings.Join(e.Message, " / ")
		if q != "" && !strings.Contains(strings.ToLower(e.Name+" "+src+" "+msg+" "+e.Exception), q) {
			continue
		}
		n++
		if n > 100 {
			continue
		}
		count := ""
		if e.Count > 1 {
			count = fmt.Sprintf(" ×%d", e.Count)
		}
		fmt.Fprintf(&b, "%s%s | %s | %s | last %s", e.Level, count, e.Name, src, s.stateTime(e.Timestamp.Time))
		if e.Count > 1 && !e.FirstOccurred.IsZero() {
			fmt.Fprintf(&b, ", first %s", s.stateTime(e.FirstOccurred.Time))
		}
		fmt.Fprintf(&b, "\n  %s\n", truncate(msg, 500))
		if e.Exception != "" {
			lines := strings.Split(strings.TrimSpace(e.Exception), "\n")
			for _, l := range lines[max(0, len(lines)-3):] {
				fmt.Fprintf(&b, "  | %s\n", truncate(l, 300))
			}
		}
	}
	var out strings.Builder
	switch {
	case n == 0:
		out.WriteString("No warnings or errors in the system log")
		if q != "" || in.Level != "" {
			out.WriteString(" match")
		}
		out.WriteString(".\n")
	case n > 100:
		fmt.Fprintf(&out, "%d entries, newest first; showing 100:\n", n)
	default:
		fmt.Fprintf(&out, "%d entries, newest first (times %s):\n", n, s.stateZone())
	}
	out.WriteString(b.String())

	if in.RawLog {
		lines := in.RawLogLines
		if lines <= 0 {
			lines = 100
		}
		lines = min(lines, 2000)
		log, err := s.ha.GetErrorLog(ctx, min(lines*512, 4<<20))
		if err != nil {
			fmt.Fprintf(&out, "\nCould not read home-assistant.log: %v\n", err)
		} else {
			all := strings.Split(strings.TrimRight(log.Text, "\n"), "\n")
			if len(all) > lines {
				all = all[len(all)-lines:]
			}
			fmt.Fprintf(&out, "\nLast %d lines of home-assistant.log:\n", len(all))
			for _, l := range all {
				out.WriteString(stateTokenParam.ReplaceAllString(truncate(l, 1000), "${1}REDACTED"))
				out.WriteString("\n")
			}
		}
	}
	return text(out.String()), nil, nil
}

func stateLogSource(src []any) string {
	if len(src) == 2 {
		return fmt.Sprintf("%v:%v", src[0], stateValue(src[1]))
	}
	if len(src) == 0 {
		return "-"
	}
	return stateValue(src)
}

// --- ha_check_config ---

type stateCheckConfigInput struct{}

func (s *Server) stateCheckConfig(ctx context.Context, _ *mcp.CallToolRequest, _ stateCheckConfigInput) (*mcp.CallToolResult, any, error) {
	res, err := s.ha.CheckConfig(ctx)
	if err != nil {
		return fail(err)
	}
	return text(stateConfigReport(res)), nil, nil
}

func stateConfigReport(res *homeassistant.ConfigCheckResult) string {
	var b strings.Builder
	if res.Result == "valid" {
		b.WriteString("Configuration is valid.\n")
	} else {
		fmt.Fprintf(&b, "Configuration is %s.\n", res.Result)
	}
	if res.Errors != nil && *res.Errors != "" {
		fmt.Fprintf(&b, "Errors:\n%s\n", strings.TrimSpace(*res.Errors))
	}
	if res.Warnings != nil && *res.Warnings != "" {
		fmt.Fprintf(&b, "Warnings:\n%s\n", strings.TrimSpace(*res.Warnings))
	}
	return b.String()
}

// --- ha_calendar_events ---

type stateCalendarEventsInput struct {
	EntityID string `json:"entity_id,omitempty" jsonschema:"calendar entity id; omit to list the calendars"`
	Start    string `json:"start,omitempty" jsonschema:"start of the range: 'YYYY-MM-DD', 'today', 'tomorrow', ISO timestamp, or an offset like '-7d'; default today"`
	End      string `json:"end,omitempty" jsonschema:"end of the range, same forms; default 7 days after start"`
}

func (s *Server) stateCalendarEvents(ctx context.Context, _ *mcp.CallToolRequest, in stateCalendarEventsInput) (*mcp.CallToolResult, any, error) {
	if in.EntityID == "" {
		cals, err := s.ha.ListCalendars(ctx)
		if err != nil {
			return fail(err)
		}
		if len(cals) == 0 {
			return text("There are no calendars."), nil, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d calendars:\n", len(cals))
		for _, c := range cals {
			fmt.Fprintf(&b, "%s | %s\n", c.EntityID, c.Name)
		}
		return text(b.String()), nil, nil
	}

	now := s.now()
	start, err := stateParseTime(in.Start, now)
	if err != nil {
		return fail(err)
	}
	end := start.AddDate(0, 0, 7)
	if in.End != "" {
		if end, err = stateParseTime(in.End, now); err != nil {
			return fail(err)
		}
		// A bare date as the end means through that day, as a person means it.
		if e := strings.ToLower(strings.TrimSpace(in.End)); stateDateOnly.MatchString(e) || e == "today" || e == "tomorrow" || e == "yesterday" {
			end = end.AddDate(0, 0, 1)
		}
	}
	events, err := s.ha.CalendarEvents(ctx, in.EntityID, start, end)
	if err != nil {
		return fail(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d events %s → %s", in.EntityID, len(events), start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"))
	if len(events) > 300 {
		b.WriteString(", showing the first 300")
		events = events[:300]
	}
	b.WriteString("\n")
	loc := now.Location()
	for _, e := range events {
		fmt.Fprintf(&b, "%s | %s", stateCalRange(e.Start, e.End, loc), truncate(e.Summary, 150))
		if e.Location != nil && *e.Location != "" {
			fmt.Fprintf(&b, " | at %s", truncate(*e.Location, 100))
		}
		if e.Description != nil && *e.Description != "" {
			fmt.Fprintf(&b, " | %s", truncate(*e.Description, 300))
		}
		if e.RRule != nil && *e.RRule != "" {
			fmt.Fprintf(&b, " | repeats %s", *e.RRule)
		}
		if e.UID != nil && *e.UID != "" {
			fmt.Fprintf(&b, " | uid %s", *e.UID)
			if e.RecurrenceID != nil && *e.RecurrenceID != "" {
				fmt.Fprintf(&b, " recurrence_id %s", *e.RecurrenceID)
			}
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil, nil
}

func stateCalRange(start, end homeassistant.CalendarTime, loc *time.Location) string {
	if start.Date != "" {
		last := end.Date
		if t, err := time.Parse("2006-01-02", end.Date); err == nil {
			// All-day ends are exclusive in iCalendar.
			last = t.AddDate(0, 0, -1).Format("2006-01-02")
		}
		if last == "" || last == start.Date {
			return start.Date + " all day"
		}
		return start.Date + " → " + last + " all day"
	}
	st, err1 := time.Parse(time.RFC3339, start.DateTime)
	en, err2 := time.Parse(time.RFC3339, end.DateTime)
	if err1 != nil || err2 != nil {
		return start.DateTime + " → " + end.DateTime
	}
	st, en = st.In(loc), en.In(loc)
	if st.Format("2006-01-02") == en.Format("2006-01-02") {
		return st.Format("2006-01-02 15:04") + "–" + en.Format("15:04")
	}
	return st.Format("2006-01-02 15:04") + " → " + en.Format("2006-01-02 15:04")
}

// --- ha_list_todo_items ---

type stateListTodoInput struct {
	EntityID string `json:"entity_id,omitempty" jsonschema:"todo entity id like todo.shopping_list; omit to list the to-do lists"`
	Status   string `json:"status,omitempty" jsonschema:"'needs_action' or 'completed'; default both"`
}

func (s *Server) stateListTodoItems(ctx context.Context, _ *mcp.CallToolRequest, in stateListTodoInput) (*mcp.CallToolResult, any, error) {
	if in.EntityID == "" {
		states, err := s.ha.ListStates(ctx)
		if err != nil {
			return fail(err)
		}
		var b strings.Builder
		n := 0
		for _, st := range states {
			if st.Domain() != "todo" {
				continue
			}
			n++
			fmt.Fprintf(&b, "%s | %s | %s open\n", st.EntityID, stateName(st), st.State)
		}
		if n == 0 {
			return text("There are no to-do lists."), nil, nil
		}
		return text(fmt.Sprintf("%d to-do lists:\n%s", n, b.String())), nil, nil
	}
	if !strings.HasPrefix(in.EntityID, "todo.") {
		return fail(fmt.Errorf("%q is not a todo entity; call without entity_id to list them", in.EntityID))
	}
	items, err := s.ha.ListTodoItems(ctx, in.EntityID)
	if err != nil {
		return fail(err)
	}
	var open, done []homeassistant.TodoItem
	for _, it := range items {
		if it.Status == "completed" {
			done = append(done, it)
		} else {
			open = append(open, it)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d open, %d completed\n", in.EntityID, len(open), len(done))
	write := func(list []homeassistant.TodoItem, mark string) {
		if len(list) > 300 {
			fmt.Fprintf(&b, "(showing 300 of %d)\n", len(list))
			list = list[:300]
		}
		for _, it := range list {
			fmt.Fprintf(&b, "%s %s", mark, truncate(it.Summary, 200))
			if it.Due != nil && *it.Due != "" {
				fmt.Fprintf(&b, " | due %s", *it.Due)
			}
			if it.Description != nil && *it.Description != "" {
				fmt.Fprintf(&b, " | %s", truncate(*it.Description, 300))
			}
			fmt.Fprintf(&b, " | uid %s\n", it.UID)
		}
	}
	if in.Status != "completed" {
		write(open, "[ ]")
	}
	if in.Status != "needs_action" {
		write(done, "[x]")
	}
	return text(b.String()), nil, nil
}

// --- ha_camera_snapshot ---

type stateCameraInput struct {
	EntityID string `json:"entity_id" jsonschema:"camera entity id"`
	Width    int    `json:"width,omitempty" jsonschema:"scale the image to this width in pixels where the camera supports it, e.g. 640"`
}

func (s *Server) stateCameraSnapshot(ctx context.Context, _ *mcp.CallToolRequest, in stateCameraInput) (*mcp.CallToolResult, any, error) {
	img, err := s.ha.CameraSnapshot(ctx, in.EntityID, in.Width, 0)
	if err != nil {
		return fail(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf("%s at %s (%s, %d KB)", in.EntityID, s.stateTime(s.now()), img.ContentType, (len(img.Data)+1023)/1024)},
		&mcp.ImageContent{Data: img.Data, MIMEType: img.ContentType},
	}}, nil, nil
}

// --- ha_list_notifications ---

type stateListNotificationsInput struct{}

func (s *Server) stateListNotifications(ctx context.Context, _ *mcp.CallToolRequest, _ stateListNotificationsInput) (*mcp.CallToolResult, any, error) {
	ns, err := s.ha.ListNotifications(ctx)
	if err != nil {
		return fail(err)
	}
	if len(ns) == 0 {
		return text("There are no notifications."), nil, nil
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].CreatedAt.After(ns[j].CreatedAt) })
	var b strings.Builder
	fmt.Fprintf(&b, "%d notifications, newest first:\n", len(ns))
	for _, n := range ns {
		title := ""
		if n.Title != nil && *n.Title != "" {
			title = *n.Title + ": "
		}
		fmt.Fprintf(&b, "%s | %s | %s%s\n", n.NotificationID, s.stateTime(n.CreatedAt), title, truncate(n.Message, 800))
	}
	return text(b.String()), nil, nil
}

// --- ha_call_service ---

type stateTarget struct {
	EntityID []string `json:"entity_id,omitempty" jsonschema:"entity ids"`
	DeviceID []string `json:"device_id,omitempty" jsonschema:"device ids"`
	AreaID   []string `json:"area_id,omitempty" jsonschema:"area ids"`
	FloorID  []string `json:"floor_id,omitempty" jsonschema:"floor ids"`
	LabelID  []string `json:"label_id,omitempty" jsonschema:"label ids"`
}

type stateCallServiceInput struct {
	Domain         string         `json:"domain,omitempty" jsonschema:"service domain, e.g. light; may be left out when service is given as domain.service"`
	Service        string         `json:"service" jsonschema:"service name, e.g. turn_on, or domain.service like light.turn_on"`
	Target         *stateTarget   `json:"target,omitempty" jsonschema:"what the service acts on"`
	Data           map[string]any `json:"data,omitempty" jsonschema:"service fields, e.g. {\"brightness_pct\": 50} or {\"item\": \"Milk\"}"`
	ReturnResponse bool           `json:"return_response,omitempty" jsonschema:"return the service's response; required for services that only return data"`
}

func (s *Server) stateCallService(ctx context.Context, _ *mcp.CallToolRequest, in stateCallServiceInput) (*mcp.CallToolResult, any, error) {
	if d, svc, ok := strings.Cut(in.Service, "."); ok && (in.Domain == "" || in.Domain == d) {
		in.Domain, in.Service = d, svc
	}
	switch in.Domain + "." + in.Service {
	case "homeassistant.restart":
		return fail(errors.New("use ha_restart, which checks the configuration first"))
	case "homeassistant.stop", "hassio.host_shutdown", "hassio.host_reboot":
		return fail(fmt.Errorf("%s.%s would take Home Assistant down with no way back through this server; the person has to do it in the UI", in.Domain, in.Service))
	}
	call := homeassistant.ServiceCall{Domain: in.Domain, Service: in.Service, Data: in.Data, ReturnResponse: in.ReturnResponse}
	if t := in.Target; t != nil {
		call.Target = &homeassistant.Target{
			EntityID: stateStrings(t.EntityID), DeviceID: stateStrings(t.DeviceID), AreaID: stateStrings(t.AreaID),
			FloorID: stateStrings(t.FloorID), LabelID: stateStrings(t.LabelID),
		}
	}
	res, err := s.ha.CallService(ctx, call)
	if err != nil {
		var haErr *homeassistant.Error
		if errors.As(err, &haErr) && haErr.StatusCode == 400 && !in.ReturnResponse && strings.Contains(strings.ToLower(haErr.Message), "response") {
			return fail(fmt.Errorf("%w (this service returns data: call again with return_response:true)", err))
		}
		return fail(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Called %s.%s.\n", in.Domain, in.Service)
	hasResponse := len(res.Response) > 0 && string(res.Response) != "null"
	switch {
	case len(res.ChangedStates) == 0 && !hasResponse:
		b.WriteString("No entity changed state while the call ran. That is normal for services that act later or change nothing visible; check with ha_get_state if it matters.\n")
	case len(res.ChangedStates) > 0:
		fmt.Fprintf(&b, "%d entities changed:\n", len(res.ChangedStates))
		for i, st := range res.ChangedStates {
			if i == 50 {
				fmt.Fprintf(&b, "… %d more\n", len(res.ChangedStates)-50)
				break
			}
			fmt.Fprintf(&b, "%s | %s | %s\n", st.EntityID, stateName(st), stateWithUnit(st))
		}
	}
	if hasResponse {
		resp := stateSanitizeJSON(res.Response)
		if len(resp) > 50000 {
			resp = resp[:50000] + " … response cut at 50000 characters"
		}
		fmt.Fprintf(&b, "Response:\n%s\n", resp)
	}
	return text(b.String()), nil, nil
}

// --- ha_fire_event ---

type stateFireEventInput struct {
	EventType string         `json:"event_type" jsonschema:"event type, e.g. my_custom_event"`
	Data      map[string]any `json:"data,omitempty" jsonschema:"event data"`
}

func (s *Server) stateFireEvent(ctx context.Context, _ *mcp.CallToolRequest, in stateFireEventInput) (*mcp.CallToolResult, any, error) {
	if in.EventType == "" {
		return fail(errors.New("event_type is required"))
	}
	c, err := s.ha.FireEvent(ctx, in.EventType, in.Data)
	if err != nil {
		return fail(err)
	}
	return text(fmt.Sprintf("Fired %s (context id %s). Automations it triggered show up in ha_logbook.", in.EventType, c.ID)), nil, nil
}

// --- ha_restart ---

type stateRestartInput struct {
	Confirm bool `json:"confirm" jsonschema:"must be true; Home Assistant goes down for a minute or more"`
}

func (s *Server) stateRestart(ctx context.Context, _ *mcp.CallToolRequest, in stateRestartInput) (*mcp.CallToolResult, any, error) {
	if !in.Confirm {
		return fail(errors.New("a restart makes Home Assistant unavailable and stops running automations. Check with the person, then call again with confirm:true"))
	}
	check, err := s.ha.CheckConfig(ctx)
	if err != nil {
		return fail(fmt.Errorf("the configuration check failed, so the restart was not attempted: %w", err))
	}
	if check.Result != "valid" {
		return fail(fmt.Errorf("the restart was refused because the configuration is invalid. Fix it first.\n%s", stateConfigReport(check)))
	}
	err = s.ha.Restart(ctx)
	var haErr *homeassistant.Error
	var netErr net.Error
	switch {
	case err == nil:
	case errors.As(err, &haErr):
		return fail(err)
	case errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset"):
		// HA often shuts its HTTP server down before answering the call that
		// asked it to.
	default:
		return fail(err)
	}
	msg := "Restart started. Home Assistant will be unavailable for a minute or more; tools will fail until it is back."
	if check.Warnings != nil && *check.Warnings != "" {
		msg += "\nThe configuration check had warnings:\n" + strings.TrimSpace(*check.Warnings)
	}
	return text(msg), nil, nil
}

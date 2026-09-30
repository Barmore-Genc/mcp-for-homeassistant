package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// addOrganizeTools registers the tools for registries, helpers, integrations, dashboards and backups. Write tools are skipped when s.readOnly is set.
func (s *Server) addOrganizeTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_registry",
		Annotations: readOnlyTool(),
		Description: "List how the home is organized: type='areas', 'floors', 'labels', 'categories', 'devices', " +
			"'entities', 'persons' or 'zones'. Use it to find the ids that other tools and automations need " +
			"(area_id, floor_id, label_id, device_id, entity_id) and to answer 'what is in the kitchen', 'which " +
			"devices does the hue integration have' or 'what is labelled outdoor'. Devices and entities can be " +
			"filtered by area, label, integration and search text (entities also by domain); disabled ones are left " +
			"out unless include_disabled is set. Entities here are registry entries (names, areas, labels, " +
			"disabled/hidden), not live states. Change any of this with ha_manage_registry.",
	}, s.organizeListRegistry)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_helpers",
		Annotations: readOnlyTool(),
		Description: "List the helpers created in the UI with each one's settings: input_boolean, input_number, " +
			"input_select, input_text, input_datetime, input_button, counter, timer and schedule. Pass domain " +
			"for one type or leave it out for all. Each line shows the helper's id (what ha_manage_helper takes) " +
			"and its entity_id (what automations and services use). Helpers defined in YAML are not listed and " +
			"cannot be changed here. Current values are states; read those with the state tools.",
	}, s.organizeListHelpers)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_integrations",
		Annotations: readOnlyTool(),
		Description: "List the configured integrations (config entries): title, domain, state, whether it is " +
			"disabled and its entry_id. Use it to find out why an integration's devices are unavailable (state " +
			"setup_error, setup_retry, not_loaded) and to get the entry_id that ha_manage_integration needs to " +
			"reload, enable or disable one. Filter by domain, for example domain='hue'.",
	}, s.organizeListIntegrations)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_get_dashboard",
		Annotations: readOnlyTool(),
		Description: "Without url_path: list the dashboards with their url_path, title and mode. With url_path: " +
			"return that dashboard's full config as YAML, ready to edit and pass to ha_save_dashboard. The default " +
			"dashboard (Overview) has url_path 'lovelace'. A dashboard that was never edited is auto-generated and " +
			"has no stored config; the answer says so.",
	}, s.organizeGetDashboard)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_backup_info",
		Annotations: readOnlyTool(),
		Description: "Show the backups Home Assistant knows about (newest first, with date, size, storage " +
			"locations and whether each is protected), when an automatic backup last ran and succeeded, when the " +
			"next is due, and whether a backup is running now. It also shows the automatic backup settings: whether " +
			"they are set up, the schedule, how many backups are kept, what they contain and where they are stored. Use it before risky changes to check there is a " +
			"recent backup, and after ha_create_backup to see the result.",
	}, s.organizeBackupInfo)

	if s.readOnly {
		return
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_manage_registry",
		Annotations: writeTool(true),
		Description: "Create, update or delete areas, floors, labels, categories and zones, and update devices " +
			"and entities. resource is one of area, floor, label, category, device, entity, zone; action is create, " +
			"update or delete. Devices can only be updated (name, area, labels, disabled). Entities can be updated " +
			"(name, icon, area, labels, disabled, hidden, new_entity_id) or deleted, which removes a stale registry " +
			"entry; an integration that still provides the entity adds it back. Only the fields you pass change; " +
			"the answer shows the item before and after. Areas, floors and labels can be referenced by name or id. " +
			"Deleting and renaming an entity_id need confirm:true: a renamed entity_id breaks every automation, " +
			"script, dashboard and template that uses the old one, so the first call without confirm lists what " +
			"references it. Persons are not managed here.",
	}, s.organizeManageRegistry)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_manage_helper",
		Annotations: writeTool(true),
		Description: "Create, update or delete a UI helper. domain is the helper type, action is create, update " +
			"or delete, id is the helper's id or entity_id (from ha_list_helpers). fields holds the settings; on " +
			"update only the fields you pass change and null removes one. Fields by domain (name is required on " +
			"create, icon like mdi:bell is optional everywhere):\n" +
			"- input_boolean: initial (bool)\n" +
			"- input_number: min, max (required), step, initial, mode ('slider' or 'box'), unit_of_measurement\n" +
			"- input_select: options (list of strings, required), initial\n" +
			"- input_text: min, max (length, max up to 255), pattern (regex), mode ('text' or 'password'), initial\n" +
			"- input_datetime: has_date, has_time (at least one true), initial\n" +
			"- input_button: no extra fields\n" +
			"- counter: initial, step, minimum, maximum, restore (bool)\n" +
			"- timer: duration ('HH:MM:SS'), restore (bool)\n" +
			"- schedule: monday … sunday, each a list of {from: 'HH:MM:SS', to: 'HH:MM:SS'}\n" +
			"Deleting needs confirm:true and breaks automations that use the helper. Zones are managed with " +
			"ha_manage_registry.",
	}, s.organizeManageHelper)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_manage_integration",
		Annotations: writeTool(true),
		Description: "Reload, enable or disable a configured integration by entry_id (from ha_list_integrations). " +
			"Reload is the first thing to try when an integration's devices went unavailable or it is in " +
			"setup_retry. Disabling unloads it and makes all its devices and entities unavailable until it is " +
			"enabled again, so it needs confirm:true. The answer says when Home Assistant needs a restart to " +
			"finish the change.",
	}, s.organizeManageIntegration)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_save_dashboard",
		Annotations: writeTool(true),
		Description: "Replace a storage-mode dashboard's whole config. Get the current one with ha_get_dashboard, " +
			"edit it, and pass it back as config (YAML text, JSON text or an object) with a top-level views list " +
			"(or strategy). Replacing a config that exists needs confirm:true. The answer contains the previous " +
			"config so the change can be reverted by saving that back. Dashboards in YAML mode are edited in their " +
			"files and cannot be saved here.",
	}, s.organizeSaveDashboard)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_create_backup",
		Annotations: writeTool(false),
		Description: "Start a backup now with the automatic backup settings (the storage locations, encryption and " +
			"contents chosen in Settings > System > Backups). Use it before a risky change. It waits up to 30 " +
			"seconds for the backup to finish and reports it; a longer one keeps running and shows up in " +
			"ha_backup_info. It fails when automatic backups have not been set up, which only the person can do " +
			"in the Home Assistant UI.",
	}, s.organizeCreateBackup)
}

// --- shared rendering and lookups ---

// organizeLine joins the non-empty parts of a list line, so an absent field
// costs nothing rather than a dangling separator. Every list line goes through
// here, which makes it the place that keeps HA-supplied names to one line.
func organizeLine(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, oneLine(p))
		}
	}
	return strings.Join(kept, " | ")
}

func organizeCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func organizeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func organizePrefixed(prefix string, p *string) string {
	if p == nil || *p == "" {
		return ""
	}
	return prefix + *p
}

// organizeValue renders a helper setting. Strings go out bare because quoting
// every one doubles the punctuation on a line for no gain in clarity.
func organizeValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

type organizeRef struct{ id, name string }

// organizeResolve accepts an id or a name, because a model holding "Kitchen"
// should not need a round trip to learn that its id is "kitchen".
func organizeResolve(kind, in string, refs []organizeRef) (organizeRef, error) {
	for _, r := range refs {
		if r.id == in {
			return r, nil
		}
	}
	var matches []organizeRef
	for _, r := range refs {
		if strings.EqualFold(r.name, in) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		names := make([]string, 0, len(refs))
		for _, r := range refs {
			names = append(names, oneLine(r.name))
		}
		if len(names) > 30 {
			names = append(names[:30], "…")
		}
		if len(names) == 0 {
			return organizeRef{}, fmt.Errorf("no %s %q: there are none", kind, in)
		}
		return organizeRef{}, fmt.Errorf("no %s with id or name %q; known: %s", kind, in, strings.Join(names, ", "))
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.id
		}
		return organizeRef{}, fmt.Errorf("%d %ss are named %q; pass one of these ids: %s", len(matches), kind, in, strings.Join(ids, ", "))
	}
}

// organizeIndex holds the registries that turn ids on a device or entity into
// names a person recognizes.
type organizeIndex struct {
	areas   map[string]homeassistant.Area
	floors  map[string]homeassistant.Floor
	labels  map[string]homeassistant.Label
	devices map[string]homeassistant.Device
	entries map[string]homeassistant.ConfigEntry
}

type organizeNeed struct{ floors, devices, entries bool }

func (s *Server) organizeLoadIndex(ctx context.Context, need organizeNeed) (*organizeIndex, error) {
	ix := &organizeIndex{
		areas: map[string]homeassistant.Area{}, floors: map[string]homeassistant.Floor{},
		labels: map[string]homeassistant.Label{}, devices: map[string]homeassistant.Device{},
		entries: map[string]homeassistant.ConfigEntry{},
	}
	areas, err := s.ha.ListAreas(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range areas {
		ix.areas[a.AreaID] = a
	}
	labels, err := s.ha.ListLabels(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range labels {
		ix.labels[l.LabelID] = l
	}
	if need.floors {
		floors, err := s.ha.ListFloors(ctx)
		if err != nil {
			return nil, err
		}
		for _, f := range floors {
			ix.floors[f.FloorID] = f
		}
	}
	if need.devices {
		devices, err := s.ha.ListDevices(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range devices {
			ix.devices[d.ID] = d
		}
	}
	if need.entries {
		entries, err := s.ha.ListConfigEntries(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ix.entries[e.EntryID] = e
		}
	}
	return ix, nil
}

func (ix *organizeIndex) areaRefs() []organizeRef {
	refs := make([]organizeRef, 0, len(ix.areas))
	for _, a := range ix.areas {
		refs = append(refs, organizeRef{a.AreaID, a.Name})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	return refs
}

func (ix *organizeIndex) labelRefs() []organizeRef {
	refs := make([]organizeRef, 0, len(ix.labels))
	for _, l := range ix.labels {
		refs = append(refs, organizeRef{l.LabelID, l.Name})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	return refs
}

func (ix *organizeIndex) floorRefs() []organizeRef {
	refs := make([]organizeRef, 0, len(ix.floors))
	for _, f := range ix.floors {
		refs = append(refs, organizeRef{f.FloorID, f.Name})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	return refs
}

func (ix *organizeIndex) resolveLabels(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, l := range in {
		r, err := organizeResolve("label", l, ix.labelRefs())
		if err != nil {
			return nil, err
		}
		out = append(out, r.id)
	}
	return out, nil
}

func (ix *organizeIndex) areaName(id *string) string {
	if id == nil || *id == "" {
		return ""
	}
	if a, ok := ix.areas[*id]; ok {
		return a.Name
	}
	return *id
}

func (ix *organizeIndex) labelNames(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id
		if l, ok := ix.labels[id]; ok {
			names[i] = l.Name
		}
	}
	return "labels " + strings.Join(names, ", ")
}

func (ix *organizeIndex) deviceDomains(d homeassistant.Device) []string {
	var out []string
	for _, id := range d.ConfigEntries {
		if e, ok := ix.entries[id]; ok && !slices.Contains(out, e.Domain) {
			out = append(out, e.Domain)
		}
	}
	return out
}

// entityArea is the area an entity is in: its own, or its device's when it
// has none, which is how HA itself decides and how area targets resolve.
func (ix *organizeIndex) entityArea(e homeassistant.EntityRegistryEntry) (id string, inherited bool) {
	if e.AreaID != nil && *e.AreaID != "" {
		return *e.AreaID, false
	}
	if e.DeviceID != nil {
		if d, ok := ix.devices[*e.DeviceID]; ok && d.AreaID != nil {
			return *d.AreaID, true
		}
	}
	return "", false
}

// entityName is the name HA shows: an entity with has_entity_name is named
// relative to its device, so "Battery" on "Front Door" reads "Front Door Battery".
func (ix *organizeIndex) entityName(e homeassistant.EntityRegistryEntry) string {
	if e.Name != nil && *e.Name != "" {
		return *e.Name
	}
	orig := organizeStr(e.OriginalName)
	if e.HasEntityName && e.DeviceID != nil {
		if d, ok := ix.devices[*e.DeviceID]; ok {
			if orig == "" {
				return d.DisplayName()
			}
			return d.DisplayName() + " " + orig
		}
	}
	return orig
}

func (ix *organizeIndex) entityLine(e homeassistant.EntityRegistryEntry) string {
	area := ""
	if id, inherited := ix.entityArea(e); id != "" {
		area = "area " + ix.areaName(&id)
		if inherited {
			area += " (from device)"
		}
	}
	name := ix.entityName(e)
	device := ""
	if e.DeviceID != nil {
		if d, ok := ix.devices[*e.DeviceID]; ok && d.DisplayName() != name {
			device = "device " + d.DisplayName()
		}
	}
	if e.Name != nil && *e.Name != "" {
		name += " (renamed)"
	}
	return organizeLine(e.EntityID, name, e.Platform, device, area, ix.labelNames(e.Labels),
		organizePrefixed("icon ", e.Icon), organizePrefixed("disabled by ", e.DisabledBy),
		organizePrefixed("hidden by ", e.HiddenBy), organizeStr(e.EntityCategory))
}

func (ix *organizeIndex) deviceLine(d homeassistant.Device, entityCount int) string {
	model := strings.TrimSpace(organizeStr(d.Manufacturer) + " " + organizeStr(d.Model))
	via := ""
	if d.ViaDeviceID != nil {
		if v, ok := ix.devices[*d.ViaDeviceID]; ok {
			via = "via " + v.DisplayName()
		}
	}
	name := d.DisplayName()
	if d.NameByUser != nil && *d.NameByUser != "" && d.Name != nil && *d.Name != *d.NameByUser {
		name += " (was " + *d.Name + ")"
	}
	area := ""
	if d.AreaID != nil {
		area = "area " + ix.areaName(d.AreaID)
	}
	integ := ""
	if domains := ix.deviceDomains(d); len(domains) > 0 {
		integ = "integration " + strings.Join(domains, ", ")
	}
	entities := ""
	if entityCount >= 0 {
		entities = organizeCount(entityCount, "entity", "entities")
	}
	return organizeLine(name, "device_id "+d.ID, area, model, integ, via, organizeStr(d.EntryType),
		ix.labelNames(d.Labels), organizePrefixed("disabled by ", d.DisabledBy), entities)
}

func organizeAreaLine(a homeassistant.Area, ix *organizeIndex) string {
	floor := ""
	if a.FloorID != nil {
		floor = "floor " + *a.FloorID
		if f, ok := ix.floors[*a.FloorID]; ok {
			floor = "floor " + f.Name
		}
	}
	aliases := ""
	if len(a.Aliases) > 0 {
		aliases = "aliases " + strings.Join(a.Aliases, ", ")
	}
	return organizeLine(a.Name, "area_id "+a.AreaID, floor, organizePrefixed("icon ", a.Icon), ix.labelNames(a.Labels),
		aliases, organizePrefixed("temperature ", a.TemperatureEntityID), organizePrefixed("humidity ", a.HumidityEntityID))
}

func organizeFloorLine(f homeassistant.Floor, areas []string) string {
	level := ""
	if f.Level != nil {
		level = fmt.Sprintf("level %d", *f.Level)
	}
	aliases := ""
	if len(f.Aliases) > 0 {
		aliases = "aliases " + strings.Join(f.Aliases, ", ")
	}
	areaList := "no areas"
	if len(areas) > 0 {
		areaList = "areas " + strings.Join(areas, ", ")
	}
	return organizeLine(f.Name, "floor_id "+f.FloorID, level, organizePrefixed("icon ", f.Icon), aliases, areaList)
}

func organizeLabelLine(l homeassistant.Label) string {
	return organizeLine(l.Name, "label_id "+l.LabelID, organizePrefixed("color ", l.Color),
		organizePrefixed("icon ", l.Icon), truncate(organizeStr(l.Description), 120))
}

func organizeCategoryLine(scope string, c homeassistant.Category) string {
	return organizeLine(c.Name, "category_id "+c.CategoryID, "scope "+scope, organizePrefixed("icon ", c.Icon))
}

func organizeZoneLine(z map[string]any, entityID string) string {
	name, _ := z["name"].(string)
	id, _ := z["id"].(string)
	parts := []string{name, "id " + id}
	if entityID != "" {
		parts = append(parts, entityID)
	}
	parts = append(parts, fmt.Sprintf("lat %v, lon %v", z["latitude"], z["longitude"]))
	if r, ok := z["radius"]; ok {
		parts = append(parts, fmt.Sprintf("radius %vm", r))
	}
	if p, _ := z["passive"].(bool); p {
		parts = append(parts, "passive")
	}
	if icon, _ := z["icon"].(string); icon != "" {
		parts = append(parts, "icon "+icon)
	}
	return organizeLine(parts...)
}

// organizeCategoryScopes are the scopes the HA UI offers categories in; HA
// accepts any slug, but nothing else shows them.
var organizeCategoryScopes = []string{"automation", "script", "scene", "helpers"}

// --- ha_list_registry ---

type organizeListRegistryInput struct {
	Type            string `json:"type" jsonschema:"areas, floors, labels, categories, devices, entities, persons or zones"`
	Scope           string `json:"scope,omitempty" jsonschema:"categories only: automation, script, scene or helpers; all four when left out"`
	Area            string `json:"area,omitempty" jsonschema:"devices and entities: only those in this area (id or name); an entity counts as in its device's area unless it has its own"`
	Label           string `json:"label,omitempty" jsonschema:"devices and entities: only those with this label (id or name)"`
	Integration     string `json:"integration,omitempty" jsonschema:"devices and entities: only those from this integration domain, for example hue or zwave_js"`
	Domain          string `json:"domain,omitempty" jsonschema:"entities only: only this entity domain, for example light or sensor"`
	Search          string `json:"search,omitempty" jsonschema:"devices and entities: case-insensitive text to find in the name, entity_id, manufacturer or model"`
	IncludeDisabled bool   `json:"include_disabled,omitempty" jsonschema:"devices and entities: also list disabled ones"`
	Limit           int    `json:"limit,omitempty" jsonschema:"devices and entities: most lines to return, default 100, at most 1000"`
}

func (s *Server) organizeListRegistry(ctx context.Context, _ *mcp.CallToolRequest, in organizeListRegistryInput) (*mcp.CallToolResult, any, error) {
	var (
		out string
		err error
	)
	switch in.Type {
	case "areas", "area":
		out, err = s.organizeListAreas(ctx)
	case "floors", "floor":
		out, err = s.organizeListFloors(ctx)
	case "labels", "label":
		out, err = s.organizeListLabels(ctx)
	case "categories", "category":
		out, err = s.organizeListCategories(ctx, in.Scope)
	case "devices", "device":
		out, err = s.organizeListDevices(ctx, in)
	case "entities", "entity":
		out, err = s.organizeListEntities(ctx, in)
	case "persons", "person", "people":
		out, err = s.organizeListPersons(ctx)
	case "zones", "zone":
		out, err = s.organizeListZones(ctx)
	default:
		err = fmt.Errorf("type must be areas, floors, labels, categories, devices, entities, persons or zones; got %q", in.Type)
	}
	if err != nil {
		return fail(err)
	}
	return text(out), nil, nil
}

func (s *Server) organizeListAreas(ctx context.Context) (string, error) {
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{floors: true})
	if err != nil {
		return "", err
	}
	if len(ix.areas) == 0 {
		return "There are no areas.", nil
	}
	var b strings.Builder
	b.WriteString(organizeCount(len(ix.areas), "area", "areas") + ":\n")
	for _, r := range ix.areaRefs() {
		b.WriteString(organizeAreaLine(ix.areas[r.id], ix) + "\n")
	}
	return b.String(), nil
}

func (s *Server) organizeListFloors(ctx context.Context) (string, error) {
	floors, err := s.ha.ListFloors(ctx)
	if err != nil {
		return "", err
	}
	if len(floors) == 0 {
		return "There are no floors. Areas can be grouped into floors with ha_manage_registry.", nil
	}
	areas, err := s.ha.ListAreas(ctx)
	if err != nil {
		return "", err
	}
	byFloor := map[string][]string{}
	unassigned := 0
	for _, a := range areas {
		if a.FloorID == nil {
			unassigned++
			continue
		}
		byFloor[*a.FloorID] = append(byFloor[*a.FloorID], a.Name)
	}
	sort.Slice(floors, func(i, j int) bool {
		li, lj := floors[i].Level, floors[j].Level
		if li != nil && lj != nil && *li != *lj {
			return *li < *lj
		}
		return floors[i].Name < floors[j].Name
	})
	var b strings.Builder
	b.WriteString(organizeCount(len(floors), "floor", "floors") + ":\n")
	for _, f := range floors {
		b.WriteString(organizeFloorLine(f, byFloor[f.FloorID]) + "\n")
	}
	if unassigned > 0 {
		fmt.Fprintf(&b, "%s on no floor.\n", organizeCount(unassigned, "area is", "areas are"))
	}
	return b.String(), nil
}

func (s *Server) organizeListLabels(ctx context.Context) (string, error) {
	labels, err := s.ha.ListLabels(ctx)
	if err != nil {
		return "", err
	}
	if len(labels) == 0 {
		return "There are no labels.", nil
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
	var b strings.Builder
	b.WriteString(organizeCount(len(labels), "label", "labels") + ":\n")
	for _, l := range labels {
		b.WriteString(organizeLabelLine(l) + "\n")
	}
	return b.String(), nil
}

func (s *Server) organizeListCategories(ctx context.Context, scope string) (string, error) {
	scopes := organizeCategoryScopes
	if scope != "" {
		scopes = []string{scope}
	}
	var b strings.Builder
	total := 0
	for _, sc := range scopes {
		cats, err := s.ha.ListCategories(ctx, sc)
		if err != nil {
			return "", err
		}
		sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })
		for _, c := range cats {
			b.WriteString(organizeCategoryLine(sc, c) + "\n")
		}
		total += len(cats)
	}
	if total == 0 {
		return fmt.Sprintf("There are no categories in %s.", strings.Join(scopes, ", ")), nil
	}
	return organizeCount(total, "category", "categories") + ":\n" + b.String(), nil
}

func organizeLimit(n int) int {
	switch {
	case n <= 0:
		return 100
	case n > 1000:
		return 1000
	}
	return n
}

// organizeFilters resolves the area and label filters once, so a name typo is
// reported as such instead of as an empty result.
func organizeFilters(ix *organizeIndex, in organizeListRegistryInput) (area, label string, err error) {
	if in.Area != "" {
		r, err := organizeResolve("area", in.Area, ix.areaRefs())
		if err != nil {
			return "", "", err
		}
		area = r.id
	}
	if in.Label != "" {
		r, err := organizeResolve("label", in.Label, ix.labelRefs())
		if err != nil {
			return "", "", err
		}
		label = r.id
	}
	return area, label, nil
}

func (s *Server) organizeListDevices(ctx context.Context, in organizeListRegistryInput) (string, error) {
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{devices: true, entries: true})
	if err != nil {
		return "", err
	}
	area, label, err := organizeFilters(ix, in)
	if err != nil {
		return "", err
	}
	entities, err := s.ha.ListEntityRegistry(ctx)
	if err != nil {
		return "", err
	}
	counts := map[string]int{}
	for _, e := range entities {
		if e.DeviceID != nil {
			counts[*e.DeviceID]++
		}
	}
	search := strings.ToLower(in.Search)
	var matched []homeassistant.Device
	disabled := 0
	for _, d := range ix.devices {
		if area != "" && organizeStr(d.AreaID) != area {
			continue
		}
		if label != "" && !slices.Contains(d.Labels, label) {
			continue
		}
		if in.Integration != "" && !slices.Contains(ix.deviceDomains(d), in.Integration) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(strings.Join([]string{
			d.DisplayName(), organizeStr(d.Name), organizeStr(d.Manufacturer), organizeStr(d.Model), d.ID,
		}, " ")), search) {
			continue
		}
		if d.DisabledBy != nil && !in.IncludeDisabled {
			disabled++
			continue
		}
		matched = append(matched, d)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].DisplayName() < matched[j].DisplayName() })
	var b strings.Builder
	if len(matched) == 0 {
		b.WriteString("No devices match.\n")
	} else {
		b.WriteString(organizeCount(len(matched), "device", "devices") + ":\n")
	}
	limit := organizeLimit(in.Limit)
	for i, d := range matched {
		if i == limit {
			fmt.Fprintf(&b, "… %d more not shown; narrow with area, label, integration or search, or raise limit.\n", len(matched)-limit)
			break
		}
		b.WriteString(ix.deviceLine(d, counts[d.ID]) + "\n")
	}
	if disabled > 0 {
		fmt.Fprintf(&b, "%s not shown; pass include_disabled to list them.\n", organizeCount(disabled, "disabled device", "disabled devices"))
	}
	return b.String(), nil
}

func (s *Server) organizeListEntities(ctx context.Context, in organizeListRegistryInput) (string, error) {
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{devices: true})
	if err != nil {
		return "", err
	}
	area, label, err := organizeFilters(ix, in)
	if err != nil {
		return "", err
	}
	entities, err := s.ha.ListEntityRegistry(ctx)
	if err != nil {
		return "", err
	}
	search := strings.ToLower(in.Search)
	var matched []homeassistant.EntityRegistryEntry
	disabled := 0
	for _, e := range entities {
		if in.Domain != "" && !strings.HasPrefix(e.EntityID, in.Domain+".") {
			continue
		}
		if in.Integration != "" && e.Platform != in.Integration {
			continue
		}
		if area != "" {
			if id, _ := ix.entityArea(e); id != area {
				continue
			}
		}
		if label != "" && !slices.Contains(e.Labels, label) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.EntityID+" "+ix.entityName(e)+" "+organizeStr(e.OriginalName)), search) {
			continue
		}
		if e.DisabledBy != nil && !in.IncludeDisabled {
			disabled++
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].EntityID < matched[j].EntityID })
	var b strings.Builder
	if len(matched) == 0 {
		b.WriteString("No entities match.\n")
	} else {
		fmt.Fprintf(&b, "%s, as entity_id | name | integration | device (when named differently) | area | labels | flags:\n", organizeCount(len(matched), "entity", "entities"))
	}
	limit := organizeLimit(in.Limit)
	for i, e := range matched {
		if i == limit {
			fmt.Fprintf(&b, "… %d more not shown; narrow with domain, area, label, integration or search, or raise limit.\n", len(matched)-limit)
			break
		}
		b.WriteString(ix.entityLine(e) + "\n")
	}
	if disabled > 0 {
		fmt.Fprintf(&b, "%s not shown; pass include_disabled to list them.\n", organizeCount(disabled, "disabled entity", "disabled entities"))
	}
	return b.String(), nil
}

func (s *Server) organizeListPersons(ctx context.Context) (string, error) {
	p, err := s.ha.ListPersons(ctx)
	if err != nil {
		return "", err
	}
	if len(p.Storage)+len(p.Config) == 0 {
		return "There are no persons.", nil
	}
	var b strings.Builder
	b.WriteString(organizeCount(len(p.Storage)+len(p.Config), "person", "persons") + ":\n")
	line := func(x homeassistant.Person, yaml bool) {
		user := "no login"
		if x.UserID != nil {
			user = "has login"
		}
		trackers := "no trackers"
		if len(x.DeviceTrackers) > 0 {
			trackers = "trackers " + strings.Join(x.DeviceTrackers, ", ")
		}
		src := ""
		if yaml {
			src = "defined in YAML"
		}
		b.WriteString(organizeLine(x.Name, "person."+x.ID, user, trackers, src) + "\n")
	}
	for _, x := range p.Storage {
		line(x, false)
	}
	for _, x := range p.Config {
		line(x, true)
	}
	return b.String(), nil
}

func (s *Server) organizeListZones(ctx context.Context) (string, error) {
	zones, err := s.ha.ListHelpers(ctx, "zone")
	if err != nil {
		return "", err
	}
	entityIDs, err := s.organizeHelperEntityIDs(ctx, "zone")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	home, err := s.ha.GetState(ctx, "zone.home")
	if err == nil {
		b.WriteString(organizeLine("Home", "zone.home", fmt.Sprintf("lat %v, lon %v", home.Attributes["latitude"], home.Attributes["longitude"]),
			fmt.Sprintf("radius %vm", home.Attributes["radius"]), "set in Settings > System > General, not editable here") + "\n")
	} else if !homeassistant.IsNotFound(err) {
		return "", err
	}
	sort.Slice(zones, func(i, j int) bool { return organizeValue(zones[i]["name"]) < organizeValue(zones[j]["name"]) })
	for _, z := range zones {
		id, _ := z["id"].(string)
		b.WriteString(organizeZoneLine(z, entityIDs[id]) + "\n")
	}
	if len(zones) == 0 {
		b.WriteString("No zones have been created in the UI.\n")
	}
	return b.String(), nil
}

// --- ha_list_helpers ---

// organizeHelperDomains are the helper types ha_list_helpers and
// ha_manage_helper cover; zone is a helper to HA's API but a place to a
// person, so it lives with the registry tools.
var organizeHelperDomains = []string{
	"input_boolean", "input_number", "input_select", "input_text", "input_datetime",
	"input_button", "counter", "timer", "schedule",
}

type organizeListHelpersInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"input_boolean, input_number, input_select, input_text, input_datetime, input_button, counter, timer or schedule; all of them when left out"`
}

// organizeHelperEntityIDs maps a storage collection's item ids to entity ids.
// They start out as domain.id but a person can rename the entity, and the
// entity_id is what automations use.
func (s *Server) organizeHelperEntityIDs(ctx context.Context, domains ...string) (map[string]string, error) {
	entries, err := s.ha.ListEntityRegistry(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if slices.Contains(domains, e.Platform) {
			out[e.UniqueID] = e.EntityID
		}
	}
	return out, nil
}

func organizeHelperLine(item map[string]any, entityID string) string {
	name, _ := item["name"].(string)
	id, _ := item["id"].(string)
	keys := make([]string, 0, len(item))
	for k := range item {
		if k != "id" && k != "name" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{name, entityID, "id " + id}
	for _, k := range keys {
		v := item[k]
		if v == nil || v == "" {
			continue
		}
		if list, ok := v.([]any); ok {
			if len(list) == 0 {
				continue
			}
			if ranges, ok := organizeTimeRanges(list); ok {
				parts = append(parts, k+" "+ranges)
				continue
			}
		}
		parts = append(parts, k+" "+organizeValue(v))
	}
	return organizeLine(parts...)
}

// organizeTimeRanges renders a schedule day's blocks as "07:00-09:00, 18:00-22:00",
// which is a fifth of the JSON and reads like the schedule UI.
func organizeTimeRanges(list []any) (string, bool) {
	out := make([]string, 0, len(list))
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			return "", false
		}
		from, ok1 := m["from"].(string)
		to, ok2 := m["to"].(string)
		if !ok1 || !ok2 || len(m) > 3 {
			return "", false
		}
		r := strings.TrimSuffix(from, ":00") + "-" + strings.TrimSuffix(to, ":00")
		if data, ok := m["data"]; ok {
			if dm, _ := data.(map[string]any); len(dm) > 0 {
				r += " " + organizeValue(data)
			}
		}
		out = append(out, r)
	}
	return strings.Join(out, ", "), true
}

func (s *Server) organizeListHelpers(ctx context.Context, _ *mcp.CallToolRequest, in organizeListHelpersInput) (*mcp.CallToolResult, any, error) {
	domains := organizeHelperDomains
	if in.Domain != "" {
		if !slices.Contains(organizeHelperDomains, in.Domain) {
			return fail(fmt.Errorf("domain must be one of %s; got %q", strings.Join(organizeHelperDomains, ", "), in.Domain))
		}
		domains = []string{in.Domain}
	}
	entityIDs, err := s.organizeHelperEntityIDs(ctx, domains...)
	if err != nil {
		return fail(err)
	}
	var b strings.Builder
	total := 0
	for _, d := range domains {
		items, err := s.ha.ListHelpers(ctx, d)
		if err != nil {
			return fail(err)
		}
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool { return organizeValue(items[i]["name"]) < organizeValue(items[j]["name"]) })
		fmt.Fprintf(&b, "%s (%d):\n", d, len(items))
		for _, it := range items {
			id, _ := it["id"].(string)
			b.WriteString(organizeHelperLine(it, entityIDs[id]) + "\n")
		}
		total += len(items)
	}
	if total == 0 {
		return text(fmt.Sprintf("No UI helpers of type %s.", strings.Join(domains, ", "))), nil, nil
	}
	return text(b.String()), nil, nil
}

// --- ha_list_integrations ---

type organizeListIntegrationsInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"only this integration domain, for example hue"`
}

func organizeEntryLine(e homeassistant.ConfigEntry) string {
	state := e.State
	if e.Reason != nil && *e.Reason != "" {
		state += " (" + *e.Reason + ")"
	}
	return organizeLine(e.Title, "domain "+e.Domain, state, organizePrefixed("disabled by ", e.DisabledBy), "entry_id "+e.EntryID)
}

func (s *Server) organizeListIntegrations(ctx context.Context, _ *mcp.CallToolRequest, in organizeListIntegrationsInput) (*mcp.CallToolResult, any, error) {
	entries, err := s.ha.ListConfigEntries(ctx, in.Domain)
	if err != nil {
		return fail(err)
	}
	if len(entries) == 0 {
		if in.Domain != "" {
			return text(fmt.Sprintf("No %s integration is configured.", in.Domain)), nil, nil
		}
		return text("No integrations are configured."), nil, nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		return entries[i].Title < entries[j].Title
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s, as title | domain | state | entry_id:\n", organizeCount(len(entries), "integration entry", "integration entries"))
	problems := 0
	for _, e := range entries {
		b.WriteString(organizeEntryLine(e) + "\n")
		if e.State != "loaded" && e.DisabledBy == nil {
			problems++
		}
	}
	if problems > 0 {
		fmt.Fprintf(&b, "%s not loaded; ha_manage_integration action reload retries them, and the error log says why they failed.\n", organizeCount(problems, "enabled entry is", "enabled entries are"))
	}
	return text(b.String()), nil, nil
}

// --- ha_get_dashboard ---

type organizeGetDashboardInput struct {
	URLPath string `json:"url_path,omitempty" jsonschema:"the dashboard to return, for example 'lovelace' for the default Overview; leave out to list dashboards"`
}

// organizeDashboardPath maps the names a model is likely to use for the
// default dashboard onto its url path.
func organizeDashboardPath(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "default", "overview":
		return "lovelace"
	}
	return p
}

func organizeIsCode(err error, code string) bool {
	var e *homeassistant.Error
	return errors.As(err, &e) && e.Code == code
}

func (s *Server) organizeGetDashboard(ctx context.Context, _ *mcp.CallToolRequest, in organizeGetDashboardInput) (*mcp.CallToolResult, any, error) {
	dashboards, err := s.ha.ListDashboards(ctx)
	if err != nil {
		return fail(err)
	}
	if in.URLPath == "" {
		var b strings.Builder
		b.WriteString("Dashboards, as title | url_path | mode:\n")
		hasDefault := false
		for _, d := range dashboards {
			flags := []string{d.Title, "url_path " + d.URLPath, d.Mode}
			if d.URLPath == "lovelace" {
				hasDefault = true
				flags = append(flags, "default")
			}
			if !d.ShowInSidebar {
				flags = append(flags, "hidden from sidebar")
			}
			if d.RequireAdmin {
				flags = append(flags, "admin only")
			}
			b.WriteString(organizeLine(flags...) + "\n")
		}
		if !hasDefault {
			b.WriteString("Overview | url_path lovelace | default\n")
		}
		return text(b.String()), nil, nil
	}
	path := organizeDashboardPath(in.URLPath)
	raw, err := s.ha.GetDashboardConfig(ctx, path)
	if organizeIsCode(err, "config_not_found") {
		return text(fmt.Sprintf("Dashboard %s has never been edited: Home Assistant generates it from the entities and areas, "+
			"so there is no stored config. Saving a config with ha_save_dashboard replaces the generated one.", path)), nil, nil
	}
	if err != nil {
		return fail(err)
	}
	y, err := organizeYAML(raw)
	if err != nil {
		return fail(err)
	}
	mode := ""
	for _, d := range dashboards {
		if d.URLPath == path && d.Mode == "yaml" {
			mode = " It is in YAML mode, so it is edited in its YAML file and ha_save_dashboard cannot change it."
		}
	}
	var probe struct {
		Strategy any   `json:"strategy"`
		Views    []any `json:"views"`
	}
	_ = json.Unmarshal(raw, &probe)
	shape := organizeCount(len(probe.Views), "view", "views")
	if probe.Strategy != nil {
		shape = "generated by a strategy; saving views replaces the strategy"
	}
	return text(fmt.Sprintf("Dashboard %s, %s.%s\n```yaml\n%s```", path, shape, mode, y)), nil, nil
}

// organizeYAML re-renders a JSON config as block YAML, keeping HA's key order
// because a card reads "type" first and a model editing it should see the same.
func organizeYAML(raw []byte) (string, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	organizeBlockStyle(&n)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func organizeBlockStyle(n *yaml.Node) {
	n.Style = 0
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		if strings.Contains(n.Value, "\n") {
			n.Style = yaml.LiteralStyle
		} else {
			// Encoding the value again quotes strings like "on" and "1:30"
			// that YAML 1.1, which HA uses, would read as a bool or number.
			_ = n.Encode(n.Value)
		}
	}
	for _, c := range n.Content {
		organizeBlockStyle(c)
	}
}

// --- ha_backup_info ---

type organizeBackupInfoInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"most backups to list, newest first; default 10"`
}

func organizeTime(s *string) string {
	if s == nil || *s == "" {
		return "never"
	}
	if t, err := time.Parse(time.RFC3339Nano, *s); err == nil {
		return t.UTC().Format("2006-01-02 15:04 UTC")
	}
	return *s
}

func organizeSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func organizeBackupLine(bk homeassistant.Backup) string {
	agents := make([]string, 0, len(bk.Agents))
	var size int64
	for id, a := range bk.Agents {
		if a.Protected {
			id += " (encrypted)"
		}
		agents = append(agents, id)
		size = max(size, a.Size)
	}
	sort.Strings(agents)
	date := bk.Date
	if t, err := time.Parse(time.RFC3339Nano, bk.Date); err == nil {
		date = t.UTC().Format("2006-01-02 15:04 UTC")
	}
	var contents []string
	if bk.HomeassistantIncluded {
		v := "Home Assistant"
		if bk.HomeassistantVersion != nil {
			v += " " + *bk.HomeassistantVersion
		}
		contents = append(contents, v)
	}
	if bk.DatabaseIncluded {
		contents = append(contents, "database")
	}
	contents = append(contents, bk.Folders...)
	if len(bk.Addons) > 0 {
		contents = append(contents, organizeCount(len(bk.Addons), "add-on", "add-ons"))
	}
	kind := ""
	if bk.WithAutomaticSettings != nil && *bk.WithAutomaticSettings {
		kind = "automatic settings"
	}
	failed := ""
	if len(bk.FailedAgentIDs) > 0 {
		failed = "FAILED to upload to " + strings.Join(bk.FailedAgentIDs, ", ")
	}
	return organizeLine(date, bk.Name, organizeSize(size), "in "+strings.Join(agents, ", "), strings.Join(contents, " + "),
		kind, failed, "backup_id "+bk.BackupID)
}

func (s *Server) organizeBackupInfo(ctx context.Context, _ *mcp.CallToolRequest, in organizeBackupInfoInput) (*mcp.CallToolResult, any, error) {
	var (
		info *homeassistant.BackupInfo
		cfg  *homeassistant.BackupConfig
	)
	err := stateAll(
		func() (err error) { info, err = s.ha.BackupInfo(ctx); return },
		// The settings add to the answer but are not needed for it.
		func() error { cfg, _ = s.ha.BackupConfig(ctx); return nil },
	)
	if err != nil {
		return fail(err)
	}
	out := organizeRenderBackupInfo(info, in.Limit, cfg != nil)
	if cfg != nil {
		out = organizeRenderBackupConfig(cfg) + out
	}
	return text(out), nil, nil
}

// organizeRenderBackupConfig describes the automatic backup settings. The
// encryption password is never shown, only whether one is set.
func organizeRenderBackupConfig(cfg *homeassistant.BackupConfig) string {
	var b strings.Builder
	if !cfg.AutomaticBackupsConfigured {
		b.WriteString("Automatic backups are not set up; ha_create_backup needs them set up in Settings > System > Backups.\n")
	}
	sched := cfg.Schedule
	switch sched.Recurrence {
	case "never", "":
		b.WriteString("Schedule: no automatic backups")
	case "daily":
		b.WriteString("Schedule: daily")
	case "custom_days":
		b.WriteString("Schedule: on " + strings.Join(sched.Days, ", "))
	default:
		b.WriteString("Schedule: " + sched.Recurrence)
	}
	if sched.Recurrence != "never" && sched.Recurrence != "" {
		if sched.Time != nil && *sched.Time != "" {
			b.WriteString(" at " + strings.TrimSuffix(*sched.Time, ":00"))
		} else {
			b.WriteString(" at a time Home Assistant picks")
		}
	}
	b.WriteString(". Keep: " + organizeRetention(&cfg.Retention) + ".\n")

	cb := cfg.CreateBackup
	contents := []string{"Home Assistant settings"}
	if cb.IncludeDatabase {
		contents = append(contents, "history database")
	}
	contents = append(contents, cb.IncludeFolders...)
	switch {
	case cb.IncludeAllAddons:
		contents = append(contents, "all add-ons")
	case len(cb.IncludeAddons) > 0:
		contents = append(contents, "add-ons "+strings.Join(cb.IncludeAddons, ", "))
	}
	enc := "not encrypted"
	if cb.Encrypted {
		enc = "encrypted with the stored key"
	}
	fmt.Fprintf(&b, "Automatic backups contain: %s; %s.\n", strings.Join(contents, " + "), enc)

	if len(cb.AgentIDs) == 0 {
		b.WriteString("Storage locations: none chosen.\n")
	} else {
		locs := make([]string, 0, len(cb.AgentIDs))
		for _, id := range cb.AgentIDs {
			var notes []string
			if a, ok := cfg.Agents[id]; ok {
				if !a.Protected && cb.Encrypted {
					notes = append(notes, "unencrypted")
				}
				if a.Retention != nil {
					notes = append(notes, "keeps "+organizeRetention(a.Retention))
				}
			}
			if len(notes) > 0 {
				id += " (" + strings.Join(notes, ", ") + ")"
			}
			locs = append(locs, id)
		}
		fmt.Fprintf(&b, "Storage locations: %s.\n", strings.Join(locs, ", "))
	}
	return b.String()
}

func organizeRetention(r *homeassistant.BackupRetention) string {
	switch {
	case r.Copies != nil:
		return organizeCount(*r.Copies, "newest backup", "newest backups")
	case r.Days != nil:
		return fmt.Sprintf("backups from the last %s", organizeCount(*r.Days, "day", "days"))
	}
	return "all backups"
}

func organizeRenderBackupInfo(info *homeassistant.BackupInfo, limit int, settingsShown bool) string {
	var b strings.Builder
	state := info.State
	if state == "" {
		state = "idle"
	}
	fmt.Fprintf(&b, "Backup manager: %s.\n", state)
	fmt.Fprintf(&b, "Automatic backups: last attempted %s, last completed %s", organizeTime(info.LastAttemptedAutomaticBackup), organizeTime(info.LastCompletedAutomaticBackup))
	if info.NextAutomaticBackup != nil {
		fmt.Fprintf(&b, ", next %s", organizeTime(info.NextAutomaticBackup))
	} else {
		b.WriteString(", none scheduled")
	}
	b.WriteString(".\n")
	if !settingsShown && info.LastAttemptedAutomaticBackup == nil && info.LastCompletedAutomaticBackup == nil && info.NextAutomaticBackup == nil {
		b.WriteString("Automatic backups look unconfigured; ha_create_backup needs them set up in Settings > System > Backups.\n")
	}
	if len(info.LastActionEvent) > 0 && string(info.LastActionEvent) != "null" {
		var ev struct {
			ManagerState string  `json:"manager_state"`
			State        string  `json:"state"`
			Reason       *string `json:"reason"`
		}
		if json.Unmarshal(info.LastActionEvent, &ev) == nil && ev.ManagerState != "" {
			fmt.Fprintf(&b, "Last action: %s %s", strings.ReplaceAll(ev.ManagerState, "_", " "), ev.State)
			if ev.Reason != nil && *ev.Reason != "" {
				fmt.Fprintf(&b, " (%s)", oneLine(*ev.Reason))
			}
			b.WriteString(".\n")
		}
	}
	for agent, msg := range info.AgentErrors {
		fmt.Fprintf(&b, "Storage location %s could not be read: %s\n", oneLine(agent), oneLine(msg))
	}
	backups := slices.Clone(info.Backups)
	sort.Slice(backups, func(i, j int) bool { return backups[i].Date > backups[j].Date })
	if len(backups) == 0 {
		b.WriteString("There are no backups.\n")
		return b.String()
	}
	if limit <= 0 {
		limit = 10
	}
	b.WriteString(organizeCount(len(backups), "backup", "backups") + ", newest first:\n")
	for i, bk := range backups {
		if i == limit {
			fmt.Fprintf(&b, "… %d older not shown; raise limit to see them.\n", len(backups)-limit)
			break
		}
		b.WriteString(organizeBackupLine(bk) + "\n")
	}
	return b.String()
}

// --- ha_manage_registry ---

type organizeManageRegistryInput struct {
	Resource    string   `json:"resource" jsonschema:"area, floor, label, category, device, entity or zone"`
	Action      string   `json:"action" jsonschema:"create, update or delete; devices can only be updated, entities updated or deleted"`
	ID          string   `json:"id,omitempty" jsonschema:"what to update or delete: an area, floor, label, category or zone by id or name, a device by device_id or name, an entity by entity_id"`
	Scope       string   `json:"scope,omitempty" jsonschema:"categories only, required: automation, script, scene or helpers"`
	Name        *string  `json:"name,omitempty" jsonschema:"the name; for a device or entity an empty string removes the user-set name so the integration's name shows again"`
	Icon        *string  `json:"icon,omitempty" jsonschema:"an icon such as mdi:sofa; empty string removes it"`
	Aliases     []string `json:"aliases,omitempty" jsonschema:"areas and floors: other names voice assistants accept; replaces the existing ones, [] removes them"`
	Floor       *string  `json:"floor,omitempty" jsonschema:"areas: the floor id or name; empty string takes the area off its floor"`
	Level       *int     `json:"level,omitempty" jsonschema:"floors: the level, 0 for ground, negative below ground"`
	Area        *string  `json:"area,omitempty" jsonschema:"devices and entities: the area id or name; empty string removes it (an entity then follows its device's area)"`
	Labels      []string `json:"labels,omitempty" jsonschema:"areas, devices and entities: label ids or names; replaces the existing ones, [] removes them all"`
	Color       *string  `json:"color,omitempty" jsonschema:"labels: a color such as red, indigo or a hex value like #ff9800; empty string removes it"`
	Description *string  `json:"description,omitempty" jsonschema:"labels: what the label is for; empty string removes it"`
	Disabled    *bool    `json:"disabled,omitempty" jsonschema:"devices and entities: true disables it (it stops updating and leaves the state machine), false enables it"`
	Hidden      *bool    `json:"hidden,omitempty" jsonschema:"entities: true hides it from generated dashboards and voice assistants; it keeps working"`
	NewEntityID string   `json:"new_entity_id,omitempty" jsonschema:"entities: a new entity_id in the same domain; needs confirm because everything using the old id breaks"`
	Latitude    *float64 `json:"latitude,omitempty" jsonschema:"zones: latitude of the center; required to create"`
	Longitude   *float64 `json:"longitude,omitempty" jsonschema:"zones: longitude of the center; required to create"`
	Radius      *float64 `json:"radius,omitempty" jsonschema:"zones: radius in meters, default 100"`
	Passive     *bool    `json:"passive,omitempty" jsonschema:"zones: true makes it usable in automations only, never shown as a person's location"`
	Confirm     bool     `json:"confirm,omitempty" jsonschema:"required to delete anything and to change an entity_id"`
}

func organizeNullable(p *string) homeassistant.Nullable[string] {
	switch {
	case p == nil:
		return homeassistant.Nullable[string]{}
	case *p == "":
		return homeassistant.Null[string]()
	}
	return homeassistant.Set(*p)
}

func (s *Server) organizeManageRegistry(ctx context.Context, _ *mcp.CallToolRequest, in organizeManageRegistryInput) (*mcp.CallToolResult, any, error) {
	if in.Action == "remove" {
		in.Action = "delete"
	}
	if in.Action != "create" && in.Action != "update" && in.Action != "delete" {
		return fail(fmt.Errorf("action must be create, update or delete; got %q", in.Action))
	}
	if in.Action != "create" && in.ID == "" {
		return fail(fmt.Errorf("id is required to %s a %s", in.Action, in.Resource))
	}
	var (
		out string
		err error
	)
	switch in.Resource {
	case "area":
		out, err = s.organizeManageArea(ctx, in)
	case "floor":
		out, err = s.organizeManageFloor(ctx, in)
	case "label":
		out, err = s.organizeManageLabel(ctx, in)
	case "category":
		out, err = s.organizeManageCategory(ctx, in)
	case "device":
		out, err = s.organizeManageDevice(ctx, in)
	case "entity":
		out, err = s.organizeManageEntity(ctx, in)
	case "zone":
		out, err = s.organizeManageZone(ctx, in)
	case "person":
		err = errors.New("persons are managed in Settings > People in Home Assistant, not here")
	default:
		err = fmt.Errorf("resource must be area, floor, label, category, device, entity or zone; got %q", in.Resource)
	}
	if err != nil {
		return fail(err)
	}
	return text(out), nil, nil
}

func organizeNeedConfirm(what string) error {
	return fmt.Errorf("deleting %s cannot be undone. Check with the person first, then call again with confirm:true", what)
}

func (s *Server) organizeManageArea(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{floors: true})
	if err != nil {
		return "", err
	}
	var floorID *string
	if in.Floor != nil {
		id := ""
		if *in.Floor != "" {
			r, err := organizeResolve("floor", *in.Floor, ix.floorRefs())
			if err != nil {
				return "", err
			}
			id = r.id
		}
		floorID = &id
	}
	var labels []string
	if in.Labels != nil {
		if labels, err = ix.resolveLabels(in.Labels); err != nil {
			return "", err
		}
	}
	switch in.Action {
	case "create":
		if in.Name == nil || *in.Name == "" {
			return "", errors.New("name is required to create an area")
		}
		a, err := s.ha.CreateArea(ctx, homeassistant.AreaCreate{
			Name: *in.Name, Aliases: in.Aliases, FloorID: organizeStr(floorID), Icon: organizeStr(in.Icon), Labels: labels,
		})
		if err != nil {
			return "", err
		}
		return "Created area: " + organizeAreaLine(*a, ix), nil
	}
	cur, err := organizeResolve("area", in.ID, ix.areaRefs())
	if err != nil {
		return "", err
	}
	before := organizeAreaLine(ix.areas[cur.id], ix)
	if in.Action == "delete" {
		if !in.Confirm {
			return "", organizeNeedConfirm("area " + cur.name + " (its devices and entities lose their area)")
		}
		if err := s.ha.DeleteArea(ctx, cur.id); err != nil {
			return "", err
		}
		return "Deleted area: " + before, nil
	}
	u := homeassistant.AreaUpdate{Aliases: in.Aliases, Labels: labels, FloorID: organizeNullable(floorID), Icon: organizeNullable(in.Icon)}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if u.Name == "" && u.Aliases == nil && u.Labels == nil && floorID == nil && in.Icon == nil {
		return "", errors.New("nothing to change; pass name, floor, icon, aliases or labels")
	}
	a, err := s.ha.UpdateArea(ctx, cur.id, u)
	if err != nil {
		return "", err
	}
	return "Updated area.\nBefore: " + before + "\nAfter:  " + organizeAreaLine(*a, ix), nil
}

func (s *Server) organizeManageFloor(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{floors: true})
	if err != nil {
		return "", err
	}
	areasOn := func(floorID string) []string {
		var out []string
		for _, r := range ix.areaRefs() {
			if organizeStr(ix.areas[r.id].FloorID) == floorID {
				out = append(out, r.name)
			}
		}
		return out
	}
	switch in.Action {
	case "create":
		if in.Name == nil || *in.Name == "" {
			return "", errors.New("name is required to create a floor")
		}
		f, err := s.ha.CreateFloor(ctx, homeassistant.FloorCreate{Name: *in.Name, Aliases: in.Aliases, Icon: organizeStr(in.Icon), Level: in.Level})
		if err != nil {
			return "", err
		}
		return "Created floor: " + organizeFloorLine(*f, nil) + "\nAssign areas to it with resource area, action update, floor " + f.FloorID + ".", nil
	}
	cur, err := organizeResolve("floor", in.ID, ix.floorRefs())
	if err != nil {
		return "", err
	}
	before := organizeFloorLine(ix.floors[cur.id], areasOn(cur.id))
	if in.Action == "delete" {
		if !in.Confirm {
			return "", organizeNeedConfirm("floor " + cur.name + " (its areas stay, on no floor)")
		}
		if err := s.ha.DeleteFloor(ctx, cur.id); err != nil {
			return "", err
		}
		return "Deleted floor: " + before, nil
	}
	u := homeassistant.FloorUpdate{Aliases: in.Aliases, Icon: organizeNullable(in.Icon)}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if in.Level != nil {
		u.Level = homeassistant.Set(*in.Level)
	}
	if u.Name == "" && u.Aliases == nil && in.Icon == nil && in.Level == nil {
		return "", errors.New("nothing to change; pass name, level, icon or aliases")
	}
	f, err := s.ha.UpdateFloor(ctx, cur.id, u)
	if err != nil {
		return "", err
	}
	return "Updated floor.\nBefore: " + before + "\nAfter:  " + organizeFloorLine(*f, areasOn(cur.id)), nil
}

func (s *Server) organizeManageLabel(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	switch in.Action {
	case "create":
		if in.Name == nil || *in.Name == "" {
			return "", errors.New("name is required to create a label")
		}
		l, err := s.ha.CreateLabel(ctx, homeassistant.LabelCreate{
			Name: *in.Name, Color: organizeStr(in.Color), Description: organizeStr(in.Description), Icon: organizeStr(in.Icon),
		})
		if err != nil {
			return "", err
		}
		return "Created label: " + organizeLabelLine(*l), nil
	}
	labels, err := s.ha.ListLabels(ctx)
	if err != nil {
		return "", err
	}
	refs := make([]organizeRef, len(labels))
	byID := map[string]homeassistant.Label{}
	for i, l := range labels {
		refs[i] = organizeRef{l.LabelID, l.Name}
		byID[l.LabelID] = l
	}
	cur, err := organizeResolve("label", in.ID, refs)
	if err != nil {
		return "", err
	}
	before := organizeLabelLine(byID[cur.id])
	if in.Action == "delete" {
		if !in.Confirm {
			return "", organizeNeedConfirm("label " + cur.name + " (it is removed from everything that has it)")
		}
		if err := s.ha.DeleteLabel(ctx, cur.id); err != nil {
			return "", err
		}
		return "Deleted label: " + before, nil
	}
	u := homeassistant.LabelUpdate{Color: organizeNullable(in.Color), Description: organizeNullable(in.Description), Icon: organizeNullable(in.Icon)}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if u.Name == "" && in.Color == nil && in.Description == nil && in.Icon == nil {
		return "", errors.New("nothing to change; pass name, color, description or icon")
	}
	l, err := s.ha.UpdateLabel(ctx, cur.id, u)
	if err != nil {
		return "", err
	}
	return "Updated label.\nBefore: " + before + "\nAfter:  " + organizeLabelLine(*l), nil
}

func (s *Server) organizeManageCategory(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	if in.Scope == "" {
		return "", errors.New("scope is required for categories: automation, script, scene or helpers")
	}
	switch in.Action {
	case "create":
		if in.Name == nil || *in.Name == "" {
			return "", errors.New("name is required to create a category")
		}
		c, err := s.ha.CreateCategory(ctx, in.Scope, homeassistant.CategoryCreate{Name: *in.Name, Icon: organizeStr(in.Icon)})
		if err != nil {
			return "", err
		}
		return "Created category: " + organizeCategoryLine(in.Scope, *c), nil
	}
	cats, err := s.ha.ListCategories(ctx, in.Scope)
	if err != nil {
		return "", err
	}
	refs := make([]organizeRef, len(cats))
	byID := map[string]homeassistant.Category{}
	for i, c := range cats {
		refs[i] = organizeRef{c.CategoryID, c.Name}
		byID[c.CategoryID] = c
	}
	cur, err := organizeResolve(in.Scope+" category", in.ID, refs)
	if err != nil {
		return "", err
	}
	before := organizeCategoryLine(in.Scope, byID[cur.id])
	if in.Action == "delete" {
		if !in.Confirm {
			return "", organizeNeedConfirm("category " + cur.name + " (its items become uncategorized)")
		}
		if err := s.ha.DeleteCategory(ctx, in.Scope, cur.id); err != nil {
			return "", err
		}
		return "Deleted category: " + before, nil
	}
	u := homeassistant.CategoryUpdate{Icon: organizeNullable(in.Icon)}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if u.Name == "" && in.Icon == nil {
		return "", errors.New("nothing to change; pass name or icon")
	}
	c, err := s.ha.UpdateCategory(ctx, in.Scope, cur.id, u)
	if err != nil {
		return "", err
	}
	return "Updated category.\nBefore: " + before + "\nAfter:  " + organizeCategoryLine(in.Scope, *c), nil
}

func (s *Server) organizeManageDevice(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	if in.Action != "update" {
		return "", errors.New("devices can only be updated; they are added and removed by their integration")
	}
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{devices: true, entries: true})
	if err != nil {
		return "", err
	}
	refs := make([]organizeRef, 0, len(ix.devices))
	for _, d := range ix.devices {
		refs = append(refs, organizeRef{d.ID, d.DisplayName()})
	}
	cur, err := organizeResolve("device", in.ID, refs)
	if err != nil {
		return "", err
	}
	var u homeassistant.DeviceUpdate
	var changed []string
	if in.Name != nil {
		u.NameByUser = organizeNullable(in.Name)
		changed = append(changed, "name")
	}
	if in.Area != nil {
		area := ""
		if *in.Area != "" {
			r, err := organizeResolve("area", *in.Area, ix.areaRefs())
			if err != nil {
				return "", err
			}
			area = r.id
		}
		u.AreaID = organizeNullable(&area)
		changed = append(changed, "area")
	}
	if in.Labels != nil {
		if u.Labels, err = ix.resolveLabels(in.Labels); err != nil {
			return "", err
		}
		changed = append(changed, "labels")
	}
	if in.Disabled != nil {
		if *in.Disabled {
			u.DisabledBy = homeassistant.Set("user")
		} else {
			u.DisabledBy = homeassistant.Null[string]()
		}
		changed = append(changed, "disabled")
	}
	if len(changed) == 0 {
		return "", errors.New("nothing to change; a device takes name, area, labels or disabled")
	}
	before := ix.deviceLine(ix.devices[cur.id], -1)
	d, err := s.ha.UpdateDevice(ctx, cur.id, u)
	if err != nil {
		return "", err
	}
	out := "Updated device.\nBefore: " + before + "\nAfter:  " + ix.deviceLine(*d, -1)
	if in.Area != nil {
		out += "\nIts entities without an area of their own follow the device's area."
	}
	return out, nil
}

// organizeReferenceTypes are the search/related result types that use an
// entity, as opposed to the device, area or integration that contain it.
var organizeReferenceTypes = []homeassistant.SearchItemType{
	homeassistant.SearchAutomation, homeassistant.SearchScript, homeassistant.SearchScene,
	homeassistant.SearchGroup, homeassistant.SearchPerson,
}

func (s *Server) organizeManageEntity(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	if in.Action == "create" {
		return "", errors.New("entities are created by their integration; helpers are created with ha_manage_helper")
	}
	ix, err := s.organizeLoadIndex(ctx, organizeNeed{devices: true})
	if err != nil {
		return "", err
	}
	cur, err := s.ha.GetEntityRegistryEntry(ctx, in.ID)
	if homeassistant.IsNotFound(err) {
		return "", fmt.Errorf("%s is not in the entity registry; entities without a unique id cannot be changed here", in.ID)
	}
	if err != nil {
		return "", err
	}
	before := ix.entityLine(*cur)
	if in.Action == "delete" {
		if !in.Confirm {
			return "", fmt.Errorf("deleting removes %s from the entity registry, losing its name, area and labels. "+
				"If its integration still provides it, it comes back with the defaults on the next reload. "+
				"Check with the person first, then call again with confirm:true", in.ID)
		}
		if err := s.ha.RemoveEntityRegistryEntry(ctx, in.ID); err != nil {
			return "", err
		}
		return "Removed from the entity registry: " + before, nil
	}

	var u homeassistant.EntityUpdate
	var changed []string
	if in.Name != nil {
		u.Name = organizeNullable(in.Name)
		changed = append(changed, "name")
	}
	if in.Icon != nil {
		u.Icon = organizeNullable(in.Icon)
		changed = append(changed, "icon")
	}
	if in.Area != nil {
		area := ""
		if *in.Area != "" {
			r, err := organizeResolve("area", *in.Area, ix.areaRefs())
			if err != nil {
				return "", err
			}
			area = r.id
		}
		u.AreaID = organizeNullable(&area)
		changed = append(changed, "area")
	}
	if in.Labels != nil {
		if u.Labels, err = ix.resolveLabels(in.Labels); err != nil {
			return "", err
		}
		changed = append(changed, "labels")
	}
	if in.Disabled != nil {
		if *in.Disabled {
			u.DisabledBy = homeassistant.Set("user")
		} else {
			u.DisabledBy = homeassistant.Null[string]()
		}
		changed = append(changed, "disabled")
	}
	if in.Hidden != nil {
		if *in.Hidden {
			u.HiddenBy = homeassistant.Set("user")
		} else {
			u.HiddenBy = homeassistant.Null[string]()
		}
		changed = append(changed, "hidden")
	}
	if in.NewEntityID != "" && in.NewEntityID != in.ID {
		if !in.Confirm {
			return "", s.organizeRenameRefusal(ctx, in.ID, in.NewEntityID)
		}
		u.NewEntityID = in.NewEntityID
		changed = append(changed, "entity_id")
	}
	if len(changed) == 0 {
		return "", errors.New("nothing to change; an entity takes name, icon, area, labels, disabled, hidden or new_entity_id")
	}
	res, err := s.ha.UpdateEntityRegistryEntry(ctx, in.ID, u)
	if err != nil {
		return "", err
	}
	out := "Updated entity.\nBefore: " + before + "\nAfter:  " + ix.entityLine(res.Entry)
	switch {
	case res.RequireRestart:
		out += "\nHome Assistant needs a restart before the entity is enabled."
	case res.ReloadDelay > 0:
		out += fmt.Sprintf("\nThe integration reloads in about %d seconds to enable the entity.", res.ReloadDelay)
	}
	if u.NewEntityID != "" {
		out += "\nUpdate anything that still uses " + in.ID + ": automations, scripts, dashboards and templates are not rewritten."
	}
	return out, nil
}

// organizeRenameRefusal is what a rename without confirm returns: the
// references are the thing the person needs to see before agreeing, and HA
// does not rewrite them.
func (s *Server) organizeRenameRefusal(ctx context.Context, from, to string) error {
	related, err := s.ha.SearchRelated(ctx, homeassistant.SearchEntity, from)
	if err != nil {
		return fmt.Errorf("could not check what references %s before renaming it: %w", from, err)
	}
	var refs []string
	for _, t := range organizeReferenceTypes {
		for _, id := range related[t] {
			refs = append(refs, fmt.Sprintf("%s %s", t, id))
		}
	}
	found := "No automation, script, scene, group or person references it"
	if len(refs) > 0 {
		found = "These reference it and will break: " + strings.Join(refs, ", ")
	}
	return fmt.Errorf("renaming %s to %s is not applied to anything that uses the old id. %s. Dashboards and templates are not "+
		"searched and may use it too. Check with the person, then call again with confirm:true", from, to, found)
}

func (s *Server) organizeManageZone(ctx context.Context, in organizeManageRegistryInput) (string, error) {
	fields := map[string]any{}
	if in.Name != nil {
		fields["name"] = *in.Name
	}
	if in.Icon != nil {
		if *in.Icon == "" {
			fields["icon"] = nil
		} else {
			fields["icon"] = *in.Icon
		}
	}
	if in.Latitude != nil {
		fields["latitude"] = *in.Latitude
	}
	if in.Longitude != nil {
		fields["longitude"] = *in.Longitude
	}
	if in.Radius != nil {
		fields["radius"] = *in.Radius
	}
	if in.Passive != nil {
		fields["passive"] = *in.Passive
	}
	if in.Action == "create" {
		if in.Name == nil || in.Latitude == nil || in.Longitude == nil {
			return "", errors.New("name, latitude and longitude are required to create a zone")
		}
		if fields["icon"] == nil {
			delete(fields, "icon")
		}
		z, err := s.ha.CreateHelper(ctx, "zone", fields)
		if err != nil {
			return "", err
		}
		id, _ := z["id"].(string)
		return "Created zone: " + organizeZoneLine(z, "zone."+id), nil
	}
	zones, err := s.ha.ListHelpers(ctx, "zone")
	if err != nil {
		return "", err
	}
	refs := make([]organizeRef, 0, len(zones))
	byID := map[string]map[string]any{}
	for _, z := range zones {
		id, _ := z["id"].(string)
		name, _ := z["name"].(string)
		refs = append(refs, organizeRef{id, name})
		byID[id] = z
	}
	id := strings.TrimPrefix(in.ID, "zone.")
	if id == "home" {
		return "", errors.New("the home zone is the location set in Settings > System > General and cannot be changed here")
	}
	entityIDs, err := s.organizeHelperEntityIDs(ctx, "zone")
	if err != nil {
		return "", err
	}
	for uid, eid := range entityIDs {
		if eid == in.ID {
			id = uid
		}
	}
	cur, err := organizeResolve("zone", id, refs)
	if err != nil {
		return "", err
	}
	before := organizeZoneLine(byID[cur.id], entityIDs[cur.id])
	if in.Action == "delete" {
		if !in.Confirm {
			return "", organizeNeedConfirm("zone " + cur.name + " (automations using it stop matching)")
		}
		if err := s.ha.DeleteHelper(ctx, "zone", cur.id); err != nil {
			return "", err
		}
		return "Deleted zone: " + before, nil
	}
	if len(fields) == 0 {
		return "", errors.New("nothing to change; a zone takes name, latitude, longitude, radius, passive or icon")
	}
	z, err := s.ha.UpdateHelper(ctx, "zone", cur.id, fields)
	if err != nil {
		return "", err
	}
	return "Updated zone.\nBefore: " + before + "\nAfter:  " + organizeZoneLine(z, entityIDs[cur.id]), nil
}

// --- ha_manage_helper ---

type organizeManageHelperInput struct {
	Domain  string         `json:"domain" jsonschema:"input_boolean, input_number, input_select, input_text, input_datetime, input_button, counter, timer or schedule"`
	Action  string         `json:"action" jsonschema:"create, update or delete"`
	ID      string         `json:"id,omitempty" jsonschema:"the helper's id or entity_id; required for update and delete"`
	Fields  map[string]any `json:"fields,omitempty" jsonschema:"the settings to set, as described for the domain; on update a null value removes a setting"`
	Confirm bool           `json:"confirm,omitempty" jsonschema:"required for delete"`
}

func (s *Server) organizeManageHelper(ctx context.Context, _ *mcp.CallToolRequest, in organizeManageHelperInput) (*mcp.CallToolResult, any, error) {
	if !slices.Contains(organizeHelperDomains, in.Domain) {
		if in.Domain == "zone" {
			return fail(errors.New("zones are managed with ha_manage_registry, resource zone"))
		}
		return fail(fmt.Errorf("domain must be one of %s; got %q", strings.Join(organizeHelperDomains, ", "), in.Domain))
	}
	if in.Action == "create" {
		if name, _ := in.Fields["name"].(string); name == "" {
			return fail(errors.New("fields.name is required to create a helper"))
		}
		item, err := s.ha.CreateHelper(ctx, in.Domain, in.Fields)
		if err != nil {
			return fail(err)
		}
		id, _ := item["id"].(string)
		entityIDs, err := s.organizeHelperEntityIDs(ctx, in.Domain)
		if err != nil {
			return fail(err)
		}
		eid := entityIDs[id]
		if eid == "" {
			eid = in.Domain + "." + id
		}
		return text("Created " + in.Domain + ": " + organizeHelperLine(item, eid)), nil, nil
	}
	if in.Action != "update" && in.Action != "delete" {
		return fail(fmt.Errorf("action must be create, update or delete; got %q", in.Action))
	}
	if in.ID == "" {
		return fail(fmt.Errorf("id is required to %s a helper", in.Action))
	}
	items, err := s.ha.ListHelpers(ctx, in.Domain)
	if err != nil {
		return fail(err)
	}
	entityIDs, err := s.organizeHelperEntityIDs(ctx, in.Domain)
	if err != nil {
		return fail(err)
	}
	id := in.ID
	for uid, eid := range entityIDs {
		if eid == in.ID {
			id = uid
		}
	}
	var cur map[string]any
	for _, it := range items {
		if it["id"] == id {
			cur = it
		}
	}
	if cur == nil {
		return fail(fmt.Errorf("no UI %s helper with id or entity_id %q; ha_list_helpers shows them (helpers defined in YAML cannot be changed here)", in.Domain, in.ID))
	}
	before := organizeHelperLine(cur, entityIDs[id])
	if in.Action == "delete" {
		if !in.Confirm {
			return fail(organizeNeedConfirm(in.Domain + " " + organizeValue(cur["name"]) + " (automations and dashboards using it break)"))
		}
		if err := s.ha.DeleteHelper(ctx, in.Domain, id); err != nil {
			return fail(err)
		}
		return text("Deleted " + in.Domain + ": " + before), nil, nil
	}
	if len(in.Fields) == 0 {
		return fail(errors.New("fields is required to update a helper"))
	}
	item, err := s.ha.UpdateHelper(ctx, in.Domain, id, in.Fields)
	if err != nil {
		return fail(err)
	}
	return text("Updated " + in.Domain + ".\nBefore: " + before + "\nAfter:  " + organizeHelperLine(item, entityIDs[id])), nil, nil
}

// --- ha_manage_integration ---

type organizeManageIntegrationInput struct {
	EntryID string `json:"entry_id" jsonschema:"the integration entry's entry_id from ha_list_integrations"`
	Action  string `json:"action" jsonschema:"reload, enable or disable"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"required to disable"`
}

func (s *Server) organizeManageIntegration(ctx context.Context, _ *mcp.CallToolRequest, in organizeManageIntegrationInput) (*mcp.CallToolResult, any, error) {
	if in.EntryID == "" {
		return fail(errors.New("entry_id is required; ha_list_integrations lists them"))
	}
	entry, err := s.ha.GetConfigEntry(ctx, in.EntryID)
	if err != nil {
		return fail(err)
	}
	label := fmt.Sprintf("%s (%s)", oneLine(entry.Title), entry.Domain)
	var restart bool
	var did string
	switch in.Action {
	case "reload":
		if entry.DisabledBy != nil {
			return fail(fmt.Errorf("%s is disabled; enable it instead of reloading", label))
		}
		restart, err = s.ha.ReloadConfigEntry(ctx, in.EntryID)
		did = "Reloaded"
	case "enable":
		restart, err = s.ha.SetConfigEntryDisabled(ctx, in.EntryID, false)
		did = "Enabled"
	case "disable":
		if !in.Confirm {
			return fail(fmt.Errorf("disabling %s makes all its devices and entities unavailable until it is enabled again. Check with the person, then call again with confirm:true", label))
		}
		restart, err = s.ha.SetConfigEntryDisabled(ctx, in.EntryID, true)
		did = "Disabled"
	default:
		return fail(fmt.Errorf("action must be reload, enable or disable; got %q", in.Action))
	}
	if err != nil {
		return fail(err)
	}
	out := did + " " + label + "."
	if restart {
		out += " Home Assistant needs a restart to finish this."
	}
	if after, err := s.ha.GetConfigEntry(ctx, in.EntryID); err == nil {
		out += "\nNow: " + organizeEntryLine(*after)
	}
	return text(out), nil, nil
}

// --- ha_save_dashboard ---

type organizeSaveDashboardInput struct {
	URLPath string `json:"url_path" jsonschema:"the dashboard's url_path, 'lovelace' for the default Overview"`
	Config  any    `json:"config" jsonschema:"the whole dashboard config: YAML text, JSON text or an object, with a top-level views list or strategy"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"required when the dashboard already has a stored config, which this replaces"`
}

// organizeParseConfig returns the config as JSON with its keys in the order
// they were written, because HA stores and shows the dashboard in that order.
// An object argument is taken from the raw request arguments, since decoding
// it into the input struct has already lost the order.
func organizeParseConfig(req *mcp.CallToolRequest, v any) (json.RawMessage, error) {
	switch c := v.(type) {
	case map[string]any:
		if req != nil && req.Params != nil {
			var raw struct {
				Config json.RawMessage `json:"config"`
			}
			if json.Unmarshal(req.Params.Arguments, &raw) == nil && len(raw.Config) > 0 && raw.Config[0] == '{' {
				return raw.Config, nil
			}
		}
		return json.Marshal(c)
	case string:
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(c), &n); err != nil {
			return nil, fmt.Errorf("config is not valid YAML or JSON: %w", err)
		}
		if err := configYAMLTags(&n); err != nil {
			return nil, err
		}
		if len(n.Content) == 0 || n.Content[0].Kind != yaml.MappingNode {
			return nil, errors.New("config must be a mapping with a top-level views list")
		}
		var b bytes.Buffer
		if err := organizeNodeJSON(&b, n.Content[0]); err != nil {
			return nil, fmt.Errorf("config cannot be sent as JSON: %w", err)
		}
		return b.Bytes(), nil
	case nil:
		return nil, errors.New("config is required")
	}
	return nil, fmt.Errorf("config must be YAML or JSON text or an object, got %T", v)
}

// configYAMLTags rejects tags such as !secret and !include in a config the
// agent wrote. The config reaches HA as JSON, where a tag would silently turn
// into its plain value instead of doing what the author meant.
func configYAMLTags(n *yaml.Node) error {
	if err := homeassistant.CheckYAMLTags(n); err != nil {
		return fmt.Errorf("%w; configs saved through the API cannot use YAML tags such as !secret, !include or !input", err)
	}
	return nil
}

// organizeNodeJSON writes a YAML node tree as JSON, keeping mapping order.
func organizeNodeJSON(b *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.AliasNode:
		return organizeNodeJSON(b, n.Alias)
	case yaml.MappingNode:
		b.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag == "!!merge" {
				return fmt.Errorf("line %d: only plain keys are supported", k.Line)
			}
			if i > 0 {
				b.WriteByte(',')
			}
			key, _ := json.Marshal(k.Value)
			b.Write(key)
			b.WriteByte(':')
			if err := organizeNodeJSON(b, n.Content[i+1]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := organizeNodeJSON(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if n.Tag == "!!timestamp" || n.Tag == "!!binary" {
			v = n.Value
		} else if err := n.Decode(&v); err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		b.Write(out)
	default:
		return fmt.Errorf("line %d: unsupported YAML node", n.Line)
	}
	return nil
}

func (s *Server) organizeSaveDashboard(ctx context.Context, req *mcp.CallToolRequest, in organizeSaveDashboardInput) (*mcp.CallToolResult, any, error) {
	if in.URLPath == "" {
		return fail(errors.New("url_path is required; ha_get_dashboard lists them, 'lovelace' is the default dashboard"))
	}
	path := organizeDashboardPath(in.URLPath)
	body, err := organizeParseConfig(req, in.Config)
	if err != nil {
		return fail(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		return fail(fmt.Errorf("config must be a mapping with a top-level views list: %w", err))
	}
	_, hasViews := cfg["views"]
	_, hasStrategy := cfg["strategy"]
	if !hasViews && !hasStrategy {
		return fail(errors.New("config needs a top-level views list (or a strategy); pass the whole dashboard, not one view or card"))
	}
	dashboards, err := s.ha.ListDashboards(ctx)
	if err != nil {
		return fail(err)
	}
	for _, d := range dashboards {
		if d.URLPath == path && d.Mode == "yaml" {
			return fail(fmt.Errorf("dashboard %s is in YAML mode; it is edited in its YAML file, not through the API", path))
		}
	}
	prev, err := s.ha.GetDashboardConfig(ctx, path)
	generated := organizeIsCode(err, "config_not_found")
	if err != nil && !generated {
		return fail(err)
	}
	if !generated && !in.Confirm {
		return fail(fmt.Errorf("dashboard %s already has a config and this replaces all of it. Make sure the new config is the "+
			"whole dashboard (ha_get_dashboard returns the current one), then call again with confirm:true", path))
	}
	if err := s.ha.SaveDashboardConfig(ctx, path, body); err != nil {
		return fail(err)
	}
	views, _ := cfg["views"].([]any)
	out := fmt.Sprintf("Saved dashboard %s (%s).", path, organizeCount(len(views), "view", "views"))
	if generated {
		return text(out + " It was auto-generated before. To go back to the generated dashboard, the person can clear " +
			"the config in the dashboard's raw configuration editor and save."), nil, nil
	}
	y, err := organizeYAML(prev)
	if err != nil {
		return text(out), nil, nil
	}
	return text(out + " The previous config, to revert by saving it back:\n```yaml\n" + y + "```"), nil, nil
}

// --- ha_create_backup ---

type organizeCreateBackupInput struct{}

// organizeBackupWait bounds how long ha_create_backup holds the call open; a
// variable so tests do not wait it out.
var organizeBackupWait = 30 * time.Second

func (s *Server) organizeCreateBackup(ctx context.Context, _ *mcp.CallToolRequest, _ organizeCreateBackupInput) (*mcp.CallToolResult, any, error) {
	before, err := s.ha.BackupInfo(ctx)
	if err != nil {
		return fail(err)
	}
	if before.State != "" && before.State != "idle" {
		return fail(fmt.Errorf("a backup operation is already running (%s); wait for it and check ha_backup_info", before.State))
	}
	known := map[string]bool{}
	for _, b := range before.Backups {
		known[b.BackupID] = true
	}
	job, err := s.ha.GenerateBackup(ctx)
	if err != nil {
		return fail(fmt.Errorf("%w. Backups made here use the automatic backup settings; if those have not been set up, "+
			"the person has to open Settings > System > Backups in Home Assistant and set up automatic backups "+
			"(storage location and encryption key) first", err))
	}
	deadline := time.Now().Add(organizeBackupWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(time.Second):
		}
		info, err := s.ha.BackupInfo(ctx)
		if err != nil {
			return fail(err)
		}
		if info.State != "" && info.State != "idle" {
			continue
		}
		for _, b := range info.Backups {
			if !known[b.BackupID] {
				return text("Backup finished: " + organizeBackupLine(b)), nil, nil
			}
		}
		return text("The backup job " + job + " ended without a new backup appearing. " + organizeRenderBackupInfo(info, 3, false)), nil, nil
	}
	return text(fmt.Sprintf("Backup %s started and is still running after %s; check ha_backup_info for the result.", job, organizeBackupWait)), nil, nil
}

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// Configs go in and come out as YAML because that is what HA's docs, forum
// posts and the user's own files use, so the model can apply what it knows
// without translating. JSON is accepted too, since YAML parses it.

// addAutomationTools registers the tools for automations, scripts, scenes, blueprints, traces and config validation. Write tools are skipped when s.readOnly is set.
func (s *Server) addAutomationTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_get_automation",
		Annotations: readOnlyTool(),
		Description: "List automations, scripts or scenes, or read one's full config as YAML. Without id or " +
			"entity_id it lists the kind with each item's entity_id, config id, name, state and when it last ran; " +
			"use query to narrow a long list. With id (the config id: an automation's or scene's id, a script's " +
			"key) or entity_id it returns the stored config, ready to edit and pass back to ha_manage_automation. " +
			"Only items created in the UI (stored in automations.yaml, scripts.yaml or scenes.yaml with an id) " +
			"have a readable config; the answer says so when an item is defined elsewhere in YAML. To see why an " +
			"automation did or did not do something, use ha_traces instead.",
	}, s.automationGet)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_traces",
		Annotations: readOnlyTool(),
		Description: "Debug automation and script runs. HA keeps the last few runs of each (5 by default). " +
			"Without run_id it lists stored runs, newest first, with trigger, duration, outcome and error; " +
			"without id or entity_id it lists recent runs of every automation (or script with kind:'script'), " +
			"which answers 'what failed lately'. With run_id it returns that run step by step: what triggered " +
			"it, each condition's result, each action's call and result, variables set, and where and why it " +
			"stopped. Add step (a path such as 'action/2' from that output) to get everything recorded for one " +
			"step, including full variable values.",
	}, s.automationTraces)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_validate_config",
		Annotations: readOnlyTool(),
		Description: "Check triggers, conditions and actions against HA's schemas without saving anything. " +
			"Pass any of triggers, conditions and actions as YAML, JSON or a list, or pass a whole automation " +
			"or script config as config and its sections are checked. Each section is reported valid or with " +
			"HA's error and the path to the bad key. ha_manage_automation runs the same check before saving, " +
			"so this is for drafts and single snippets.",
	}, s.automationValidate)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_device_automations",
		Annotations: readOnlyTool(),
		Description: "The device triggers, conditions and actions a device offers (for example a remote's " +
			"button presses, or a light's 'turned on'), each ready to paste into an automation's triggers, " +
			"conditions or actions. The entity_id inside them is HA's internal registry id, not the entity id; " +
			"keep it as given. Set capabilities to also list the extra fields each one accepts (such as 'for' " +
			"or brightness_pct). Most automations are easier to write with state triggers and actions; use " +
			"these for events that have no entity state, like button presses.",
	}, s.automationDeviceAutomations)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_find_related",
		Annotations: readOnlyTool(),
		Description: "Find everything connected to an item: the automations, scripts and scenes that use an " +
			"entity or device, the entities and devices an automation touches, what is in an area, which " +
			"automations were made from a blueprint, and so on. Use it before renaming or removing something, " +
			"or to find the automation that controls a device.",
	}, s.automationFindRelated)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_list_blueprints",
		Annotations: readOnlyTool(),
		Description: "List installed automation and script blueprints with their inputs. To make an automation " +
			"from one, save a config with use_blueprint (path and input values) through ha_manage_automation.",
	}, s.automationListBlueprints)

	if s.readOnly {
		return
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_manage_automation",
		Annotations: writeTool(true),
		Description: "Create, change, delete or run an automation, script or scene. action:'save' stores a full " +
			"config (YAML or JSON) and replaces whatever was there, so read the current one with " +
			"ha_get_automation first and send it back whole; without an id a new item is created. The config is " +
			"checked before saving and nothing is stored if it is invalid. Saving an existing item returns a " +
			"diff against the previous config, which is what to use to undo the change. To create an " +
			"automation from a blueprint, save a config like {alias, use_blueprint: {path, input}}. " +
			"action:'delete' needs confirm:true. The other actions act on the running item: 'enable' and " +
			"'disable' an automation, 'trigger' an automation (runs its actions now, skipping conditions unless " +
			"skip_condition is false), 'run' a script with optional variables, 'activate' a scene. Only items " +
			"created in the UI (with a config id) can be saved or deleted; enable, disable, trigger, run and " +
			"activate work on any.",
	}, s.automationManage)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ha_manage_blueprint",
		Annotations: writeTool(true),
		Description: "Install, write or delete a blueprint. action:'import' downloads one from a public https " +
			"URL (GitHub, a gist, the HA community forum or a direct YAML link) and installs it. action:'save' " +
			"writes blueprint YAML you provide to a path under the blueprints folder. action:'delete' removes " +
			"one and needs confirm:true; HA refuses while automations or scripts still use it. Using a blueprint " +
			"is not done here: save an automation or script with use_blueprint through ha_manage_automation.",
	}, s.automationManageBlueprint)
}

var automationKinds = []homeassistant.ConfigKind{homeassistant.KindAutomation, homeassistant.KindScript, homeassistant.KindScene}

func automationParseKind(kind string) (homeassistant.ConfigKind, error) {
	k := homeassistant.ConfigKind(strings.ToLower(strings.TrimSpace(kind)))
	if k == "" || slices.Contains(automationKinds, k) {
		return k, nil
	}
	return "", fmt.Errorf("kind must be automation, script or scene, got %q", kind)
}

// --- resolving items ---

// automationItem is one automation, script or scene. configID is empty when
// the item was not created in the UI; state is nil when no entity is loaded.
type automationItem struct {
	kind     homeassistant.ConfigKind
	configID string
	entityID string
	state    *homeassistant.State
}

func (it *automationItem) label() string {
	switch {
	case it.entityID != "" && it.configID != "":
		return fmt.Sprintf("%s (id %s)", it.entityID, oneLine(it.configID))
	case it.entityID != "":
		return it.entityID
	default:
		return fmt.Sprintf("%s id %s", it.kind, oneLine(it.configID))
	}
}

func (it *automationItem) requireConfigID() error {
	if it.configID != "" {
		return nil
	}
	return fmt.Errorf("%s has no config id, so it was not created in the UI (it is defined in YAML without an id, "+
		"or by an integration); its config cannot be read or changed through the API, only in the YAML file itself", it.entityID)
}

func automationNotUIManaged(it *automationItem) error {
	return fmt.Errorf("%s is not stored in %ss.yaml, so its config cannot be read or changed through the API; "+
		"it is defined elsewhere in YAML (configuration.yaml or a package) and has to be edited there", it.label(), it.kind)
}

// automationResolve finds an item by config id or entity id. An id that looks
// like an entity id is taken as one, because models pass whichever they saw.
func (s *Server) automationResolve(ctx context.Context, kind homeassistant.ConfigKind, id, entityID string) (*automationItem, error) {
	id, entityID = strings.TrimSpace(id), strings.TrimSpace(entityID)
	if entityID == "" && id != "" {
		for _, k := range automationKinds {
			if strings.HasPrefix(id, string(k)+".") {
				entityID, id = id, ""
				break
			}
		}
	}
	if entityID != "" {
		domain, _, _ := strings.Cut(entityID, ".")
		k := homeassistant.ConfigKind(domain)
		if !slices.Contains(automationKinds, k) {
			return nil, fmt.Errorf("%s is not an automation, script or scene", entityID)
		}
		if kind != "" && kind != k {
			return nil, fmt.Errorf("%s is a %s, not a %s", entityID, k, kind)
		}
		st, err := s.ha.GetState(ctx, entityID)
		if homeassistant.IsNotFound(err) {
			return nil, fmt.Errorf("%s does not exist; list them with ha_get_automation", entityID)
		}
		if err != nil {
			return nil, err
		}
		it := &automationItem{kind: k, entityID: entityID, state: st}
		if k == homeassistant.KindScript {
			e, err := s.ha.GetEntityRegistryEntry(ctx, entityID)
			if err != nil && !homeassistant.IsNotFound(err) {
				return nil, err
			}
			if e != nil && e.Platform == "script" {
				it.configID = e.UniqueID
			}
		} else if v, ok := st.Attributes["id"].(string); ok {
			it.configID = v
		}
		return it, nil
	}
	if id == "" {
		return nil, fmt.Errorf("pass id or entity_id")
	}

	kinds := automationKinds
	if kind != "" {
		kinds = []homeassistant.ConfigKind{kind}
	}
	var states []homeassistant.State
	var err error
	for _, k := range kinds {
		if k == homeassistant.KindScript {
			entries, err := s.ha.ListEntityRegistry(ctx)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if e.Platform == "script" && e.UniqueID == id {
					st, err := s.ha.GetState(ctx, e.EntityID)
					if err != nil && !homeassistant.IsNotFound(err) {
						return nil, err
					}
					return &automationItem{kind: k, configID: id, entityID: e.EntityID, state: st}, nil
				}
			}
			continue
		}
		if states == nil {
			if states, err = s.ha.ListStates(ctx); err != nil {
				return nil, err
			}
		}
		for i := range states {
			st := &states[i]
			if strings.HasPrefix(st.EntityID, string(k)+".") && st.Attributes["id"] == id {
				return &automationItem{kind: k, configID: id, entityID: st.EntityID, state: st}, nil
			}
		}
	}
	if kind == "" {
		return nil, fmt.Errorf("no automation, script or scene has the id %q; list them with ha_get_automation, or pass kind if it exists only as config", id)
	}
	return &automationItem{kind: kind, configID: id}, nil
}

// --- ha_get_automation ---

type automationGetInput struct {
	Kind     string `json:"kind,omitempty" jsonschema:"automation, script or scene; defaults to automation when listing"`
	ID       string `json:"id,omitempty" jsonschema:"config id to read: an automation's or scene's id, or a script's key"`
	EntityID string `json:"entity_id,omitempty" jsonschema:"entity id to read instead of id, such as automation.porch_light"`
	Query    string `json:"query,omitempty" jsonschema:"when listing, only items whose name, entity id or config id contains this"`
}

func (s *Server) automationGet(ctx context.Context, _ *mcp.CallToolRequest, in automationGetInput) (*mcp.CallToolResult, any, error) {
	kind, err := automationParseKind(in.Kind)
	if err != nil {
		return fail(err)
	}
	if in.ID == "" && in.EntityID == "" {
		if kind == "" {
			kind = homeassistant.KindAutomation
		}
		return s.automationList(ctx, kind, in.Query)
	}
	it, err := s.automationResolve(ctx, kind, in.ID, in.EntityID)
	if err != nil {
		return fail(err)
	}
	if err := it.requireConfigID(); err != nil {
		return fail(err)
	}
	cfg, err := s.ha.GetConfigItemRaw(ctx, it.kind, it.configID)
	if homeassistant.IsNotFound(err) {
		if it.entityID == "" {
			return fail(fmt.Errorf("no %s has the id %q", it.kind, it.configID))
		}
		return fail(automationNotUIManaged(it))
	}
	if err != nil {
		return fail(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s", it.label())
	if it.state != nil {
		fmt.Fprintf(&b, ": %s", s.automationStatus(it.kind, it.state))
	} else {
		b.WriteString(": no entity is loaded for it")
	}
	b.WriteString("\n")
	b.WriteString(automationRawYAML(cfg))
	return text(b.String()), nil, nil
}

func (s *Server) automationList(ctx context.Context, kind homeassistant.ConfigKind, query string) (*mcp.CallToolResult, any, error) {
	items, err := s.ha.ListConfigItems(ctx, kind)
	if err != nil {
		return fail(err)
	}
	q := strings.ToLower(strings.TrimSpace(query))
	sort.Slice(items, func(i, j int) bool { return items[i].EntityID < items[j].EntityID })
	var lines []string
	for _, it := range items {
		if q != "" && !strings.Contains(strings.ToLower(it.Name+" "+it.EntityID+" "+it.ConfigID), q) {
			continue
		}
		id := "[no config id: not editable here]"
		if it.ConfigID != "" {
			id = "[id " + it.ConfigID + "]"
		}
		st := homeassistant.State{EntityID: it.EntityID, State: it.State, Attributes: it.Attrs}
		lines = append(lines, fmt.Sprintf("%s %s %q %s", it.EntityID, id, it.Name, s.automationStatus(kind, &st)))
	}
	if len(lines) == 0 {
		if q != "" {
			return text(fmt.Sprintf("No %s matches %q.", kind, query)), nil, nil
		}
		return text(fmt.Sprintf("There are no %ss.", kind)), nil, nil
	}
	head := fmt.Sprintf("%d %ss:", len(lines), kind)
	if len(lines) == 1 {
		head = fmt.Sprintf("1 %s:", kind)
	}
	return text(head + "\n" + strings.Join(lines, "\n")), nil, nil
}

func (s *Server) automationStatus(kind homeassistant.ConfigKind, st *homeassistant.State) string {
	var parts []string
	switch kind {
	case homeassistant.KindScene:
		n := 0
		if ents, ok := st.Attributes["entity_id"].([]any); ok {
			n = len(ents)
		}
		if n == 1 {
			parts = append(parts, "1 entity")
		} else {
			parts = append(parts, fmt.Sprintf("%d entities", n))
		}
		if t, err := time.Parse(time.RFC3339Nano, st.State); err == nil {
			parts = append(parts, "last activated "+s.automationTime(t))
		} else {
			parts = append(parts, "never activated")
		}
		return strings.Join(parts, ", ")
	case homeassistant.KindScript:
		if st.State == "on" {
			parts = append(parts, "running")
		} else if st.State != "off" {
			parts = append(parts, truncate(st.State, 80))
		}
	default:
		parts = append(parts, truncate(st.State, 80))
		if n, ok := st.Attributes["current"].(float64); ok && n > 0 {
			parts = append(parts, fmt.Sprintf("running (%d)", int(n)))
		}
	}
	if v, ok := st.Attributes["last_triggered"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			parts = append(parts, "last ran "+s.automationTime(t))
		}
	} else {
		parts = append(parts, "never ran")
	}
	return strings.Join(parts, ", ")
}

func (s *Server) automationTime(t time.Time) string {
	return t.In(s.now().Location()).Format("2006-01-02 15:04:05 MST")
}

// --- ha_traces ---

type automationTracesInput struct {
	Kind     string `json:"kind,omitempty" jsonschema:"automation or script; defaults to automation"`
	ID       string `json:"id,omitempty" jsonschema:"config id of the automation, or the script's key"`
	EntityID string `json:"entity_id,omitempty" jsonschema:"entity id instead of id, such as automation.porch_light"`
	RunID    string `json:"run_id,omitempty" jsonschema:"one run to show step by step, from the list"`
	Step     string `json:"step,omitempty" jsonschema:"with run_id: a step path such as 'action/2' to show everything recorded for it"`
}

const (
	automationMaxTraceList  = 40
	automationMaxStepRuns   = 3
	automationMaxTraceSteps = 150
	automationMaxStepDetail = 8000
)

func (s *Server) automationTraces(ctx context.Context, _ *mcp.CallToolRequest, in automationTracesInput) (*mcp.CallToolResult, any, error) {
	kind, err := automationParseKind(in.Kind)
	if err != nil {
		return fail(err)
	}
	if kind == homeassistant.KindScene {
		return fail(fmt.Errorf("scenes do not record traces; only automations and scripts do"))
	}
	if in.ID == "" && in.EntityID == "" {
		if in.RunID != "" {
			return fail(fmt.Errorf("run_id needs the id or entity_id of the automation or script it belongs to"))
		}
		if kind == "" {
			kind = homeassistant.KindAutomation
		}
		return s.automationTraceList(ctx, kind, nil)
	}
	if kind == "" && in.EntityID == "" && !strings.Contains(in.ID, ".") {
		kind = homeassistant.KindAutomation
	}
	it, err := s.automationResolve(ctx, kind, in.ID, in.EntityID)
	if err != nil {
		return fail(err)
	}
	if it.kind == homeassistant.KindScene {
		return fail(fmt.Errorf("scenes do not record traces; only automations and scripts do"))
	}
	if err := it.requireConfigID(); err != nil {
		return fail(fmt.Errorf("%s has no config id, and HA only keeps traces for automations and scripts that have one", it.entityID))
	}
	if in.RunID == "" {
		return s.automationTraceList(ctx, it.kind, it)
	}
	raw, err := s.ha.GetTrace(ctx, homeassistant.TraceDomain(it.kind), it.configID, strings.TrimSpace(in.RunID))
	if homeassistant.IsNotFound(err) {
		return fail(fmt.Errorf("no stored run %s for %s; HA keeps only the last few, list them without run_id", in.RunID, it.label()))
	}
	if err != nil {
		return fail(err)
	}
	var tr automationTrace
	if err := json.Unmarshal([]byte(sanitizeJSON(raw)), &tr); err != nil {
		return fail(fmt.Errorf("could not read the trace: %w", err))
	}
	if in.Step != "" {
		return automationTraceStep(&tr, strings.Trim(in.Step, "/ "))
	}
	return text(s.automationRenderTrace(it, &tr)), nil, nil
}

func (s *Server) automationTraceList(ctx context.Context, kind homeassistant.ConfigKind, it *automationItem) (*mcp.CallToolResult, any, error) {
	itemID := ""
	if it != nil {
		itemID = it.configID
	}
	traces, err := s.ha.ListTraces(ctx, homeassistant.TraceDomain(kind), itemID)
	if err != nil {
		return fail(err)
	}
	sort.Slice(traces, func(i, j int) bool { return traces[i].Timestamp.Start.After(traces[j].Timestamp.Start) })

	var names map[string]string
	if it == nil && len(traces) > 0 {
		items, err := s.ha.ListConfigItems(ctx, kind)
		if err != nil {
			return fail(err)
		}
		names = map[string]string{}
		for _, ci := range items {
			if ci.ConfigID != "" {
				names[ci.ConfigID] = ci.EntityID
			}
		}
	}

	if len(traces) == 0 {
		if it != nil {
			return text(fmt.Sprintf("No stored runs for %s. It has not run since HA started, or trace storage is off (stored_traces: 0).", it.label())), nil, nil
		}
		return text(fmt.Sprintf("No stored %s runs.", kind)), nil, nil
	}
	var b strings.Builder
	if it != nil {
		fmt.Fprintf(&b, "Runs of %s, newest first:\n", it.label())
	} else {
		fmt.Fprintf(&b, "Recent %s runs, newest first:\n", kind)
	}
	for i, t := range traces {
		if i == automationMaxTraceList {
			fmt.Fprintf(&b, "… %d older runs not shown; pass an id to see one item's runs\n", len(traces)-i)
			break
		}
		fmt.Fprintf(&b, "%s %s", t.RunID, s.automationTime(t.Timestamp.Start))
		if it == nil {
			who := names[t.ItemID]
			if who == "" {
				who = string(kind) + " id " + t.ItemID
			} else {
				who += " [id " + t.ItemID + "]"
			}
			fmt.Fprintf(&b, " %s", who)
		}
		fmt.Fprintf(&b, " %s", automationOutcome(t.State, t.ScriptExecution, t.Timestamp.Start, t.Timestamp.Finish))
		if t.Error != nil && *t.Error != "" {
			fmt.Fprintf(&b, " at %s: %s", automationText(t.LastStep), automationText(t.Error))
		} else if t.LastStep != nil && automationStoppedEarly(t.ScriptExecution) {
			fmt.Fprintf(&b, " at %s", automationText(t.LastStep))
		}
		if t.Trigger != nil && *t.Trigger != "" {
			fmt.Fprintf(&b, "; trigger: %s", automationText(t.Trigger))
		}
		b.WriteString("\n")
	}
	return text(strings.TrimRight(b.String(), "\n")), nil, nil
}

// automationText renders a text field of a trace summary, which can quote
// variable values and error messages from anywhere in HA.
func automationText(s *string) string {
	if s == nil {
		return ""
	}
	return oneLine(redactSecrets(*s))
}

func automationStoppedEarly(exec *string) bool {
	if exec == nil {
		return false
	}
	switch *exec {
	case "finished", "":
		return false
	}
	return true
}

// automationOutcome turns HA's state and script_execution codes into words;
// the codes are terse enough that a model misreads failed_single as a failure.
func automationOutcome(state string, exec *string, start time.Time, finish *time.Time) string {
	if state == "running" || exec == nil {
		return "still running"
	}
	var what string
	switch *exec {
	case "finished":
		what = "finished"
	case "failed_conditions":
		what = "stopped: conditions not met"
	case "failed_single":
		what = "skipped: already running (mode: single)"
	case "failed_max_runs":
		what = "skipped: max parallel runs reached"
	case "cancelled":
		what = "cancelled"
	case "aborted":
		what = "aborted (a stop action ended it)"
	case "error":
		what = "failed with an error"
	case "timeout":
		what = "timed out"
	default:
		what = *exec
	}
	if finish != nil {
		what += " in " + automationDuration(finish.Sub(start))
	}
	return what
}

func automationDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

type automationTraceEntry struct {
	Path             string          `json:"path"`
	Timestamp        time.Time       `json:"timestamp"`
	ChangedVariables map[string]any  `json:"changed_variables"`
	Result           json.RawMessage `json:"result"`
	Error            string          `json:"error"`
}

type automationTrace struct {
	homeassistant.TraceSummary
	Trace           map[string][]automationTraceEntry `json:"trace"`
	Config          map[string]any                    `json:"config"`
	BlueprintInputs map[string]any                    `json:"blueprint_inputs"`
}

func (s *Server) automationRenderTrace(it *automationItem, tr *automationTrace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s of %s\n", tr.RunID, it.label())
	fmt.Fprintf(&b, "started %s, %s", s.automationTime(tr.Timestamp.Start),
		automationOutcome(tr.State, tr.ScriptExecution, tr.Timestamp.Start, tr.Timestamp.Finish))
	if tr.LastStep != nil {
		fmt.Fprintf(&b, ", last step %s", oneLine(*tr.LastStep))
	}
	b.WriteString("\n")
	if tr.Error != nil && *tr.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", oneLine(*tr.Error))
	}
	if tr.Trigger != nil && *tr.Trigger != "" {
		fmt.Fprintf(&b, "trigger: %s\n", oneLine(*tr.Trigger))
	}
	if bp, ok := tr.BlueprintInputs["use_blueprint"].(map[string]any); ok {
		fmt.Fprintf(&b, "blueprint: %s, inputs %s\n", automationCompact(bp["path"], 200), automationCompact(bp["input"], 400))
	}

	type step struct {
		path    string
		entries []automationTraceEntry
		first   time.Time
	}
	steps := make([]step, 0, len(tr.Trace))
	for p, es := range tr.Trace {
		st := step{path: p, entries: es}
		if len(es) > 0 {
			st.first = es[0].Timestamp
		}
		steps = append(steps, st)
	}
	// The trace object is keyed by path, and JSON object order does not
	// survive decoding; the first timestamp of each path restores execution order.
	sort.SliceStable(steps, func(i, j int) bool {
		if !steps[i].first.Equal(steps[j].first) {
			return steps[i].first.Before(steps[j].first)
		}
		return steps[i].path < steps[j].path
	})

	b.WriteString("steps:\n")
	for i, st := range steps {
		if i == automationMaxTraceSteps {
			fmt.Fprintf(&b, "… %d more steps not shown; ask for one with step\n", len(steps)-i)
			break
		}
		desc := automationDescribeStep(automationNodeAt(tr.Config, st.path))
		for j, e := range st.entries {
			if j == automationMaxStepRuns {
				fmt.Fprintf(&b, "  … ran %d more times\n", len(st.entries)-j)
				break
			}
			b.WriteString("- ")
			b.WriteString(oneLine(st.path))
			if len(st.entries) > 1 {
				fmt.Fprintf(&b, " #%d", j+1)
			}
			line := automationRenderStep(e)
			if desc != "" && line != desc && !strings.HasPrefix(line, desc+" ") && !strings.HasPrefix(line, desc+";") {
				fmt.Fprintf(&b, " (%s)", desc)
			}
			if line != "" {
				b.WriteString(": ")
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
	}
	if len(steps) == 0 {
		b.WriteString("(none recorded)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// automationRenderStep summarizes one trace entry. The variables HA records on
// every run (this, trigger, context) are left out: they are the bulk of a raw
// trace and the trigger line already says what fired.
func automationRenderStep(e automationTraceEntry) string {
	var parts []string
	if trig, ok := e.ChangedVariables["trigger"].(map[string]any); ok {
		parts = append(parts, automationDescribeTrigger(trig))
	}
	var res map[string]any
	if len(e.Result) > 0 && json.Unmarshal(e.Result, &res) == nil && res != nil {
		if params, ok := res["params"].(map[string]any); ok {
			call := oneLine(fmt.Sprintf("%v.%v", params["domain"], params["service"]))
			if t, ok := params["target"].(map[string]any); ok && len(t) > 0 {
				call += " target " + automationCompact(t, 300)
			}
			if d, ok := params["service_data"].(map[string]any); ok && len(d) > 0 {
				call += " data " + automationCompact(d, 300)
			}
			parts = append(parts, call)
			delete(res, "params")
			delete(res, "running_script")
		}
		if r, ok := res["result"]; ok {
			parts = append(parts, "result "+automationCompact(r, 100))
			delete(res, "result")
		}
		keys := make([]string, 0, len(res))
		for k, v := range res {
			if l, ok := v.([]any); ok && len(l) == 0 || v == nil {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, oneLine(k)+"="+automationCompact(res[k], 300))
		}
	}
	var vars []string
	for k, v := range e.ChangedVariables {
		if k == "this" || k == "trigger" || k == "context" {
			continue
		}
		vars = append(vars, oneLine(k)+"="+automationCompact(v, 200))
	}
	if len(vars) > 0 {
		sort.Strings(vars)
		parts = append(parts, "set "+strings.Join(vars, ", "))
	}
	if e.Error != "" {
		parts = append(parts, "ERROR: "+oneLine(e.Error))
	}
	return strings.Join(parts, "; ")
}

func automationDescribeTrigger(t map[string]any) string {
	if t["platform"] == nil && t["trigger"] == nil && t["description"] == nil {
		return "started by hand (automation.trigger or the UI's Run action)"
	}
	desc, _ := t["description"].(string)
	if desc == "" {
		desc = fmt.Sprintf("%v trigger", t["platform"])
	}
	desc = truncate(desc, 300)
	if id, ok := t["id"].(string); ok && id != "" && id != fmt.Sprint(t["idx"]) {
		desc += fmt.Sprintf(" (trigger id %q)", id)
	}
	from, _ := t["from_state"].(map[string]any)
	to, _ := t["to_state"].(map[string]any)
	if from != nil || to != nil {
		desc += fmt.Sprintf(": %v → %v", automationStateOf(from), automationStateOf(to))
	}
	if ev, ok := t["event"].(map[string]any); ok {
		if d, ok := ev["data"].(map[string]any); ok && len(d) > 0 {
			desc += " data " + automationCompact(d, 300)
		}
	}
	return desc
}

func automationStateOf(st map[string]any) string {
	if st == nil {
		return "(none)"
	}
	return truncate(fmt.Sprint(st["state"]), 100)
}

func automationTraceStep(tr *automationTrace, path string) (*mcp.CallToolResult, any, error) {
	es, ok := tr.Trace[path]
	if !ok {
		paths := make([]string, 0, len(tr.Trace))
		for p := range tr.Trace {
			paths = append(paths, oneLine(p))
		}
		sort.Strings(paths)
		return fail(fmt.Errorf("run %s has no step %q; its steps are %s", oneLine(tr.RunID), path, strings.Join(paths, ", ")))
	}
	b, _ := json.MarshalIndent(es, "", " ")
	out := string(b)
	if len(out) > automationMaxStepDetail {
		out = out[:automationMaxStepDetail] + "\n… cut at 8000 characters"
	}
	return text(out), nil, nil
}

// automationNodeAt finds the config element a trace path refers to, so each
// step can be named after what it is rather than only its position. HA's paths
// mostly mirror the config keys, with plural/singular drift and a few
// synthetic segments, so a lookup that does not fit returns nil.
func automationNodeAt(cfg map[string]any, path string) any {
	var cur any = cfg
	for _, seg := range strings.Split(path, "/") {
		switch c := cur.(type) {
		case map[string]any:
			if v, ok := automationLookup(c, seg); ok {
				cur = v
			} else if seg == "sequence" {
				cur = []any{c}
			} else if seg == "0" {
				cur = c
			} else {
				return nil
			}
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil {
				continue
			}
			if i < 0 || i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

func automationLookup(m map[string]any, key string) (any, bool) {
	for _, k := range []string{key, key + "s", strings.TrimSuffix(key, "s")} {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return nil, false
}

func automationDescribeStep(n any) string {
	m, ok := n.(map[string]any)
	if !ok {
		return ""
	}
	if a, ok := m["alias"].(string); ok && a != "" {
		return strconv.Quote(a)
	}
	for _, k := range []string{"action", "service"} {
		if v, ok := m[k].(string); ok {
			return truncate(v, 100)
		}
	}
	for _, k := range []string{"condition", "trigger", "platform"} {
		if v, ok := m[k].(string); ok {
			if e, ok := m["entity_id"]; ok {
				return truncate(fmt.Sprintf("%s %v", v, e), 200)
			}
			return truncate(v, 100)
		}
	}
	if _, ok := m["conditions"]; ok {
		if _, ok := m["sequence"]; ok {
			return "option"
		}
	}
	for _, k := range []string{"choose", "if", "repeat", "parallel", "sequence", "delay", "wait_template",
		"wait_for_trigger", "variables", "event", "stop", "scene", "device_id", "set_conversation_response"} {
		if _, ok := m[k]; ok {
			return k
		}
	}
	return ""
}

func automationCompact(v any, n int) string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			s = fmt.Sprint(v)
		} else {
			s = string(b)
		}
	}
	return truncate(s, n)
}

// --- ha_validate_config ---

type automationValidateInput struct {
	Triggers   any `json:"triggers,omitempty" jsonschema:"triggers to check, as YAML or JSON text or a list"`
	Conditions any `json:"conditions,omitempty" jsonschema:"conditions to check, as YAML or JSON text or a list"`
	Actions    any `json:"actions,omitempty" jsonschema:"actions to check, as YAML or JSON text or a list"`
	Config     any `json:"config,omitempty" jsonschema:"a whole automation or script config, instead of the separate sections; a script's sequence is checked as actions"`
}

func (s *Server) automationValidate(ctx context.Context, _ *mcp.CallToolRequest, in automationValidateInput) (*mcp.CallToolResult, any, error) {
	var req homeassistant.ValidateConfigRequest
	if in.Config != nil {
		cfg, err := automationParseConfig(in.Config)
		if err != nil {
			return fail(err)
		}
		if _, ok := cfg["use_blueprint"]; ok {
			return fail(fmt.Errorf("a blueprint automation has no triggers or actions of its own to check; HA checks its inputs when it is saved"))
		}
		req = automationSections(cfg)
	}
	for _, sec := range []struct {
		name string
		in   any
		dst  *any
	}{{"triggers", in.Triggers, &req.Triggers}, {"conditions", in.Conditions, &req.Conditions}, {"actions", in.Actions, &req.Actions}} {
		if sec.in == nil {
			continue
		}
		v, err := automationParseValue(sec.in)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", sec.name, err))
		}
		*sec.dst = v
	}
	if req.Triggers == nil && req.Conditions == nil && req.Actions == nil {
		return fail(fmt.Errorf("nothing to check: pass triggers, conditions, actions or config"))
	}
	res, err := s.ha.ValidateConfig(ctx, req)
	if err != nil {
		return fail(err)
	}
	lines, ok := automationValidationLines(res)
	head := "Valid."
	if !ok {
		head = "Invalid."
	}
	return text(head + "\n" + strings.Join(lines, "\n")), nil, nil
}

func automationSections(cfg map[string]any) homeassistant.ValidateConfigRequest {
	var req homeassistant.ValidateConfigRequest
	pick := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := cfg[k]; ok && v != nil {
				return v
			}
		}
		return nil
	}
	req.Triggers = pick("triggers", "trigger")
	req.Conditions = pick("conditions", "condition")
	req.Actions = pick("actions", "action", "sequence")
	return req
}

func automationValidationLines(res *homeassistant.ValidateConfigResponse) ([]string, bool) {
	ok := true
	var lines []string
	for _, sec := range []struct {
		name string
		r    *homeassistant.ValidationResult
	}{{"triggers", res.Triggers}, {"conditions", res.Conditions}, {"actions", res.Actions}} {
		if sec.r == nil {
			continue
		}
		if sec.r.Valid {
			lines = append(lines, sec.name+": valid")
			continue
		}
		ok = false
		msg := "invalid"
		if sec.r.Error != nil {
			msg += ": " + oneLine(*sec.r.Error)
		}
		lines = append(lines, sec.name+": "+msg)
	}
	return lines, ok
}

// --- ha_list_device_automations ---

type automationDeviceInput struct {
	DeviceID     string `json:"device_id" jsonschema:"the device's id"`
	Type         string `json:"type,omitempty" jsonschema:"trigger, condition or action; all three when left out"`
	Capabilities bool   `json:"capabilities,omitempty" jsonschema:"also list the extra fields each one accepts"`
}

const automationMaxCapabilityLookups = 60

func (s *Server) automationDeviceAutomations(ctx context.Context, _ *mcp.CallToolRequest, in automationDeviceInput) (*mcp.CallToolResult, any, error) {
	if in.DeviceID == "" {
		return fail(fmt.Errorf("device_id is required"))
	}
	types := []homeassistant.DeviceAutomationType{homeassistant.DeviceTrigger, homeassistant.DeviceCondition, homeassistant.DeviceAction}
	if in.Type != "" {
		t := homeassistant.DeviceAutomationType(strings.TrimSuffix(strings.ToLower(in.Type), "s"))
		if !slices.Contains(types, t) {
			return fail(fmt.Errorf("type must be trigger, condition or action, got %q", in.Type))
		}
		types = []homeassistant.DeviceAutomationType{t}
	}

	entities, err := s.ha.ListEntityRegistry(ctx)
	if err != nil {
		return fail(err)
	}
	entityIDs := map[string]string{}
	for _, e := range entities {
		entityIDs[e.ID] = e.EntityID
	}

	var b strings.Builder
	lookups := 0
	for _, t := range types {
		items, err := s.ha.ListDeviceAutomations(ctx, t, in.DeviceID)
		if err != nil {
			if homeassistant.IsNotFound(err) {
				return fail(fmt.Errorf("no device has the id %q", in.DeviceID))
			}
			return fail(err)
		}
		fmt.Fprintf(&b, "%ss:\n", t)
		if len(items) == 0 {
			b.WriteString("(none)\n")
		}
		for _, item := range items {
			delete(item, "metadata")
			fmt.Fprintf(&b, "- %s", automationFlowYAML(item))
			if eid, ok := entityIDs[fmt.Sprint(item["entity_id"])]; ok {
				fmt.Fprintf(&b, "  # %s", eid)
			}
			b.WriteString("\n")
			if !in.Capabilities {
				continue
			}
			lookups++
			if lookups > automationMaxCapabilityLookups {
				if lookups == automationMaxCapabilityLookups+1 {
					b.WriteString("  (extra fields not looked up past this point; ask with a narrower type)\n")
				}
				continue
			}
			raw, err := s.ha.DeviceAutomationCapabilities(ctx, t, item)
			if err != nil {
				fmt.Fprintf(&b, "  extra fields: could not look up (%v)\n", err)
				continue
			}
			if fields := automationExtraFields(raw); fields != "" {
				fmt.Fprintf(&b, "  extra fields: %s\n", fields)
			}
		}
	}
	return text(strings.TrimRight(b.String(), "\n")), nil, nil
}

// automationExtraFields renders HA's voluptuous-serialized field list.
func automationExtraFields(raw json.RawMessage) string {
	var caps struct {
		ExtraFields []map[string]any `json:"extra_fields"`
	}
	if json.Unmarshal(raw, &caps) != nil {
		return ""
	}
	var out []string
	for _, f := range caps.ExtraFields {
		desc := fmt.Sprint(f["type"])
		if desc == "<nil>" {
			if sel, ok := f["selector"].(map[string]any); ok {
				for k := range sel {
					desc = k
				}
			}
		}
		if lo, ok := f["valueMin"]; ok {
			desc += fmt.Sprintf(" %v", lo)
			if hi, ok := f["valueMax"]; ok {
				desc += fmt.Sprintf("-%v", hi)
			}
		}
		if opts, ok := f["options"].([]any); ok {
			var vals []string
			for _, o := range opts {
				if pair, ok := o.([]any); ok && len(pair) > 0 {
					vals = append(vals, fmt.Sprint(pair[0]))
				} else {
					vals = append(vals, fmt.Sprint(o))
				}
			}
			desc += " one of " + strings.Join(vals, "|")
		}
		if req, _ := f["required"].(bool); req {
			desc += ", required"
		} else {
			desc += ", optional"
		}
		out = append(out, fmt.Sprintf("%v (%s)", f["name"], desc))
	}
	return oneLine(strings.Join(out, "; "))
}

// --- ha_find_related ---

type automationRelatedInput struct {
	ItemType string `json:"item_type" jsonschema:"entity, device, area, floor, label, automation, script, scene, integration, config_entry, group, person, automation_blueprint or script_blueprint"`
	ItemID   string `json:"item_id" jsonschema:"the item's id: an entity id, device id, area id, integration domain, blueprint path; automations, scripts and scenes by entity id or config id"`
}

func (s *Server) automationFindRelated(ctx context.Context, _ *mcp.CallToolRequest, in automationRelatedInput) (*mcp.CallToolResult, any, error) {
	typ := homeassistant.SearchItemType(strings.ToLower(strings.TrimSpace(in.ItemType)))
	id := strings.TrimSpace(in.ItemID)
	if typ == "" || id == "" {
		return fail(fmt.Errorf("item_type and item_id are both required"))
	}
	switch typ {
	case homeassistant.SearchAutomation, homeassistant.SearchScript, homeassistant.SearchScene:
		if !strings.HasPrefix(id, string(typ)+".") {
			it, err := s.automationResolve(ctx, homeassistant.ConfigKind(typ), id, "")
			if err != nil {
				return fail(err)
			}
			if it.entityID == "" {
				return fail(fmt.Errorf("no %s has the id %q", typ, id))
			}
			id = it.entityID
		}
	}
	rel, err := s.ha.SearchRelated(ctx, typ, id)
	if err != nil {
		return fail(err)
	}
	if len(rel) == 0 {
		return text(fmt.Sprintf("Nothing is related to %s %s.", typ, id)), nil, nil
	}

	names := map[homeassistant.SearchItemType]map[string]string{}
	if len(rel[homeassistant.SearchDevice]) > 0 {
		if devs, err := s.ha.ListDevices(ctx); err == nil {
			m := map[string]string{}
			for _, d := range devs {
				m[d.ID] = d.DisplayName()
			}
			names[homeassistant.SearchDevice] = m
		}
	}
	if len(rel[homeassistant.SearchArea]) > 0 {
		if areas, err := s.ha.ListAreas(ctx); err == nil {
			m := map[string]string{}
			for _, a := range areas {
				m[a.AreaID] = a.Name
			}
			names[homeassistant.SearchArea] = m
		}
	}
	if len(rel[homeassistant.SearchConfigEntry]) > 0 {
		if entries, err := s.ha.ListConfigEntries(ctx, ""); err == nil {
			m := map[string]string{}
			for _, e := range entries {
				m[e.EntryID] = e.Title + " (" + e.Domain + ")"
			}
			names[homeassistant.SearchConfigEntry] = m
		}
	}

	types := make([]string, 0, len(rel))
	for t := range rel {
		types = append(types, string(t))
	}
	sort.Strings(types)
	var b strings.Builder
	fmt.Fprintf(&b, "Related to %s %s:\n", typ, id)
	for _, t := range types {
		ids := slices.Clone(rel[homeassistant.SearchItemType(t)])
		sort.Strings(ids)
		for i, v := range ids {
			if n := names[homeassistant.SearchItemType(t)][v]; n != "" {
				ids[i] = fmt.Sprintf("%s %q", v, n)
			}
		}
		fmt.Fprintf(&b, "%s: %s\n", t, strings.Join(ids, ", "))
	}
	return text(strings.TrimRight(b.String(), "\n")), nil, nil
}

// --- ha_list_blueprints ---

type automationBlueprintsInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"automation or script; both when left out"`
}

func (s *Server) automationListBlueprints(ctx context.Context, _ *mcp.CallToolRequest, in automationBlueprintsInput) (*mcp.CallToolResult, any, error) {
	domains := []string{"automation", "script"}
	if in.Domain != "" {
		domains = []string{strings.ToLower(strings.TrimSpace(in.Domain))}
	}
	var b strings.Builder
	for _, d := range domains {
		bps, err := s.ha.ListBlueprints(ctx, d)
		if err != nil {
			return fail(err)
		}
		paths := make([]string, 0, len(bps))
		for p := range bps {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		fmt.Fprintf(&b, "%s blueprints (%d):\n", d, len(paths))
		for _, p := range paths {
			bp := bps[p]
			if bp.Error != "" {
				fmt.Fprintf(&b, "- %s: failed to load: %s\n", oneLine(p), truncate(bp.Error, 500))
				continue
			}
			fmt.Fprintf(&b, "- %s %q", oneLine(p), fmt.Sprint(bp.Metadata["name"]))
			if desc, ok := bp.Metadata["description"].(string); ok && desc != "" {
				fmt.Fprintf(&b, ": %s", truncate(desc, 160))
			}
			b.WriteString("\n")
			if inputs := automationBlueprintInputs(bp.Metadata["input"]); len(inputs) > 0 {
				fmt.Fprintf(&b, "  inputs: %s\n", strings.Join(inputs, "; "))
			}
			if src, ok := bp.Metadata["source_url"].(string); ok && src != "" {
				fmt.Fprintf(&b, "  source: %s\n", oneLine(src))
			}
		}
	}
	return text(strings.TrimRight(b.String(), "\n")), nil, nil
}

// automationBlueprintInputs flattens a blueprint's inputs, including those
// grouped into collapsible sections, into "key (selector, default)" items.
func automationBlueprintInputs(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		in, _ := m[k].(map[string]any)
		if nested, ok := in["input"]; ok {
			out = append(out, automationBlueprintInputs(nested)...)
			continue
		}
		var desc []string
		if sel, ok := in["selector"].(map[string]any); ok {
			for st := range sel {
				desc = append(desc, oneLine(st))
			}
			sort.Strings(desc)
		}
		if def, ok := in["default"]; ok {
			if str, ok := def.(string); ok {
				desc = append(desc, "default "+strconv.Quote(truncate(str, 60)))
			} else {
				desc = append(desc, "default "+automationCompact(def, 60))
			}
		} else {
			desc = append(desc, "required")
		}
		name := oneLine(k)
		if n, ok := in["name"].(string); ok && n != "" && !strings.EqualFold(n, strings.ReplaceAll(k, "_", " ")) {
			name += fmt.Sprintf(" %q", n)
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, strings.Join(desc, ", ")))
	}
	return out
}

// --- ha_manage_automation ---

type automationManageInput struct {
	Kind          string         `json:"kind,omitempty" jsonschema:"automation, script or scene; taken from entity_id when given, otherwise defaults to automation"`
	Action        string         `json:"action" jsonschema:"save, delete, enable, disable, trigger (automation), run (script) or activate (scene)"`
	ID            string         `json:"id,omitempty" jsonschema:"config id (automation or scene id, script key); leave out to create a new item. A new script's key is made from its alias when left out"`
	EntityID      string         `json:"entity_id,omitempty" jsonschema:"entity id instead of id, such as automation.porch_light"`
	Config        any            `json:"config,omitempty" jsonschema:"for save: the full config as YAML or JSON text or an object; it replaces the stored one entirely"`
	Variables     map[string]any `json:"variables,omitempty" jsonschema:"for trigger and run: variables passed to the run"`
	SkipCondition *bool          `json:"skip_condition,omitempty" jsonschema:"for trigger: skip the automation's conditions; default true"`
	Confirm       bool           `json:"confirm,omitempty" jsonschema:"required for delete"`
}

func (s *Server) automationManage(ctx context.Context, req *mcp.CallToolRequest, in automationManageInput) (*mcp.CallToolResult, any, error) {
	kind, err := automationParseKind(in.Kind)
	if err != nil {
		return fail(err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	switch action {
	case "save":
		return s.automationSave(ctx, req, kind, in)
	case "delete":
		return s.automationDelete(ctx, kind, in)
	case "enable", "disable", "trigger", "run", "activate":
		return s.automationControl(ctx, kind, action, in)
	default:
		return fail(fmt.Errorf("action must be save, delete, enable, disable, trigger, run or activate, got %q", in.Action))
	}
}

var automationSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func automationSlug(s string) string {
	return strings.Trim(automationSlugRe.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func (s *Server) automationSave(ctx context.Context, req *mcp.CallToolRequest, kind homeassistant.ConfigKind, in automationManageInput) (*mcp.CallToolResult, any, error) {
	if in.Config == nil {
		return fail(fmt.Errorf("config is required to save"))
	}
	body, cfg, err := automationConfigJSON(req, in.Config)
	if err != nil {
		return fail(err)
	}

	var it *automationItem
	derivedKey := false
	if in.ID != "" || in.EntityID != "" {
		k := kind
		if k == "" && in.EntityID == "" && !strings.Contains(in.ID, ".") {
			k = homeassistant.KindAutomation
		}
		if it, err = s.automationResolve(ctx, k, in.ID, in.EntityID); err != nil {
			return fail(err)
		}
		if err := it.requireConfigID(); err != nil {
			return fail(err)
		}
	} else {
		if kind == "" {
			kind = homeassistant.KindAutomation
		}
		it = &automationItem{kind: kind}
		switch kind {
		case homeassistant.KindScript:
			alias, _ := cfg["alias"].(string)
			it.configID = automationSlug(alias)
			derivedKey = true
			if it.configID == "" {
				return fail(fmt.Errorf("a new script needs an id (its key, such as 'morning_routine') or an alias to make one from"))
			}
		default:
			if id, ok := cfg["id"].(string); ok && id != "" {
				it.configID = id
			} else {
				// The HA frontend uses the creation time in milliseconds as the id.
				it.configID = strconv.FormatInt(s.now().UnixMilli(), 10)
			}
		}
	}

	if _, bp := cfg["use_blueprint"]; !bp && it.kind != homeassistant.KindScene {
		req := automationSections(cfg)
		if req.Triggers != nil || req.Conditions != nil || req.Actions != nil {
			res, err := s.ha.ValidateConfig(ctx, req)
			if err != nil {
				return fail(err)
			}
			if lines, ok := automationValidationLines(res); !ok {
				return fail(fmt.Errorf("the config is invalid and was not saved:\n%s", strings.Join(lines, "\n")))
			}
		}
	}

	prev, err := s.ha.GetConfigItemRaw(ctx, it.kind, it.configID)
	if homeassistant.IsNotFound(err) {
		if it.entityID != "" {
			return fail(automationNotUIManaged(it))
		}
		prev = nil
	} else if err != nil {
		return fail(err)
	}
	if derivedKey {
		_, err := s.ha.GetState(ctx, "script."+it.configID)
		if prev != nil || err == nil {
			return fail(fmt.Errorf("a script with the key %q already exists; pass id:%q to replace it, or a different id for a new script", it.configID, it.configID))
		}
	}

	if err := s.ha.SaveConfigItemRaw(ctx, it.kind, it.configID, body); err != nil {
		return fail(fmt.Errorf("%w\nNothing was saved", err))
	}

	saved := s.automationAwaitEntity(ctx, it.kind, it.configID)
	var b strings.Builder
	if prev == nil {
		fmt.Fprintf(&b, "Created %s.", saved.label())
	} else {
		fmt.Fprintf(&b, "Updated %s.", saved.label())
	}
	if saved.state != nil {
		fmt.Fprintf(&b, " Now: %s.", s.automationStatus(saved.kind, saved.state))
	} else {
		b.WriteString(" HA stored it but has not loaded an entity for it; check the log for errors.")
	}
	if prev != nil {
		after, err := s.ha.GetConfigItemRaw(ctx, it.kind, it.configID)
		if err != nil {
			after = body
		}
		diff := automationDiff(automationRawYAML(prev), automationRawYAML(after))
		if diff == "" {
			b.WriteString("\nThe stored config did not change.")
		} else {
			b.WriteString("\nChanges (- before, + after):\n")
			b.WriteString(diff)
		}
	}
	return text(strings.TrimRight(b.String(), "\n")), nil, nil
}

// automationAwaitEntity looks up the entity of a just-saved item. HA answers
// the save before a new item's entity is set up, so a new one is polled for
// briefly; on timeout the item comes back without state.
func (s *Server) automationAwaitEntity(ctx context.Context, kind homeassistant.ConfigKind, id string) *automationItem {
	deadline := time.Now().Add(3 * time.Second)
	for {
		it, err := s.automationResolve(ctx, kind, id, "")
		if err == nil && it.state != nil {
			return it
		}
		if time.Now().After(deadline) {
			return &automationItem{kind: kind, configID: id}
		}
		select {
		case <-ctx.Done():
			return &automationItem{kind: kind, configID: id}
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (s *Server) automationDelete(ctx context.Context, kind homeassistant.ConfigKind, in automationManageInput) (*mcp.CallToolResult, any, error) {
	if in.ID == "" && in.EntityID == "" {
		return fail(fmt.Errorf("id or entity_id is required to delete"))
	}
	if kind == "" && in.EntityID == "" && !strings.Contains(in.ID, ".") {
		kind = homeassistant.KindAutomation
	}
	it, err := s.automationResolve(ctx, kind, in.ID, in.EntityID)
	if err != nil {
		return fail(err)
	}
	if err := it.requireConfigID(); err != nil {
		return fail(err)
	}
	cfg, err := s.ha.GetConfigItemRaw(ctx, it.kind, it.configID)
	if homeassistant.IsNotFound(err) {
		if it.entityID == "" {
			return fail(fmt.Errorf("no %s has the id %q", it.kind, it.configID))
		}
		return fail(automationNotUIManaged(it))
	}
	if err != nil {
		return fail(err)
	}
	if !in.Confirm {
		return fail(fmt.Errorf("deleting %s removes it from %ss.yaml and cannot be undone except by saving it again. "+
			"Check with the person first, then call again with confirm:true", it.label(), it.kind))
	}
	if err := s.ha.DeleteConfigItem(ctx, it.kind, it.configID); err != nil {
		return fail(err)
	}
	return text(fmt.Sprintf("Deleted %s. Its config was:\n%s", it.label(), automationRawYAML(cfg))), nil, nil
}

func (s *Server) automationControl(ctx context.Context, kind homeassistant.ConfigKind, action string, in automationManageInput) (*mcp.CallToolResult, any, error) {
	want := map[string]homeassistant.ConfigKind{
		"enable": homeassistant.KindAutomation, "disable": homeassistant.KindAutomation,
		"trigger": homeassistant.KindAutomation, "run": homeassistant.KindScript, "activate": homeassistant.KindScene,
	}[action]
	if kind != "" && kind != want {
		return fail(fmt.Errorf("%s only applies to a %s", action, want))
	}
	if in.ID == "" && in.EntityID == "" {
		return fail(fmt.Errorf("id or entity_id is required to %s", action))
	}
	it, err := s.automationResolve(ctx, want, in.ID, in.EntityID)
	if err != nil {
		return fail(err)
	}
	if it.entityID == "" {
		return fail(fmt.Errorf("no %s entity has the id %q", want, it.configID))
	}
	call := homeassistant.ServiceCall{Domain: string(want), Target: &homeassistant.Target{EntityID: []string{it.entityID}}}
	switch action {
	case "enable":
		call.Service = "turn_on"
	case "disable":
		call.Service = "turn_off"
	case "trigger":
		call.Service = "trigger"
		call.Data = map[string]any{}
		if in.SkipCondition != nil {
			call.Data["skip_condition"] = *in.SkipCondition
		}
		if len(in.Variables) > 0 {
			call.Data["variables"] = in.Variables
		}
	case "run":
		call.Service = "turn_on"
		if len(in.Variables) > 0 {
			call.Data = map[string]any{"variables": in.Variables}
		}
	case "activate":
		call.Service = "turn_on"
	}
	res, err := s.ha.CallService(ctx, call)
	if err != nil {
		return fail(err)
	}

	switch action {
	case "enable", "disable":
		st, err := s.ha.GetState(ctx, it.entityID)
		if err != nil {
			return fail(err)
		}
		return text(fmt.Sprintf("%s is now %s.", it.entityID, st.State)), nil, nil
	case "trigger":
		// automation.trigger returns once the run is over, so its trace is complete.
		msg := fmt.Sprintf("Triggered %s", it.entityID)
		if in.SkipCondition == nil || *in.SkipCondition {
			msg += " (conditions skipped)"
		}
		msg += "."
		if it.configID != "" {
			if traces, err := s.ha.ListTraces(ctx, homeassistant.TraceAutomation, it.configID); err == nil && len(traces) > 0 {
				sort.Slice(traces, func(i, j int) bool { return traces[i].Timestamp.Start.After(traces[j].Timestamp.Start) })
				t := traces[0]
				msg += fmt.Sprintf(" Run %s %s", t.RunID, automationOutcome(t.State, t.ScriptExecution, t.Timestamp.Start, t.Timestamp.Finish))
				if t.Error != nil && *t.Error != "" {
					msg += fmt.Sprintf(" at %s: %s", automationText(t.LastStep), automationText(t.Error))
				}
				msg += ". Details with ha_traces."
			}
		}
		return text(msg), nil, nil
	case "run":
		return text(fmt.Sprintf("Started %s. It runs in the background; see how it went with ha_traces.", it.entityID)), nil, nil
	default:
		return text(fmt.Sprintf("Activated %s; %d entity states changed.", it.entityID, len(res.ChangedStates))), nil, nil
	}
}

// --- ha_manage_blueprint ---

type automationManageBlueprintInput struct {
	Action    string `json:"action" jsonschema:"import, save or delete"`
	URL       string `json:"url,omitempty" jsonschema:"for import: public https URL of the blueprint"`
	Domain    string `json:"domain,omitempty" jsonschema:"automation or script; for save it is read from the blueprint when left out"`
	Path      string `json:"path,omitempty" jsonschema:"path under the domain's blueprints folder, such as 'someone/motion_light.yaml'; import picks one when left out"`
	YAML      string `json:"yaml,omitempty" jsonschema:"for save: the blueprint's full YAML"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"replace an existing blueprint file at the same path"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"required for delete"`
}

func (s *Server) automationManageBlueprint(ctx context.Context, _ *mcp.CallToolRequest, in automationManageBlueprintInput) (*mcp.CallToolResult, any, error) {
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "import":
		if in.URL == "" {
			return fail(fmt.Errorf("url is required to import"))
		}
		imp, err := s.ha.ImportBlueprint(ctx, in.URL)
		if err != nil {
			return fail(err)
		}
		if len(imp.ValidationErrors) > 0 {
			return fail(fmt.Errorf("the blueprint at %s is invalid and was not installed: %s", in.URL, strings.Join(imp.ValidationErrors, "; ")))
		}
		domain, _ := imp.Blueprint.Metadata["domain"].(string)
		path := in.Path
		if path == "" {
			path = imp.SuggestedFilename
		}
		path = automationBlueprintPath(path)
		if imp.Exists && !in.Overwrite {
			return fail(fmt.Errorf("a blueprint from this URL is already installed at %s/%s; pass overwrite:true to replace it", domain, path))
		}
		replaced, err := s.ha.SaveBlueprint(ctx, homeassistant.SaveBlueprintRequest{
			Domain: domain, Path: path, YAML: imp.RawData, SourceURL: in.URL, AllowOverride: in.Overwrite,
		})
		if err != nil {
			return fail(err)
		}
		verb := "Installed"
		if replaced {
			verb = "Replaced"
		}
		return text(automationBlueprintSaved(verb, domain, path, imp.Blueprint.Metadata)), nil, nil

	case "save":
		if in.YAML == "" || in.Path == "" {
			return fail(fmt.Errorf("yaml and path are required to save"))
		}
		var doc struct {
			Blueprint map[string]any `yaml:"blueprint"`
		}
		if err := yaml.Unmarshal([]byte(in.YAML), &doc); err != nil {
			return fail(fmt.Errorf("the yaml does not parse: %w", err))
		}
		if doc.Blueprint == nil {
			return fail(fmt.Errorf("the yaml has no top-level blueprint: section with name, domain and input"))
		}
		domain := strings.ToLower(in.Domain)
		if domain == "" {
			domain, _ = doc.Blueprint["domain"].(string)
		}
		path := automationBlueprintPath(in.Path)
		replaced, err := s.ha.SaveBlueprint(ctx, homeassistant.SaveBlueprintRequest{
			Domain: domain, Path: path, YAML: in.YAML, AllowOverride: in.Overwrite,
		})
		if err != nil {
			return fail(err)
		}
		verb := "Saved"
		if replaced {
			verb = "Replaced"
		}
		return text(automationBlueprintSaved(verb, domain, path, automationNormalize(doc.Blueprint).(map[string]any))), nil, nil

	case "delete":
		if in.Domain == "" || in.Path == "" {
			return fail(fmt.Errorf("domain and path are required to delete"))
		}
		path := automationBlueprintPath(in.Path)
		if !in.Confirm {
			return fail(fmt.Errorf("deleting the blueprint %s/%s removes the file. Check with the person first, then call again with confirm:true", in.Domain, path))
		}
		if err := s.ha.DeleteBlueprint(ctx, strings.ToLower(in.Domain), path); err != nil {
			if strings.Contains(err.Error(), "in use") {
				return fail(fmt.Errorf("%w. ha_find_related with item_type %s_blueprint shows what uses it", err, strings.ToLower(in.Domain)))
			}
			return fail(err)
		}
		return text(fmt.Sprintf("Deleted the %s blueprint %s.", in.Domain, path)), nil, nil

	default:
		return fail(fmt.Errorf("action must be import, save or delete, got %q", in.Action))
	}
}

// automationBlueprintPath adds the .yaml extension HA appends on save, so the
// path reported back is the one ha_list_blueprints and use_blueprint use.
func automationBlueprintPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if !strings.HasSuffix(p, ".yaml") {
		p += ".yaml"
	}
	return p
}

func automationBlueprintSaved(verb, domain, path string, meta map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s the %s blueprint %q at %s.", verb, domain, fmt.Sprint(meta["name"]), path)
	if inputs := automationBlueprintInputs(meta["input"]); len(inputs) > 0 {
		fmt.Fprintf(&b, "\ninputs: %s", strings.Join(inputs, "; "))
	}
	article := "a"
	if domain == "automation" {
		article = "an"
	}
	fmt.Fprintf(&b, "\nTo use it, save %s %s through ha_manage_automation with use_blueprint: {path: %s, input: {...}}.", article, domain, path)
	return b.String()
}

// --- YAML and JSON handling ---

func automationParseConfig(v any) (map[string]any, error) {
	val, err := automationParseValue(v)
	if err != nil {
		return nil, err
	}
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("config must be a mapping of keys (alias, triggers, actions, ...), not a %T", val)
	}
	return m, nil
}

// automationConfigJSON returns a save's config as JSON in the key order it
// was written, which HA keeps in the YAML file, and decoded for inspection.
func automationConfigJSON(req *mcp.CallToolRequest, v any) (json.RawMessage, map[string]any, error) {
	cfg, err := automationParseConfig(v)
	if err != nil {
		return nil, nil, err
	}
	body, err := organizeParseConfig(req, v)
	if err != nil {
		if body, err = json.Marshal(cfg); err != nil {
			return nil, nil, fmt.Errorf("config cannot be sent as JSON: %w", err)
		}
		return body, cfg, nil
	}
	var ordered map[string]any
	if err := json.Unmarshal(body, &ordered); err != nil {
		return nil, nil, fmt.Errorf("config cannot be sent as JSON: %w", err)
	}
	return body, ordered, nil
}

func automationParseValue(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return automationNormalize(v), nil
	}
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("the config text is empty")
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		return nil, fmt.Errorf("could not parse as YAML or JSON: %w", err)
	}
	if err := configYAMLTags(&n); err != nil {
		return nil, err
	}
	var out any
	if err := n.Decode(&out); err != nil {
		return nil, fmt.Errorf("could not parse as YAML or JSON: %w", err)
	}
	return automationNormalize(out), nil
}

// automationNormalize makes a decoded YAML value JSON-encodable: yaml.v3 gives
// map[any]any for non-string keys and time.Time for bare timestamps.
func automationNormalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = automationNormalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = automationNormalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = automationNormalize(val)
		}
		return out
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return v
	}
}

// automationKeyOrder puts the keys a person reads first at the top, the way
// HA's editor writes them; the rest follow alphabetically. JSON decoding has
// already lost the stored order.
var automationKeyOrder = func() map[string]int {
	keys := []string{
		"id", "alias", "name", "description", "icon", "use_blueprint", "path", "input", "mode", "max", "max_exceeded",
		"trigger_variables", "variables", "fields",
		"trigger", "triggers", "platform", "condition", "conditions", "action", "actions", "service", "event",
		"sequence", "entities", "metadata",
		"entity_id", "device_id", "area_id", "domain", "type", "event_type", "event_data",
		"from", "to", "at", "above", "below", "state", "attribute", "value_template", "for",
		"target", "data", "response_variable",
	}
	m := make(map[string]int, len(keys))
	for i, k := range keys {
		m[k] = i
	}
	return m
}()

func automationSortKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, iok := automationKeyOrder[keys[i]]
		pj, jok := automationKeyOrder[keys[j]]
		switch {
		case iok && jok:
			return pi < pj
		case iok != jok:
			return iok
		default:
			return keys[i] < keys[j]
		}
	})
	return keys
}

func automationYAMLNode(v any, flow bool) *yaml.Node {
	switch x := v.(type) {
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode}
		if flow {
			n.Style = yaml.FlowStyle
		}
		for _, k := range automationSortKeys(x) {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, automationYAMLNode(x[k], flow))
		}
		return n
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		if flow {
			n.Style = yaml.FlowStyle
		}
		for _, e := range x {
			n.Content = append(n.Content, automationYAMLNode(e, flow))
		}
		return n
	case string:
		n := &yaml.Node{}
		_ = n.Encode(x)
		if strings.Contains(x, "\n") && !flow {
			n.Style = yaml.LiteralStyle
		}
		return n
	default:
		n := &yaml.Node{}
		if err := n.Encode(x); err != nil {
			_ = n.Encode(fmt.Sprint(x))
		}
		return n
	}
}

func automationYAML(v any) string {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(automationYAMLNode(automationNormalize(v), false)); err != nil {
		j, _ := json.MarshalIndent(v, "", "  ")
		return string(j) + "\n"
	}
	_ = enc.Close()
	return b.String()
}

// automationRawYAML renders a stored config in the key order HA keeps it in,
// which is the order the person or the HA editor wrote it.
func automationRawYAML(raw json.RawMessage) string {
	if y, err := organizeYAML(raw); err == nil {
		return y
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	return automationYAML(v)
}

func automationFlowYAML(v any) string {
	out, err := yaml.Marshal(automationYAMLNode(automationNormalize(v), true))
	if err != nil {
		return automationCompact(v, 1000)
	}
	return strings.TrimSpace(string(out))
}

// --- diff ---

const automationMaxDiffCells = 4_000_000

// automationDiff is a line diff with two lines of context around each change.
// Configs are small, so a plain LCS table is fast enough; past the size limit
// the whole previous config is returned instead, which still lets the change
// be undone.
func automationDiff(before, after string) string {
	if before == after {
		return ""
	}
	a := strings.Split(strings.TrimRight(before, "\n"), "\n")
	b := strings.Split(strings.TrimRight(after, "\n"), "\n")
	if (len(a)+1)*(len(b)+1) > automationMaxDiffCells {
		return "(too large to diff; the previous config was)\n" + before
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte
		text string
	}
	var ops []line
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, line{' ', a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, line{'-', a[i]})
			i++
		default:
			ops = append(ops, line{'+', b[j]})
			j++
		}
	}
	const ctx = 2
	keep := make([]bool, len(ops))
	for k, op := range ops {
		if op.op == ' ' {
			continue
		}
		for d := max(0, k-ctx); d <= min(len(ops)-1, k+ctx); d++ {
			keep[d] = true
		}
	}
	var out strings.Builder
	gap := false
	for k, op := range ops {
		if !keep[k] {
			gap = true
			continue
		}
		if gap && out.Len() > 0 {
			out.WriteString("…\n")
		}
		gap = false
		out.WriteByte(op.op)
		out.WriteByte(' ')
		out.WriteString(op.text)
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

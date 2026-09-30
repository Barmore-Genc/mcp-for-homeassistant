package homeassistant

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestCheckBlueprintYAML(t *testing.T) {
	bad := []string{
		"blueprint:\n  name: !include .storage/auth\n  domain: automation\n",
		"blueprint:\n  name: !env_var HOME\n",
		"blueprint:\n  name: !secret x\n",
		"blueprint:\n  name: !include_dir_named .storage\n",
		"blueprint:\n  name: !include_dir_merge_list .\n",
		"blueprint:\n  name: !<!include> .storage/auth\n",
		"%TAG !e! !\n---\nblueprint:\n  name: !e!include .storage/auth\n",
		"%TAG !e! tag:example.com,2000:\n---\nblueprint:\n  name: !e!include x\n",
		"blueprint:\n  !include k: v\n",
		"actions:\n  - variables:\n      leak: !include .storage/auth\n",
		"a: &x !env_var y\nb: *x\n",
		"a: !!python/object/apply:os.system [id]\n",
		"blueprint:\n  name: x\n---\nleak: !include .storage/auth\n",
		"blueprint: [unclosed\n",
	}
	for _, y := range bad {
		if err := checkBlueprintYAML(y); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("accepted %q: %v", y, err)
		}
	}
	good := "blueprint:\n  name: Motion light\n  domain: automation\n  input:\n    light: {selector: {entity: {}}}\n" +
		"triggers:\n  - trigger: state\n    entity_id: !input light\n    to: 'on'\nactions:\n  - action: light.turn_on\n" +
		"    target: !!map {entity_id: !input light}\n    data: {text: \"!include is only text here\", n: !!int 3}\n"
	if err := checkBlueprintYAML(good); err != nil {
		t.Error(err)
	}
}

func TestSaveBlueprintRejectsTagsBeforeSending(t *testing.T) {
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		f.result(int64(msg["id"].(float64)), map[string]any{"overrides_existing": false})
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)
	_, err := c.SaveBlueprint(context.Background(), SaveBlueprintRequest{
		Domain: "automation", Path: "x", YAML: "blueprint:\n  name: !include .storage/auth\n  domain: automation\n",
	})
	if !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), "!include") {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.messages("blueprint/save")); n != 0 {
		t.Fatalf("%d save commands reached HA", n)
	}
}

func stubLookup(addrs map[string][]string) *Options {
	return &Options{LookupHost: func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range addrs[strings.TrimSuffix(host, ".")] {
			out = append(out, netip.MustParseAddr(a))
		}
		if len(out) == 0 {
			return nil, errors.New("no such host")
		}
		return out, nil
	}}
}

var publicLookup = stubLookup(map[string][]string{"example.com": {"93.184.215.14"}})

func TestImportBlueprintChecksRawData(t *testing.T) {
	leak := "SECRET-FROM-HA-HOST"
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		f.result(int64(msg["id"].(float64)), map[string]any{
			"suggested_filename": "example.com/x",
			"raw_data":           "blueprint:\n  name: !include .storage/auth\n  domain: automation\n",
			"blueprint":          map[string]any{"metadata": map[string]any{"name": leak, "domain": "automation"}},
			"validation_errors":  []string{leak},
		})
		return true
	})
	c := newTestClient(t, h.srv.URL, publicLookup)
	imp, err := c.ImportBlueprint(context.Background(), "https://example.com/x.yaml")
	if imp != nil || !errors.Is(err, ErrInvalidArgument) || strings.Contains(err.Error(), leak) {
		t.Fatalf("imp = %v, err = %v", imp, err)
	}
}

func TestImportBlueprintRedactsParseErrors(t *testing.T) {
	leak := `{"refresh_tokens": [...]}`
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		f.fail(int64(msg["id"].(float64)), "home_assistant_error", "Invalid blueprint: expected str @ data['blueprint']['name']. Got "+leak)
		return true
	})
	c := newTestClient(t, h.srv.URL, publicLookup)
	_, err := c.ImportBlueprint(context.Background(), "https://example.com/x.yaml")
	if err == nil || strings.Contains(err.Error(), leak) {
		t.Fatalf("err = %v", err)
	}
}

func TestBlueprintURLHosts(t *testing.T) {
	for _, u := range []string{
		"https://localhost./x.yaml", "https://127.1/x.yaml", "https://0x7f.1/x.yaml", "https://0x7f000001/x.yaml",
		"https://2130706433/x.yaml", "https://nas.local./x.yaml", "https://router.lan./x.yaml", "https://LOCALHOST/x.yaml",
		"https://printer.home.arpa./x.yaml", "https://[::ffff:127.0.0.1]/x.yaml", "https://1.2.3.4.5/x.yaml",
	} {
		if err := validateBlueprintURL(u); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	for _, u := range []string{"https://github.com/x.yaml", "https://gist.github.com./x.yaml", "https://cafe.be/x.yaml", "https://1password.com/x.yaml"} {
		if err := validateBlueprintURL(u); err != nil {
			t.Errorf("rejected %q: %v", u, err)
		}
	}

	c := newTestClient(t, "http://ha.test", stubLookup(map[string][]string{
		"127.0.0.1.nip.io": {"127.0.0.1"},
		"ten.example":      {"10.0.0.5"},
		"mixed.example":    {"93.184.215.14", "192.168.1.2"},
		"cgnat.example":    {"100.100.1.1"},
		"ll.example":       {"169.254.169.254"},
		"ula.example":      {"fd00::1"},
		"v6ll.example":     {"fe80::1"},
		"mapped.example":   {"::ffff:127.0.0.1"},
		"zero.example":     {"0.0.0.0"},
		"mc.example":       {"224.0.0.1"},
		"public.example":   {"93.184.215.14", "2606:2800:21f:cb07:6820:80da:af6b:8b2c"},
	}))
	ctx := context.Background()
	for _, h := range []string{"127.0.0.1.nip.io", "ten.example", "mixed.example", "cgnat.example", "ll.example",
		"ula.example", "v6ll.example", "mapped.example", "zero.example", "mc.example", "missing.example"} {
		if err := c.checkBlueprintHost(ctx, "https://"+h+"/x.yaml"); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("accepted host %s: %v", h, err)
		}
	}
	if err := c.checkBlueprintHost(ctx, "https://public.example/x.yaml"); err != nil {
		t.Error(err)
	}
}

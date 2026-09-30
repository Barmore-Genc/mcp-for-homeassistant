package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type restCapture struct {
	method, path, rawPath, query, auth string
	body                               map[string]any
}

func restServer(t *testing.T, status int, respBody string, headers map[string]string) (*httptest.Server, *restCapture, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	cap := &restCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cap.method, cap.path, cap.rawPath, cap.query = r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery
		cap.auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		cap.body = nil
		_ = json.Unmarshal(b, &cap.body)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, cap, &hits
}

func TestNewValidatesBaseURL(t *testing.T) {
	for _, u := range []string{
		"ftp://ha.local", "ha.local:8123", "http://user:pw@ha.local", "http://ha.local/?x=1", "http://ha.local/#frag", "http://",
	} {
		if _, err := New(u, "t", nil); err == nil {
			t.Errorf("New(%q) accepted", u)
		}
	}
	if _, err := New("http://ha.local:8123/", "tok\nen", nil); err == nil {
		t.Error("token with newline accepted")
	}
	if _, err := New("https://ha.example.com/", "t", nil); err != nil {
		t.Error(err)
	}
}

func TestPathInjectionRejected(t *testing.T) {
	srv, _, hits := restServer(t, 200, `{}`, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()

	bad := []string{
		"../api/config", "light.x/../../api", "light.x?a=b", "light.x#f", "light.%2e%2e", "LIGHT.X",
		"light.", ".x", "light", "light.x\n", "light.x y", "light.x/", "light..x",
	}
	for _, id := range bad {
		if _, err := c.GetState(ctx, id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("GetState(%q) err = %v", id, err)
		}
		if _, err := c.CameraSnapshot(ctx, id, 0, 0); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("CameraSnapshot(%q) err = %v", id, err)
		}
	}
	for _, id := range []string{"..", "../x", "a/b", "a?b", "a#b", "a%2fb", ".hidden", "a..b", "a b", "", strings.Repeat("a", 300)} {
		if _, err := c.GetConfigItem(ctx, KindAutomation, id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("GetConfigItem(automation, %q) err = %v", id, err)
		}
		if err := c.DeleteConfigItem(ctx, KindScene, id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("DeleteConfigItem(scene, %q) err = %v", id, err)
		}
	}
	for _, id := range []string{"My-Script", "a.b", "a-b"} {
		if _, err := c.GetConfigItem(ctx, KindScript, id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("GetConfigItem(script, %q) err = %v", id, err)
		}
	}
	for _, s := range []string{"../x", "light/turn_on", "Light", "a?b"} {
		if _, err := c.CallService(ctx, ServiceCall{Domain: s, Service: "x"}); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("CallService(domain %q) err = %v", s, err)
		}
		if _, err := c.CallService(ctx, ServiceCall{Domain: "light", Service: s}); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("CallService(service %q) err = %v", s, err)
		}
	}
	for _, id := range []string{"../x", "abc/def", "abc-def", ""} {
		if _, err := c.ReloadConfigEntry(ctx, id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("ReloadConfigEntry(%q) err = %v", id, err)
		}
	}
	if _, err := c.CalendarEvents(ctx, "calendar.x/../../y", time.Now(), time.Now()); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("CalendarEvents err = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server was reached %d times with invalid input", hits.Load())
	}
}

func TestValidIDsReachExpectedPath(t *testing.T) {
	srv, cap, _ := restServer(t, 200, `{"id":"1700000000001"}`, nil)
	c := newTestClient(t, srv.URL+"/ha", nil)
	ctx := context.Background()

	if _, err := c.GetConfigItem(ctx, KindAutomation, "1700000000001"); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/ha/api/config/automation/config/1700000000001" || cap.auth != "Bearer "+testToken {
		t.Fatalf("path %q auth %q", cap.path, cap.auth)
	}
	if _, err := c.GetConfigItem(ctx, KindScene, "My_Scene-1.v2"); err != nil {
		t.Fatal(err)
	}
	if cap.rawPath != "/ha/api/config/scene/config/My_Scene-1.v2" {
		t.Fatalf("raw path %q", cap.rawPath)
	}
}

func TestEndpointRejectsUnsafeSegments(t *testing.T) {
	c, _ := New("http://ha.local", "t", nil)
	for _, s := range []string{"", ".", "..", "a/b", "a?b", "a#b", "a%b", "a\\b", "a\x00"} {
		if _, err := c.endpoint("api", s); err == nil {
			t.Errorf("endpoint accepted %q", s)
		}
	}
}

func TestRESTErrorAndNoTokenLeak(t *testing.T) {
	srv, _, _ := restServer(t, 404, `{"message":"Entity not found."}`, nil)
	c := newTestClient(t, srv.URL, nil)
	_, err := c.GetState(context.Background(), "light.nope")
	var haErr *Error
	if !errors.As(err, &haErr) || haErr.StatusCode != 404 || haErr.Message != "Entity not found." || !IsNotFound(err) {
		t.Fatalf("got %#v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("token in error")
	}
	if err.Error() != "homeassistant: GET /api/states/light.nope: HTTP 404: Entity not found." {
		t.Fatalf("error text %q", err.Error())
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var leaked atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `[]`)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/states", http.StatusFound)
	}))
	defer srv.Close()

	// A caller-supplied client that would normally follow redirects.
	c := newTestClient(t, srv.URL, &Options{HTTPClient: &http.Client{}})
	_, err := c.ListStates(context.Background())
	var haErr *Error
	if !errors.As(err, &haErr) || haErr.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 error, got %v", err)
	}
	if leaked.Load() != nil {
		t.Fatal("request followed redirect to another host")
	}
}

func TestResponseSizeCap(t *testing.T) {
	big := `[` + strings.Repeat(`{"entity_id":"light.x","state":"on"},`, 2000) + `{}]`
	srv, _, _ := restServer(t, 200, big, nil)
	c := newTestClient(t, srv.URL, &Options{MaxResponseBytes: 1024})
	if _, err := c.ListStates(context.Background()); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}

	img := strings.Repeat("x", 2048)
	srv2, _, _ := restServer(t, 200, img, map[string]string{"Content-Type": "image/jpeg"})
	c2 := newTestClient(t, srv2.URL, &Options{MaxImageBytes: 1000})
	if _, err := c2.CameraSnapshot(context.Background(), "camera.x", 0, 0); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge for image, got %v", err)
	}
}

func TestCameraSnapshot(t *testing.T) {
	srv, cap, _ := restServer(t, 200, "\xff\xd8\xff\xe0jpegdata", map[string]string{"Content-Type": "image/jpeg"})
	c := newTestClient(t, srv.URL, nil)
	img, err := c.CameraSnapshot(context.Background(), "camera.front_door", 640, 0)
	if err != nil {
		t.Fatal(err)
	}
	if img.ContentType != "image/jpeg" || cap.path != "/api/camera_proxy/camera.front_door" || cap.query != "width=640" {
		t.Fatalf("got %s %s?%s", img.ContentType, cap.path, cap.query)
	}

	srv2, _, _ := restServer(t, 200, `<html>login</html>`, map[string]string{"Content-Type": "text/html"})
	c2 := newTestClient(t, srv2.URL, nil)
	if _, err := c2.CameraSnapshot(context.Background(), "camera.x", 0, 0); err == nil {
		t.Fatal("non-image accepted")
	}
}

func TestCallService(t *testing.T) {
	srv, cap, _ := restServer(t, 200, `{"changed_states":[],"service_response":{"a":1}}`, nil)
	c := newTestClient(t, srv.URL, nil)
	res, err := c.CallService(context.Background(), ServiceCall{
		Domain: "todo", Service: "get_items", ReturnResponse: true,
		Target: &Target{EntityID: []string{"todo.list"}},
		Data:   map[string]any{"status": "needs_action"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.path != "/api/services/todo/get_items" || cap.query != "return_response=" {
		t.Fatalf("request %s?%s", cap.path, cap.query)
	}
	if cap.body["status"] != "needs_action" || cap.body["entity_id"].([]any)[0] != "todo.list" {
		t.Fatalf("body %v", cap.body)
	}
	if string(res.Response) != `{"a":1}` {
		t.Fatalf("response %s", res.Response)
	}

	_, err = c.CallService(context.Background(), ServiceCall{
		Domain: "light", Service: "turn_on",
		Target: &Target{EntityID: []string{"light.a"}}, Data: map[string]any{"entity_id": "light.b"},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("conflicting target accepted: %v", err)
	}
}

func TestSaveConfigItemPinsID(t *testing.T) {
	srv, cap, _ := restServer(t, 200, `{"result":"ok"}`, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()
	if err := c.SaveConfigItem(ctx, KindAutomation, "abc", map[string]any{"id": "other", "alias": "x"}); err != nil {
		t.Fatal(err)
	}
	if cap.method != http.MethodPost || cap.body["id"] != "abc" {
		t.Fatalf("%s body %v", cap.method, cap.body)
	}
	if err := c.SaveConfigItem(ctx, KindScript, "my_script", map[string]any{"id": "x", "sequence": []any{}}); err != nil {
		t.Fatal(err)
	}
	if _, has := cap.body["id"]; has || cap.path != "/api/config/script/config/my_script" {
		t.Fatalf("script request %s %v", cap.path, cap.body)
	}
}

func TestWithTopLevelIDKeepsOrder(t *testing.T) {
	got, err := withTopLevelID(json.RawMessage(`{"alias":"x","id":"other","triggers":[{"z":1,"id":2}],"mode":"single"}`), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"abc","alias":"x","triggers":[{"z":1,"id":2}],"mode":"single"}`; string(got) != want {
		t.Fatalf("got %s", got)
	}
	got, _ = withTopLevelID(json.RawMessage(`{"id":"x","sequence":[]}`), "")
	if string(got) != `{"sequence":[]}` {
		t.Fatalf("got %s", got)
	}
	if err := (&Client{}).SaveConfigItemRaw(context.Background(), KindAutomation, "abc", json.RawMessage(`[1]`)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("a non-object config was accepted: %v", err)
	}
}

func TestBackupCreateSettingsDropsPassword(t *testing.T) {
	var s BackupCreateSettings
	if err := json.Unmarshal([]byte(`{"agent_ids":["backup.local"],"include_database":true,"password":"s3cret"}`), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Encrypted || !s.IncludeDatabase || len(s.AgentIDs) != 1 {
		t.Fatalf("decoded %+v", s)
	}
	if b, _ := json.Marshal(s); strings.Contains(string(b), "s3cret") {
		t.Fatalf("password kept: %s", b)
	}
}

func TestErrorLogTail(t *testing.T) {
	var gotRange string
	body := strings.Repeat("old line\n", 1000) + "last line one\nlast line two\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)
	log, err := c.GetErrorLog(context.Background(), 40)
	if err != nil {
		t.Fatal(err)
	}
	if gotRange != "bytes=-40" {
		t.Fatalf("range %q", gotRange)
	}
	if !log.Truncated || log.Text != "old line\nlast line one\nlast line two\n" {
		t.Fatalf("tail %+v", log)
	}
}

func TestPartialUpdateJSON(t *testing.T) {
	b, _ := json.Marshal(AreaUpdate{Name: "Kitchen", FloorID: Null[string](), Icon: Set("mdi:x"), Labels: []string{}})
	if string(b) != `{"name":"Kitchen","floor_id":null,"icon":"mdi:x","labels":[]}` {
		t.Fatalf("got %s", b)
	}
	b, _ = json.Marshal(EntityUpdate{Categories: map[string]*string{"automation": nil}})
	if string(b) != `{"categories":{"automation":null}}` {
		t.Fatalf("got %s", b)
	}
	b, _ = json.Marshal(FloorUpdate{})
	if string(b) != `{}` {
		t.Fatalf("got %s", b)
	}
}

func TestBlueprintValidation(t *testing.T) {
	for _, u := range []string{
		"http://github.com/x.yaml", "https://127.0.0.1/x.yaml", "https://[::1]/x.yaml", "https://localhost/x.yaml",
		"https://homeassistant.local/x.yaml", "https://intranet/x.yaml", "https://user:pw@github.com/x.yaml",
		"https://github.com:8443/x.yaml", "file:///etc/passwd", "https://metadata.google.internal/x",
	} {
		if err := validateBlueprintURL(u); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	if err := validateBlueprintURL("https://github.com/home-assistant/core/blob/dev/x.yaml"); err != nil {
		t.Error(err)
	}
	for _, p := range []string{"../secrets.yaml", "a/../../b", "/etc/x", "a//b", "a/./b", "", "a\\b"} {
		if err := validateBlueprintPath(p); err == nil {
			t.Errorf("accepted path %q", p)
		}
	}
	if err := validateBlueprintPath("homeassistant/motion_light.yaml"); err != nil {
		t.Error(err)
	}
}

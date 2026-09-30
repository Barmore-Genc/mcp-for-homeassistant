package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testToken = "secret-test-token"

type fakeConn struct {
	t    *testing.T
	conn *websocket.Conn
	mu   sync.Mutex
}

func (f *fakeConn) send(v any) {
	b, _ := json.Marshal(v)
	f.sendRaw(b)
}

func (f *fakeConn) sendRaw(b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.conn.Write(context.Background(), websocket.MessageText, b)
}

func (f *fakeConn) result(id int64, result any) {
	f.send(map[string]any{"id": id, "type": "result", "success": true, "result": result})
}

func (f *fakeConn) fail(id int64, code, msg string) {
	f.send(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": code, "message": msg}})
}

func (f *fakeConn) event(id int64, ev any) {
	f.send(map[string]any{"id": id, "type": "event", "event": ev})
}

type fakeHA struct {
	srv        *httptest.Server
	conns      atomic.Int32
	ignorePing atomic.Bool

	mu       sync.Mutex
	received []map[string]any
	// handle answers one command; return false to close the connection.
	handle func(f *fakeConn, msg map[string]any) bool
}

func newFakeHA(t *testing.T, handle func(f *fakeConn, msg map[string]any) bool) *fakeHA {
	t.Helper()
	h := &fakeHA{handle: handle}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/websocket" {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(1 << 20)
		h.conns.Add(1)
		f := &fakeConn{t: t, conn: c}
		ctx := context.Background()
		f.send(map[string]any{"type": "auth_required", "ha_version": "2026.9.4"})
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var auth map[string]any
		_ = json.Unmarshal(data, &auth)
		if auth["type"] != "auth" || auth["access_token"] != testToken {
			f.send(map[string]any{"type": "auth_invalid", "message": "Invalid access token or password"})
			return
		}
		f.send(map[string]any{"type": "auth_ok", "ha_version": "2026.9.4"})
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Errorf("invalid JSON from client: %s", data)
				return
			}
			h.mu.Lock()
			h.received = append(h.received, msg)
			h.mu.Unlock()
			id := int64(msg["id"].(float64))
			switch msg["type"] {
			case "supported_features":
				f.result(id, nil)
				continue
			case "ping":
				if !h.ignorePing.Load() {
					f.send(map[string]any{"id": id, "type": "pong"})
				}
				continue
			}
			if !h.handle(f, msg) {
				return
			}
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHA) messages(typ string) []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]any
	for _, m := range h.received {
		if m["type"] == typ {
			out = append(out, m)
		}
	}
	return out
}

func newTestClient(t *testing.T, url string, opts *Options) *Client {
	t.Helper()
	c, err := New(url, testToken, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestWSCorrelationOutOfOrder(t *testing.T) {
	const n = 40
	var (
		mu      sync.Mutex
		waiting []map[string]any
	)
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		mu.Lock()
		waiting = append(waiting, msg)
		if len(waiting) == n {
			for i := len(waiting) - 1; i >= 0; i-- {
				m := waiting[i]
				f.result(int64(m["id"].(float64)), map[string]any{"echo": m["area_id"]})
			}
			waiting = nil
		}
		mu.Unlock()
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("area_%d", i)
			var out struct{ Echo string }
			if err := c.wsCall(context.Background(), "test/echo", map[string]any{"area_id": want}, &out); err != nil {
				errs <- err
				return
			}
			if out.Echo != want {
				errs <- fmt.Errorf("got %q want %q", out.Echo, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := h.conns.Load(); got != 1 {
		t.Fatalf("expected 1 shared connection, got %d", got)
	}
}

func TestWSAuthInvalid(t *testing.T) {
	h := newFakeHA(t, func(*fakeConn, map[string]any) bool { return true })
	c, err := New(h.srv.URL, "wrong-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.ListAreas(context.Background())
	var haErr *Error
	if !errors.As(err, &haErr) || haErr.Code != "auth_invalid" || !IsUnauthorized(err) {
		t.Fatalf("expected auth_invalid, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatal("token leaked into error")
	}
}

func TestWSErrorResult(t *testing.T) {
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		f.fail(int64(msg["id"].(float64)), "not_found", "Entity not found")
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)
	_, err := c.GetEntityRegistryEntry(context.Background(), "light.nope")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "config/entity_registry/get: not_found: Entity not found") {
		t.Fatalf("got %v", err)
	}
}

func TestWSReconnectAfterDrop(t *testing.T) {
	var calls atomic.Int32
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		if calls.Add(1) == 1 {
			return false
		}
		f.result(int64(msg["id"].(float64)), []any{})
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)
	ctx := context.Background()

	if _, err := c.ListAreas(ctx); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("expected ErrConnectionClosed, got %v", err)
	}
	if _, err := c.ListAreas(ctx); err != nil {
		t.Fatalf("call after reconnect: %v", err)
	}
	if got := h.conns.Load(); got != 2 {
		t.Fatalf("expected 2 connections, got %d", got)
	}
}

func TestWSCallContextCancel(t *testing.T) {
	h := newFakeHA(t, func(*fakeConn, map[string]any) bool { return true })
	c := newTestClient(t, h.srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.ListAreas(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	ws.mu.Lock()
	pending := len(ws.pending)
	ws.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending calls leaked: %d", pending)
	}
}

func TestWSCancelledWriteKeepsSharedConnection(t *testing.T) {
	unblock := make(chan struct{})
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		if msg["type"] == "test/block" {
			<-unblock
		}
		f.result(int64(msg["id"].(float64)), nil)
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)
	ctx := context.Background()
	if _, err := c.ListAreas(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.wsCall(ctx, "test/block", nil, nil) }()
	time.Sleep(50 * time.Millisecond)

	// While the server is not reading, a large frame fills the socket buffers
	// and the write is still in progress when the caller's deadline passes.
	time.AfterFunc(300*time.Millisecond, func() { close(unblock) })
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	big := map[string]any{"pad": strings.Repeat("x", 900<<10)}
	if err := c.wsCall(short, "test/big", big, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}

	if _, err := c.ListAreas(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.conns.Load(); got != 1 {
		t.Fatalf("a cancelled write reconnected the shared connection: %d connections", got)
	}
}

func TestWSCoalescedMessages(t *testing.T) {
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		id := int64(msg["id"].(float64))
		if msg["type"] == "subscribe_events" {
			f.result(id, nil)
			ev := func(n int) string {
				return fmt.Sprintf(`{"id":%d,"type":"event","event":{"event_type":"x","data":{"n":%d},"origin":"LOCAL","time_fired":"2026-01-01T00:00:00+00:00","context":{"id":"c"}}}`, id, n)
			}
			f.sendRaw([]byte("[" + ev(1) + "," + ev(2) + "," + ev(3) + "]"))
			return true
		}
		f.result(id, nil)
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)
	res, err := c.CollectEvents(context.Background(), CollectEventsOptions{EventType: "x", Duration: 5 * time.Second, MaxEvents: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 3 || !res.LimitReached || string(res.Events[2].Data) != `{"n":3}` {
		t.Fatalf("got %+v", res)
	}
}

func subscribeServer(t *testing.T, events int) *fakeHA {
	return newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		id := int64(msg["id"].(float64))
		switch msg["type"] {
		case "subscribe_events":
			f.result(id, nil)
			for i := range events {
				f.event(id, map[string]any{"event_type": msg["event_type"], "data": map[string]any{"i": i}, "time_fired": "2026-01-01T00:00:00+00:00"})
			}
		default:
			f.result(id, nil)
		}
		return true
	})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollectEventsUnsubscribes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events int
		max    int
		dur    time.Duration
	}{
		{"limit", 10, 5, 5 * time.Second},
		{"duration", 0, 5, 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := subscribeServer(t, tc.events)
			c := newTestClient(t, h.srv.URL, nil)
			res, err := c.CollectEvents(context.Background(), CollectEventsOptions{EventType: "state_changed", Duration: tc.dur, MaxEvents: tc.max})
			if err != nil {
				t.Fatal(err)
			}
			if tc.events > 0 && len(res.Events) != tc.max {
				t.Fatalf("got %d events", len(res.Events))
			}
			subs := h.messages("subscribe_events")
			waitUntil(t, func() bool { return len(h.messages("unsubscribe_events")) == 1 })
			unsub := h.messages("unsubscribe_events")[0]
			if unsub["subscription"] != subs[0]["id"] {
				t.Fatalf("unsubscribed %v, subscribed %v", unsub["subscription"], subs[0]["id"])
			}
			c.ws.mu.Lock()
			n := len(c.ws.subs)
			c.ws.mu.Unlock()
			if n != 0 {
				t.Fatalf("%d subscriptions left", n)
			}
		})
	}
}

func TestSubscriptionContextCancelUnsubscribes(t *testing.T) {
	h := subscribeServer(t, 1)
	c := newTestClient(t, h.srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := c.SubscribeEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, open, err := sub.Next(context.Background()); !open || err != nil {
		t.Fatalf("next: %v %v", open, err)
	}
	cancel()
	waitUntil(t, func() bool { return len(h.messages("unsubscribe_events")) == 1 })
	for range sub.Events() {
	}
	if _, has := h.messages("subscribe_events")[0]["event_type"]; has {
		t.Fatal("empty event type must subscribe to all events")
	}
}

func TestSubscriptionOverflowDoesNotBlockReader(t *testing.T) {
	h := subscribeServer(t, defaultSubscriptionBuf+50)
	c := newTestClient(t, h.srv.URL, nil)
	sub, err := c.SubscribeEvents(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	waitUntil(t, func() bool { return sub.Dropped() == 50 })
	if _, err := c.ListAreas(context.Background()); err != nil {
		t.Fatalf("reader blocked by full subscription: %v", err)
	}
}

func TestSubscriptionEndsOnConnectionDrop(t *testing.T) {
	var drop atomic.Bool
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		if drop.Load() {
			return false
		}
		f.result(int64(msg["id"].(float64)), nil)
		return true
	})
	c := newTestClient(t, h.srv.URL, nil)
	sub, err := c.SubscribeEvents(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	drop.Store(true)
	_, _ = c.ListAreas(context.Background())
	for range sub.Events() {
	}
	if !errors.Is(sub.Err(), ErrConnectionClosed) {
		t.Fatalf("expected ErrConnectionClosed, got %v", sub.Err())
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close after drop: %v", err)
	}
}

func TestWSPingKeepalive(t *testing.T) {
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		f.result(int64(msg["id"].(float64)), []any{})
		return true
	})
	c := newTestClient(t, h.srv.URL, &Options{PingInterval: 50 * time.Millisecond})
	if _, err := c.ListAreas(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(h.messages("ping")) >= 3 })
}

func TestWSPingFailureCloses(t *testing.T) {
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		f.result(int64(msg["id"].(float64)), []any{})
		return true
	})
	c := newTestClient(t, h.srv.URL, &Options{PingInterval: 50 * time.Millisecond})
	if _, err := c.ListAreas(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	h.ignorePing.Store(true)
	waitUntil(t, ws.isClosed)
	h.ignorePing.Store(false)
	if _, err := c.ListAreas(context.Background()); err != nil {
		t.Fatalf("reconnect after keepalive failure: %v", err)
	}
	if h.conns.Load() != 2 {
		t.Fatalf("expected reconnect, got %d connections", h.conns.Load())
	}
}

func TestWSClose(t *testing.T) {
	block := make(chan struct{})
	h := newFakeHA(t, func(f *fakeConn, msg map[string]any) bool {
		<-block
		return true
	})
	defer close(block)
	c := newTestClient(t, h.srv.URL, nil)
	if _, err := c.HAVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.ListAreas(context.Background())
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrConnectionClosed) {
			t.Fatalf("pending call: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending call not released by Close")
	}
	if _, err := c.ListAreas(context.Background()); !errors.Is(err, errClientClosed) {
		t.Fatalf("call after Close: %v", err)
	}
}

func TestEncodeCommandEnvelopeWins(t *testing.T) {
	b, err := encodeCommand(7, "person/list", map[string]any{"type": "config/auth/delete", "id": 99, "x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), `"id":7,"type":"person/list"}`) {
		t.Fatalf("envelope not last: %s", b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "person/list" || m["id"] != float64(7) {
		t.Fatalf("decoded %v", m)
	}
	b, _ = encodeCommand(1, "ping", nil)
	if string(b) != `{"id":1,"type":"ping"}` {
		t.Fatalf("got %s", b)
	}
	if _, err := encodeCommand(1, "x", []int{1}); err == nil {
		t.Fatal("non-object payload accepted")
	}
}

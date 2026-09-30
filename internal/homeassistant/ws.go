package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const maxPingTimeout = 10 * time.Second

type wsMessage struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Success   *bool           `json:"success"`
	Result    json.RawMessage `json:"result"`
	Event     json.RawMessage `json:"event"`
	Error     *wsError        `json:"error"`
	HAVersion string          `json:"ha_version"`
	Message   string          `json:"message"`
}

type wsError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	TranslationKey string `json:"translation_key"`
}

type wsConn struct {
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	haVersion string

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan wsMessage
	subs    map[int64]*Subscription
	err     error
	done    chan struct{}
}

func (c *Client) wsURL() string {
	u := *c.base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = c.base.Path + "/api/websocket"
	u.RawPath = ""
	return u.String()
}

func (c *Client) getWS(ctx context.Context) (*wsConn, error) {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("homeassistant: %w", errClientClosed)
	}
	if c.ws != nil && !c.ws.isClosed() {
		return c.ws, nil
	}
	ws, err := c.dialWS(ctx)
	if err != nil {
		return nil, err
	}
	c.ws = ws
	return ws, nil
}

func (c *Client) dialWS(ctx context.Context) (*wsConn, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, c.wsURL(), &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, &Error{Op: "websocket connect", StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		}
		return nil, fmt.Errorf("homeassistant: websocket connect: %w", err)
	}
	conn.SetReadLimit(c.maxWSMessage)

	haVersion, err := c.authenticate(ctx, conn)
	if err != nil {
		conn.CloseNow()
		return nil, err
	}

	lifetime, stop := context.WithCancel(context.Background())
	w := &wsConn{
		conn:      conn,
		ctx:       lifetime,
		cancel:    stop,
		haVersion: haVersion,
		pending:   map[int64]chan wsMessage{},
		subs:      map[int64]*Subscription{},
		done:      make(chan struct{}),
	}
	go w.readLoop()
	go w.pingLoop(c.pingInterval)

	// Coalescing lets HA batch bursts of events into one frame, which keeps
	// its per-connection send queue short; readLoop accepts both forms.
	if _, err := w.call(ctx, "supported_features", map[string]any{
		"features": map[string]int{"coalesce_messages": 1},
	}); err != nil {
		w.close(err)
		return nil, err
	}
	return w, nil
}

func (c *Client) authenticate(ctx context.Context, conn *websocket.Conn) (string, error) {
	var m wsMessage
	if err := readJSON(ctx, conn, &m); err != nil {
		return "", fmt.Errorf("homeassistant: websocket auth: %w", err)
	}
	if m.Type != "auth_required" {
		return "", fmt.Errorf("homeassistant: websocket auth: unexpected message %q", m.Type)
	}
	auth, _ := json.Marshal(map[string]string{"type": "auth", "access_token": c.token})
	if err := conn.Write(ctx, websocket.MessageText, auth); err != nil {
		return "", fmt.Errorf("homeassistant: websocket auth: %w", err)
	}
	m = wsMessage{}
	if err := readJSON(ctx, conn, &m); err != nil {
		return "", fmt.Errorf("homeassistant: websocket auth: %w", err)
	}
	switch m.Type {
	case "auth_ok":
		return m.HAVersion, nil
	case "auth_invalid":
		return "", &Error{Op: "websocket auth", Code: "auth_invalid", Message: sanitizeText(m.Message, 256)}
	default:
		return "", fmt.Errorf("homeassistant: websocket auth: unexpected message %q", m.Type)
	}
}

func readJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (w *wsConn) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err != nil
}

func (w *wsConn) close(err error) {
	w.mu.Lock()
	if w.err != nil {
		w.mu.Unlock()
		return
	}
	w.err = err
	subs := w.subs
	w.subs = nil
	w.pending = nil
	w.mu.Unlock()

	close(w.done)
	w.cancel()
	if errors.Is(err, errClientClosed) {
		_ = w.conn.Close(websocket.StatusNormalClosure, "")
	} else {
		_ = w.conn.CloseNow()
	}
	for _, s := range subs {
		s.finish(fmt.Errorf("homeassistant: %w", ErrConnectionClosed))
	}
}

func (w *wsConn) readLoop() {
	for {
		_, data, err := w.conn.Read(w.ctx)
		if err != nil {
			w.close(fmt.Errorf("%w: %v", ErrConnectionClosed, err))
			return
		}
		data = bytes.TrimSpace(data)
		if len(data) > 0 && data[0] == '[' {
			var batch []json.RawMessage
			if err := json.Unmarshal(data, &batch); err != nil {
				w.close(fmt.Errorf("%w: invalid JSON from server", ErrConnectionClosed))
				return
			}
			for _, raw := range batch {
				w.dispatch(raw)
			}
			continue
		}
		w.dispatch(data)
	}
}

func (w *wsConn) dispatch(raw []byte) {
	var m wsMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	w.mu.Lock()
	switch m.Type {
	case "result", "pong":
		ch := w.pending[m.ID]
		delete(w.pending, m.ID)
		w.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case "event":
		s := w.subs[m.ID]
		w.mu.Unlock()
		if s != nil {
			s.deliver(m.Event)
		}
	default:
		w.mu.Unlock()
	}
}

func (w *wsConn) pingLoop(interval time.Duration) {
	timeout := min(maxPingTimeout, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(w.ctx, timeout)
			_, err := w.call(ctx, "ping", nil)
			cancel()
			if err != nil {
				w.close(fmt.Errorf("%w: keepalive failed: %v", ErrConnectionClosed, err))
				return
			}
		}
	}
}

// register reserves a message id with a result channel and, for
// subscriptions, the subscription that will receive its events. Both are in
// place before the command is written so no reply can be missed.
func (w *wsConn) register(ch chan wsMessage, sub *Subscription) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, errNotSent
	}
	if sub != nil && len(w.subs) >= maxSubscriptions {
		return 0, fmt.Errorf("homeassistant: too many active subscriptions (max %d)", maxSubscriptions)
	}
	w.nextID++
	id := w.nextID
	w.pending[id] = ch
	if sub != nil {
		sub.id = id
		w.subs[id] = sub
	}
	return id, nil
}

func (w *wsConn) forget(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != nil {
		delete(w.pending, id)
	}
}

func (w *wsConn) removeSub(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.subs != nil {
		delete(w.subs, id)
	}
}

var errNotSent = errors.New("connection closed before send")

func (w *wsConn) call(ctx context.Context, typ string, payload any) (json.RawMessage, error) {
	return w.callWithSub(ctx, typ, payload, nil)
}

func (w *wsConn) callWithSub(ctx context.Context, typ string, payload any, sub *Subscription) (json.RawMessage, error) {
	ch := make(chan wsMessage, 1)
	// HA rejects an id lower than one it has already seen, so allocating the id
	// and writing the frame must happen under the same lock.
	w.writeMu.Lock()
	id, err := w.register(ch, sub)
	if err != nil {
		w.writeMu.Unlock()
		return nil, err
	}
	msg, err := encodeCommand(id, typ, payload)
	if err != nil {
		w.writeMu.Unlock()
		w.forget(id)
		if sub != nil {
			w.removeSub(id)
		}
		return nil, err
	}
	err = w.conn.Write(ctx, websocket.MessageText, msg)
	w.writeMu.Unlock()
	if err != nil {
		// A failed or interrupted write leaves the frame stream in an unknown
		// state, so the connection cannot be reused.
		w.close(fmt.Errorf("%w: write: %v", ErrConnectionClosed, err))
		if ctx.Err() != nil {
			return nil, fmt.Errorf("homeassistant: %s: %w", typ, ctx.Err())
		}
		return nil, fmt.Errorf("homeassistant: %s: %w", typ, ErrConnectionClosed)
	}

	select {
	case m := <-ch:
		return resultOf(typ, m)
	case <-ctx.Done():
		w.forget(id)
		return nil, fmt.Errorf("homeassistant: %s: %w", typ, ctx.Err())
	case <-w.done:
		select {
		case m := <-ch:
			return resultOf(typ, m)
		default:
		}
		return nil, fmt.Errorf("homeassistant: %s: %w", typ, ErrConnectionClosed)
	}
}

func resultOf(typ string, m wsMessage) (json.RawMessage, error) {
	if m.Type == "pong" {
		return nil, nil
	}
	if m.Success == nil || !*m.Success {
		e := &Error{Op: typ, Code: "unknown_error"}
		if m.Error != nil {
			e.Code = m.Error.Code
			e.Message = m.Error.Message
			e.TranslationKey = m.Error.TranslationKey
		}
		return nil, e
	}
	return m.Result, nil
}

// encodeCommand appends id and type after the payload fields. HA's JSON
// decoder keeps the last duplicate key, so a payload can never override them.
func encodeCommand(id int64, typ string, payload any) ([]byte, error) {
	body := []byte("{}")
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("homeassistant: %s: encode request: %w", typ, err)
		}
		if string(b) != "null" {
			body = b
		}
	}
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, fmt.Errorf("homeassistant: %s: payload must be a JSON object", typ)
	}
	t, _ := json.Marshal(typ)
	out := make([]byte, 0, len(body)+len(t)+32)
	out = append(out, body[:len(body)-1]...)
	if len(bytes.TrimSpace(body[1:len(body)-1])) > 0 {
		out = append(out, ',')
	}
	out = append(out, `"id":`...)
	out = strconv.AppendInt(out, id, 10)
	out = append(out, `,"type":`...)
	out = append(out, t...)
	out = append(out, '}')
	return out, nil
}

// wsCall sends one command and decodes its result into out (if non-nil).
func (c *Client) wsCall(ctx context.Context, typ string, payload any, out any) error {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	var (
		raw json.RawMessage
		err error
	)
	for attempt := 0; attempt < 2; attempt++ {
		var ws *wsConn
		ws, err = c.getWS(ctx)
		if err != nil {
			return err
		}
		raw, err = ws.call(ctx, typ, payload)
		if !errors.Is(err, errNotSent) {
			break
		}
	}
	if errors.Is(err, errNotSent) {
		return fmt.Errorf("homeassistant: %s: %w", typ, ErrConnectionClosed)
	}
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("homeassistant: %s: decode result: %w", typ, err)
	}
	return nil
}

// HAVersion returns the Home Assistant version reported during the WebSocket
// handshake, connecting if needed.
func (c *Client) HAVersion(ctx context.Context) (string, error) {
	ws, err := c.getWS(ctx)
	if err != nil {
		return "", err
	}
	return ws.haVersion, nil
}

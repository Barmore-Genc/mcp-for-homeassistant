// Package homeassistant is a client for the Home Assistant REST and WebSocket APIs.
package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestTimeout = 30 * time.Second
	defaultMaxResponse    = 32 << 20
	defaultMaxImage       = 20 << 20
	defaultMaxWSMessage   = 64 << 20
	defaultPingInterval   = 30 * time.Second
	maxErrorBody          = 4 << 10
	maxSubscriptions      = 64
)

// Options tunes a Client. Zero values select the defaults.
type Options struct {
	// HTTPClient is used for REST calls and the WebSocket handshake. Its
	// CheckRedirect is replaced so that credentials never follow a redirect.
	HTTPClient *http.Client
	// RequestTimeout bounds each REST request and WebSocket command that has no
	// earlier context deadline. Default 30s.
	RequestTimeout time.Duration
	// MaxResponseBytes caps REST JSON bodies and WebSocket messages. Default 32 MiB
	// for REST and 64 MiB for WebSocket messages.
	MaxResponseBytes int64
	// MaxImageBytes caps camera snapshots. Default 20 MiB.
	MaxImageBytes int64
	// PingInterval is the WebSocket keepalive interval. Default 30s.
	PingInterval time.Duration
}

// Client talks to one Home Assistant instance. It is safe for concurrent use.
type Client struct {
	base           *url.URL
	token          string
	http           *http.Client
	requestTimeout time.Duration
	maxResponse    int64
	maxWSMessage   int64
	maxImage       int64
	pingInterval   time.Duration

	wsMu   sync.Mutex
	ws     *wsConn
	closed bool
}

// New creates a client for the instance at baseURL (for example
// "http://homeassistant.local:8123") authenticated with a long-lived access token.
func New(baseURL, token string, opts *Options) (*Client, error) {
	if opts == nil {
		opts = &Options{}
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("homeassistant: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("homeassistant: base URL must use http or https")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("homeassistant: base URL must be scheme://host[:port][/path] without credentials, query or fragment")
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("homeassistant: invalid access token")
	}

	c := &Client{
		base:           u,
		token:          token,
		http:           safeHTTPClient(opts.HTTPClient),
		requestTimeout: opts.RequestTimeout,
		maxResponse:    opts.MaxResponseBytes,
		maxWSMessage:   opts.MaxResponseBytes,
		maxImage:       opts.MaxImageBytes,
		pingInterval:   opts.PingInterval,
	}
	if c.requestTimeout <= 0 {
		c.requestTimeout = defaultRequestTimeout
	}
	if c.maxResponse <= 0 {
		c.maxResponse = defaultMaxResponse
		c.maxWSMessage = defaultMaxWSMessage
	}
	if c.maxImage <= 0 {
		c.maxImage = defaultMaxImage
	}
	if c.pingInterval <= 0 {
		c.pingInterval = defaultPingInterval
	}
	return c, nil
}

func safeHTTPClient(in *http.Client) *http.Client {
	var hc http.Client
	if in != nil {
		hc = *in
	}
	// Refusing redirects keeps the bearer token on the configured origin; HA's
	// API never redirects, so a redirect means a proxy or an attacker.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &hc
}

// Close closes the WebSocket connection. The client must not be used afterwards.
func (c *Client) Close() error {
	c.wsMu.Lock()
	ws := c.ws
	c.ws = nil
	c.closed = true
	c.wsMu.Unlock()
	if ws != nil {
		ws.close(errClientClosed)
	}
	return nil
}

// withTimeout applies the default timeout unless the caller already set a deadline.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.requestTimeout)
}

// endpoint builds an absolute URL under the base URL from path segments. Every
// segment must already be validated; escaping is a second line of defence.
func (c *Client) endpoint(segments ...string) (*url.URL, error) {
	for _, s := range segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/?#%\\") || hasControl(s) {
			return nil, fmt.Errorf("homeassistant: invalid path segment %q", s)
		}
	}
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	u := *c.base
	u.Path = c.base.Path + "/" + strings.Join(segments, "/")
	u.RawPath = c.base.EscapedPath() + "/" + strings.Join(escaped, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return &u, nil
}

type restRequest struct {
	method  string
	path    []string
	query   url.Values
	body    any
	maxBody int64
	timeout time.Duration
}

type rawResponse struct {
	body        []byte
	contentType string
}

func (c *Client) doRaw(ctx context.Context, r restRequest) (*rawResponse, error) {
	u, err := c.endpoint(r.path...)
	if err != nil {
		return nil, err
	}
	if len(r.query) > 0 {
		u.RawQuery = r.query.Encode()
	}
	op := r.method + " " + u.EscapedPath()

	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, fmt.Errorf("homeassistant: %s: encode request: %w", op, err)
		}
		body = bytes.NewReader(b)
	}

	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	} else {
		var cancel context.CancelFunc
		ctx, cancel = c.withTimeout(ctx)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("homeassistant: %s: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("homeassistant: %s: %w", op, scrubURLError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, restError(op, resp.StatusCode, b)
	}

	limit := r.maxBody
	if limit <= 0 {
		limit = c.maxResponse
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("homeassistant: %s: read response: %w", op, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("homeassistant: %s: %w (limit %d bytes)", op, ErrResponseTooLarge, limit)
	}
	return &rawResponse{body: b, contentType: resp.Header.Get("Content-Type")}, nil
}

func (c *Client) doJSON(ctx context.Context, r restRequest, out any) error {
	resp, err := c.doRaw(ctx, r)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return fmt.Errorf("homeassistant: %s %s: decode response: %w", r.method, strings.Join(r.path, "/"), err)
	}
	return nil
}

// scrubURLError drops the URL from transport errors because op already names it.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

package homeassistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrResponseTooLarge is returned when a response exceeds the configured cap.
	ErrResponseTooLarge = errors.New("response too large")
	// ErrInvalidArgument wraps validation failures of caller-supplied values.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrConnectionClosed is returned for WebSocket commands interrupted by a
	// dropped connection. The next call reconnects.
	ErrConnectionClosed = errors.New("websocket connection closed")

	errClientClosed = errors.New("client closed")
)

// Error is an error reported by Home Assistant.
type Error struct {
	// Op is the REST method and path, or the WebSocket command type.
	Op string
	// StatusCode is the HTTP status for REST calls, 0 for WebSocket commands.
	StatusCode int
	// Code is the WebSocket error code (for example "not_found"), or
	// "auth_invalid" when WebSocket authentication failed.
	Code string
	// Message is the human-readable message from Home Assistant.
	Message string
	// TranslationKey is set for some WebSocket errors.
	TranslationKey string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("homeassistant: ")
	b.WriteString(e.Op)
	b.WriteString(": ")
	switch {
	case e.StatusCode != 0:
		fmt.Fprintf(&b, "HTTP %d", e.StatusCode)
	case e.Code != "":
		b.WriteString(e.Code)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(oneLine(e.Message))
	}
	return b.String()
}

// oneLine collapses line breaks and other control characters into spaces, so
// a message from HA cannot add lines that read like the caller's own output.
func oneLine(s string) string {
	isBreak := func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }
	if strings.IndexFunc(s, isBreak) < 0 {
		return s
	}
	return strings.Join(strings.FieldsFunc(s, isBreak), " ")
}

// IsNotFound reports whether err is a Home Assistant "not found" error.
func IsNotFound(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.StatusCode == http.StatusNotFound || e.Code == "not_found"
}

// IsUnauthorized reports whether err means the token was rejected or lacks
// permission (non-admin user).
func IsUnauthorized(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden ||
		e.Code == "unauthorized" || e.Code == "auth_invalid"
}

func restError(op string, status int, body []byte) *Error {
	e := &Error{Op: op, StatusCode: status}
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		e.Message = m.Message
	} else {
		e.Message = sanitizeText(string(body), 512)
	}
	return e
}

func sanitizeText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func invalidArg(format string, args ...any) error {
	return fmt.Errorf("homeassistant: %w: %s", ErrInvalidArgument, fmt.Sprintf(format, args...))
}

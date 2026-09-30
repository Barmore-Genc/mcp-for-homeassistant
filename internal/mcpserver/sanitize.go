package mcpserver

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
)

// Camera and media player states carry an access token in access_token and in
// the entity_picture URL. It opens the camera stream and proxy to anyone who
// holds it, without the admin token, so it must never reach the model or
// anything the model writes elsewhere. The other patterns catch credentials
// that integrations and HA itself put in log lines, event data and traces.
var (
	secretParam = regexp.MustCompile(`(?i)((?:^|[?&;\s,(])(?:[a-z0-9_.-]*(?:token|key|password|passwd|secret|signature)[a-z0-9_.-]*|[a-z0-9_.-]*sig)=)[^&\s"'<>,;)]+`)
	authHeader  = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"',;}]+`)
	bearerToken = regexp.MustCompile(`(?i)(\bbearer\s+)[a-z0-9._~+/=-]{8,}`)
	jwtLike     = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)
)

// redactSecrets replaces credentials in free text with REDACTED.
func redactSecrets(s string) string {
	s = authHeader.ReplaceAllString(s, "${1}REDACTED")
	s = bearerToken.ReplaceAllString(s, "${1}REDACTED")
	s = jwtLike.ReplaceAllString(s, "REDACTED")
	return secretParam.ReplaceAllString(s, "${1}REDACTED")
}

// sanitizeValue drops camera access tokens from a decoded HA value and
// redacts credentials in every string it holds.
func sanitizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "access_token" {
				continue
			}
			if str, ok := val.(string); ok && strings.HasPrefix(k, "entity_picture") && redactSecrets(str) != str {
				continue
			}
			out[k] = sanitizeValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeValue(val)
		}
		return out
	case string:
		return redactSecrets(t)
	default:
		return v
	}
}

// redactAccessTokens replaces the current access token of every entity in s.
// A template or a trace variable can hold a token on its own, where no pattern
// recognizes it.
func redactAccessTokens(s string, states []homeassistant.State) string {
	for _, st := range states {
		if tok, ok := st.Attributes["access_token"].(string); ok && len(tok) >= 8 {
			s = strings.ReplaceAll(s, tok, "REDACTED")
		}
	}
	return s
}

func sanitizeAttrs(attrs map[string]any) map[string]any {
	if attrs == nil {
		return nil
	}
	return sanitizeValue(attrs).(map[string]any)
}

// sanitizeJSON returns sanitized compact JSON, or the input redacted as text
// when it is not JSON.
func sanitizeJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return redactSecrets(string(raw))
	}
	b, _ := json.Marshal(sanitizeValue(v))
	return string(b)
}

// oneLine collapses line breaks and other control characters into spaces.
// Names, titles and messages from HA are set by integrations, devices and
// anyone who can edit the HA config; without this, a name with a newline could
// print a line that reads like the tool's own output.
func oneLine(s string) string {
	if strings.IndexFunc(s, isBreak) < 0 {
		return s
	}
	return strings.Join(strings.FieldsFunc(s, isBreak), " ")
}

func isBreak(r rune) bool {
	return unicode.IsControl(r) || r == ' ' || r == ' '
}

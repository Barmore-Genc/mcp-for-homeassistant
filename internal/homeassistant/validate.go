package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	slugRe       = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)
	entityIDRe   = regexp.MustCompile(`^[a-z0-9_]{1,64}\.[a-z0-9_]{1,255}$`)
	configIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,254}$`)
	registryIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,255}$`)
	urlPathRe    = regexp.MustCompile(`^[a-z0-9_-]{1,128}$`)
	eventTypeRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,255}$`)
	blueprintSeg = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,127}$`)
)

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// ValidateEntityID checks that id looks like "domain.object_id".
func ValidateEntityID(id string) error {
	if !entityIDRe.MatchString(id) {
		return invalidArg("entity id %q must look like domain.object_id (lowercase letters, digits, underscores)", id)
	}
	return nil
}

func validateSlug(kind, s string) error {
	if !slugRe.MatchString(s) {
		return invalidArg("%s %q must contain only lowercase letters, digits and underscores", kind, s)
	}
	return nil
}

// validateConfigID accepts the ids HA generates for automations and scenes
// (numeric timestamps, uuids) and hand-written YAML ids, while keeping "..",
// "/", "?", "#" and "%" out of URL paths.
func validateConfigID(kind, id string) error {
	if !configIDRe.MatchString(id) || strings.Contains(id, "..") {
		return invalidArg("%s id %q may contain only letters, digits, '_', '-' and '.'", kind, id)
	}
	return nil
}

// validateRegistryID is for ids sent in WebSocket JSON bodies, where there is
// no path injection risk; it rejects empty and oversized values.
func validateRegistryID(kind, id string) error {
	if !registryIDRe.MatchString(id) {
		return invalidArg("%s id %q is not valid", kind, id)
	}
	return nil
}

func validateDashboardURLPath(p string) error {
	if !urlPathRe.MatchString(p) {
		return invalidArg("dashboard url path %q may contain only lowercase letters, digits, '-' and '_'", p)
	}
	return nil
}

func validateEventType(t string) error {
	if !eventTypeRe.MatchString(t) {
		return invalidArg("event type %q is not valid", t)
	}
	return nil
}

func validateBlueprintDomain(d string) error {
	if d != "automation" && d != "script" {
		return invalidArg("blueprint domain must be automation or script, got %q", d)
	}
	return nil
}

// validateBlueprintPath keeps blueprint file operations inside the domain's
// blueprint folder.
func validateBlueprintPath(p string) error {
	if p == "" || len(p) > 255 {
		return invalidArg("blueprint path must be 1-255 characters")
	}
	for _, seg := range strings.Split(p, "/") {
		if !blueprintSeg.MatchString(seg) || strings.Contains(seg, "..") {
			return invalidArg("blueprint path %q must be relative, without '..', using letters, digits, '_', '-' and '.'", p)
		}
	}
	return nil
}

// validateBlueprintURL limits imports to public https URLs. HA fetches the URL
// itself, so an unrestricted value would let a caller probe HA's network.
func validateBlueprintURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return invalidArg("blueprint URL must be an https URL")
	}
	if p := u.Port(); p != "" && p != "443" {
		return invalidArg("blueprint URL must use the default https port")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if net.ParseIP(host) != nil || numericHost(host) {
		return invalidArg("blueprint URL must use a host name, not an IP address")
	}
	if !strings.Contains(host, ".") || host == "localhost" {
		return invalidArg("blueprint URL must point to a public host")
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa", ".home", ".corp", ".intranet", ".private"} {
		if strings.HasSuffix(host, suffix) {
			return invalidArg("blueprint URL must point to a public host")
		}
	}
	return nil
}

// numericHost reports whether the last label is a number. No TLD is numeric,
// and resolvers read such names as IPv4 shorthand ("127.1", "0x7f.1").
func numericHost(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	digits, base := last, "0123456789"
	if h, ok := strings.CutPrefix(last, "0x"); ok {
		digits, base = h, "0123456789abcdef"
	}
	return last != "" && strings.Trim(digits, base) == ""
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// checkBlueprintHost resolves the import URL's host and refuses addresses on
// HA's own network. HA resolves the name again when it fetches and follows
// redirects on its own, so this narrows what a caller can reach rather than
// closing it off.
func (c *Client) checkBlueprintHost(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return invalidArg("blueprint URL must be an https URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := c.lookupHost(ctx, u.Hostname())
	if err != nil {
		return invalidArg("blueprint URL host %s does not resolve: %v", u.Hostname(), err)
	}
	for _, a := range addrs {
		a = a.Unmap()
		if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
			a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() || cgnat.Contains(a) {
			return invalidArg("blueprint URL host %s resolves to %s, which is not a public address", u.Hostname(), a)
		}
	}
	return nil
}

func isJSONObject(b []byte) bool {
	t := bytes.TrimLeft(b, " \t\r\n")
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

// rejectReservedKeys stops caller-supplied maps from overriding the WebSocket
// envelope fields.
func rejectReservedKeys(m map[string]any) error {
	for _, k := range []string{"id", "type"} {
		if _, ok := m[k]; ok {
			return invalidArg("field %q is reserved", k)
		}
	}
	return nil
}

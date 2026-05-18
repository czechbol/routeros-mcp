package server

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
)

const redactionMask = "[REDACTED]"

// Default set of RouterOS field names whose values are always sensitive.
// Match is case-insensitive on the field name; values are replaced wholesale.
var defaultRedactFields = []string{
	"api-key",
	"auth-key",
	"authentication-password",
	"challenge-passphrase",
	"challenge-password",
	"community",
	"confirm-new-password",
	"eab-hmac-key",
	"eab-key-b64",
	"eap-password",
	"enc-key",
	"encryption-password",
	"export-passphrase",
	"ipsec-secret",
	"key-passphrase",
	"key-val",
	"lacp-user-key",
	"mac-auth-password",
	"new-password",
	"old-password",
	"otp",
	"otp-secret",
	"ovpn-password",
	"passphrase",
	"passwd",
	"password",
	"ppk-secret",
	"pre-shared-key",
	"preshared-key",
	"private-key",
	"private-key-passphrase",
	"psk",
	"radius-password",
	"radius-secret",
	"recovery-passphrase",
	"remote-key",
	"secret",
	"secret-download-key",
	"secrets",
	"security.passphrase",
	"shared-secret",
	"smb-password",
	"smb-server-password",
	"snmp-community",
	"socks5-password",
	"sshfs-password",
	"tcp-md5-key",
	"token",
	"trap-community",
	"wpa-pre-shared-key",
	"wpa2-pre-shared-key",
}

// defaultPathSensitiveFields maps a normalised RouterOS REST path prefix to
// the list of additional field names that hold sensitive values on that
// menu but cannot be globally allowlisted because the same field name is
// non-sensitive elsewhere (e.g. `name` is the SNMP community string on
// /snmp/community but the harmless identifier almost everywhere else).
// Match is case-insensitive on both path and field name, and applies to
// any path whose first N segments equal the key.
var defaultPathSensitiveFields = map[string][]string{
	"snmp/community": {"name"},
}

var (
	redactInit      sync.Once
	redactMu        sync.RWMutex
	redactSet       map[string]struct{}
	redactStringPat *regexp.Regexp
	redactPathSet   map[string]map[string]struct{}
	redactOn        bool
)

// resetRedactor reloads configuration from the environment. Test-only;
// kept unexported so production code cannot reset state mid-run.
func resetRedactor() {
	redactMu.Lock()
	defer redactMu.Unlock()
	redactInit = sync.Once{}
	redactSet = nil
	redactStringPat = nil
	redactPathSet = nil
	redactOn = false
}

func initRedactor() {
	redactInit.Do(func() {
		redactOn = os.Getenv("REDACT") != "0"
		redactSet = make(map[string]struct{}, len(defaultRedactFields))
		for _, f := range defaultRedactFields {
			redactSet[strings.ToLower(f)] = struct{}{}
		}
		extra := os.Getenv("REDACT_EXTRA")
		if extra != "" {
			for name := range strings.SplitSeq(extra, ",") {
				name = strings.TrimSpace(strings.ToLower(name))
				if name != "" {
					redactSet[name] = struct{}{}
				}
			}
		}
		redactStringPat = buildRedactStringPattern(redactSet)
		redactPathSet = make(map[string]map[string]struct{}, len(defaultPathSensitiveFields))
		for path, fields := range defaultPathSensitiveFields {
			set := make(map[string]struct{}, len(fields))
			for _, f := range fields {
				set[strings.ToLower(f)] = struct{}{}
			}
			redactPathSet[normalisePath(path)] = set
		}
	})
}

// normalisePath lowercases and trims a RouterOS REST path so per-path
// override lookup is direction- and case-insensitive.
func normalisePath(p string) string {
	return strings.ToLower(strings.Trim(p, "/"))
}

// pathSensitiveFields returns the override set for restPath, or nil. A path
// matches if any registered prefix equals one of its segment prefixes — so
// `/snmp/community/*0` inherits the `/snmp/community` override.
func pathSensitiveFields(restPath string) map[string]struct{} {
	clean := normalisePath(restPath)
	if clean == "" || len(redactPathSet) == 0 {
		return nil
	}
	for {
		if set, ok := redactPathSet[clean]; ok {
			return set
		}
		i := strings.LastIndex(clean, "/")
		if i < 0 {
			return nil
		}
		clean = clean[:i]
	}
}

// buildRedactStringPattern compiles a single regex that finds sensitive
// keys followed by `:` or `=` and a quoted-or-bare value. It runs over
// free-form strings (error bodies, plain-text 4xx payloads) where the
// JSON-walk redactor cannot reach.
func buildRedactStringPattern(keys map[string]struct{}) *regexp.Regexp {
	if len(keys) == 0 {
		return nil
	}
	alts := make([]string, 0, len(keys))
	for k := range keys {
		alts = append(alts, regexp.QuoteMeta(k))
	}
	// Groups: (1) optional opening quote around key, (2) key, (3) matching
	// closing quote, (4) delim+ws, (5) value (quoted or bare up to a
	// delimiter that ends a value in JSON/url-encoded/k=v contexts).
	src := `(?i)(["']?)(` + strings.Join(alts, "|") +
		`)(["']?)(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;}&"]+)`
	return regexp.MustCompile(src)
}

// RedactionEnabled reports whether the redactor will modify values.
func RedactionEnabled() bool {
	initRedactor()
	return redactOn
}

// Redact walks a decoded JSON value and replaces sensitive field values
// with a fixed mask. It mutates and returns the same value for convenience.
// Disabled when REDACT=0.
func Redact(v any) any {
	initRedactor()
	if !redactOn {
		return v
	}
	return redactValue(v, nil)
}

// RedactPath is like Redact but additionally masks fields that are only
// sensitive on restPath (see defaultPathSensitiveFields).
func RedactPath(restPath string, v any) any {
	initRedactor()
	if !redactOn {
		return v
	}
	return redactValue(v, pathSensitiveFields(restPath))
}

func redactValue(v any, extras map[string]struct{}) any {
	switch x := v.(type) {
	case map[string]any:
		return redactMap(x, extras)
	case []any:
		for i, child := range x {
			x[i] = redactValue(child, extras)
		}
		return x
	case string:
		return redactStringValue(x, extras)
	default:
		return v
	}
}

func redactMap(m map[string]any, extras map[string]struct{}) map[string]any {
	for k, child := range m {
		if isSensitiveKey(k) || isExtraSensitive(k, extras) {
			m[k] = redactionMask
			continue
		}
		m[k] = redactValue(child, extras)
	}
	return m
}

// redactStringValue scrubs a free-form string. RouterOS error bodies
// sometimes echo submitted creds verbatim; try a JSON decode first so
// structured payloads stay structured, then fall back to pattern scrub.
func redactStringValue(s string, extras map[string]struct{}) string {
	if obj, ok := tryParseJSONString(s); ok {
		redacted := redactValue(obj, extras)
		if b, err := json.Marshal(redacted); err == nil {
			return string(b)
		}
	}
	return redactString(s)
}

func isExtraSensitive(k string, extras map[string]struct{}) bool {
	if extras == nil {
		return false
	}
	_, ok := extras[strings.ToLower(k)]
	return ok
}

func tryParseJSONString(s string) (any, bool) {
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return v, true
}

// RedactString scrubs sensitive `key=value` and `"key":"value"` patterns
// from free-form text. Honors REDACT=0.
func RedactString(s string) string {
	initRedactor()
	if !redactOn {
		return s
	}
	return redactString(s)
}

func redactString(s string) string {
	if redactStringPat == nil {
		return s
	}
	return redactStringPat.ReplaceAllStringFunc(s, func(m string) string {
		sub := redactStringPat.FindStringSubmatch(m)
		if len(sub) < 6 {
			return m
		}
		val := sub[5]
		var newVal string
		switch {
		case strings.HasPrefix(val, `"`):
			newVal = `"` + redactionMask + `"`
		case strings.HasPrefix(val, `'`):
			newVal = `'` + redactionMask + `'`
		default:
			newVal = redactionMask
		}
		return sub[1] + sub[2] + sub[3] + sub[4] + newVal
	})
}

func isSensitiveKey(k string) bool {
	_, ok := redactSet[strings.ToLower(k)]
	return ok
}

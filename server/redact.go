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
	"password",
	"passwd",
	"secret",
	"pre-shared-key",
	"psk",
	"private-key",
	"private-key-passphrase",
	"community",
	"snmp-community",
	"api-key",
	"token",
	"otp",
	"recovery-passphrase",
	"radius-secret",
	"shared-secret",
	"wpa-pre-shared-key",
	"wpa2-pre-shared-key",
}

var (
	redactInit      sync.Once
	redactMu        sync.RWMutex
	redactSet       map[string]struct{}
	redactStringPat *regexp.Regexp
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
	})
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
	return redactValue(v)
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if isSensitiveKey(k) {
				x[k] = redactionMask
				continue
			}
			x[k] = redactValue(child)
		}
		return x
	case []any:
		for i, child := range x {
			x[i] = redactValue(child)
		}
		return x
	case string:
		// Free-form strings can carry sensitive keys (RouterOS error
		// bodies sometimes echo submitted creds in plain text). Try a
		// JSON decode first so structured payloads stay structured; fall
		// back to a pattern scrub for everything else.
		if obj, ok := tryParseJSONString(x); ok {
			redacted := redactValue(obj)
			if b, err := json.Marshal(redacted); err == nil {
				return string(b)
			}
		}
		return redactString(x)
	default:
		return v
	}
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

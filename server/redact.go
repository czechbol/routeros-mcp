package server

import (
	"os"
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
	redactInit sync.Once
	redactMu   sync.RWMutex
	redactSet  map[string]struct{}
	redactOn   bool
)

// resetRedactor reloads configuration from the environment. Exported only via
// the unexported name so it stays test-only.
func resetRedactor() {
	redactMu.Lock()
	defer redactMu.Unlock()
	redactInit = sync.Once{}
	redactSet = nil
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
		if extra == "" {
			return
		}
		for _, name := range strings.Split(extra, ",") {
			name = strings.TrimSpace(strings.ToLower(name))
			if name != "" {
				redactSet[name] = struct{}{}
			}
		}
	})
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
	default:
		return v
	}
}

func isSensitiveKey(k string) bool {
	_, ok := redactSet[strings.ToLower(k)]
	return ok
}

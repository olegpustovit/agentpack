package obs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var normalizers = []*regexp.Regexp{
	regexp.MustCompile("\"[^\"]*\"|'[^']*'|`[^`]*`"),
	regexp.MustCompile(`(?:[A-Za-z]:[\\/]|/)[^\s,;]+`),
	regexp.MustCompile(`\b(?:session|node|trace|request|worker|operation)[_-][A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{8,}(?:-[0-9a-f]+)*\b`),
	regexp.MustCompile(`\b\d+(?:\.\d+)?\b`),
}

func normalizeMessage(value string) string {
	for _, pattern := range normalizers {
		value = pattern.ReplaceAllString(value, "<value>")
	}
	return value
}

func Fingerprint(event Event) string {
	var key strings.Builder
	key.WriteString(event.Kind)
	if event.Kind == "ui.error" {
		fmt.Fprintf(&key, "\n%v\n%s\n%v", event.Attrs["error_name"], normalizeMessage(fmt.Sprint(event.Attrs["message"])), event.Attrs["route"])
	} else {
		if chain, ok := event.Attrs["errors"].([]any); ok {
			for _, item := range chain {
				if err, ok := item.(map[string]any); ok {
					fmt.Fprintf(&key, "\n%v\n%s", err["type"], normalizeMessage(fmt.Sprint(err["message"])))
				}
			}
		} else {
			key.WriteString(normalizeMessage(fmt.Sprint(event.Attrs["message"])))
		}
		count := 0
		for _, function := range strings.Split(event.Stack, "\n") {
			if strings.HasPrefix(function, "github.com/olegpustovit/agentpack/") && !strings.Contains(function, "/internal/obs.") {
				key.WriteString("\n" + function)
				count++
				if count == 5 {
					break
				}
			}
		}
	}
	digest := sha256.Sum256([]byte(key.String()))
	return hex.EncodeToString(digest[:])
}

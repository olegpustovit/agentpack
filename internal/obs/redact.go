package obs

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxEventBytes = 32 << 10

var (
	credentialKey   = regexp.MustCompile(`(?i)(token|secret|password|passwd|(?:api|private|client)[_-]?key|authorization|credential|auth|signature|sig)`)
	credentialValue = regexp.MustCompile(`(?i)(<local>|bearer\s+[^\s,;]+|basic\s+[^\s,;]+|(?:token|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|authorization)\s*[=:]\s*[^\s,;]+|://[^/@\s]+:[^/@\s]+@|\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,})\b)`)
)

func ascii(value string) bool {
	for i := range len(value) {
		if value[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func CredentialKey(key string) bool {
	// Keep the regex's Unicode folding; hot-path metadata keys are ASCII.
	if !ascii(key) {
		return credentialKey.MatchString(key)
	}
	key = strings.ToLower(key)
	for _, part := range []string{"token", "secret", "password", "passwd", "auth", "credential", "sig", "apikey", "api_key", "api-key", "privatekey", "private_key", "private-key", "clientkey", "client_key", "client-key"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

// CredentialShaped is shared with Library capture; keep one credential policy.
func CredentialShaped(value string) bool {
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if (!ascii(value) || strings.ContainsAny(value, "<:= \t\r\n\f") || strings.Contains(lower, "gh") || strings.Contains(lower, "github") || strings.Contains(lower, "sk-") || strings.Contains(lower, "xox")) && credentialValue.MatchString(value) {
		return true
	}
	if !strings.ContainsAny(value, "?@") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for key := range parsed.Query() {
		key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
		if key == "key" || CredentialKey(key) {
			return true
		}
	}
	return false
}

func boundedString(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.Clone(value)
}

func redactString(value string) string {
	if CredentialShaped(value) {
		return "[redacted]"
	}
	return boundedString(value, 4096)
}

// Redact accepts JSON-shaped attributes. Unknown types are omitted rather than
// invoking arbitrary String/MarshalJSON methods that might reveal credentials.
func Redact(value any) any {
	budget := MaxEventBytes / 2
	return redactValue(value, 0, &budget)
}

func redactValue(value any, depth int, budget *int) any {
	if depth >= 6 || *budget <= 0 {
		return "[truncated]"
	}
	*budget -= 8
	switch v := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case string:
		result := boundedString(redactString(v), max(0, *budget))
		*budget -= len(result)
		return result
	case http.Header:
		result := make(map[string]any)
		for _, key := range []string{"Content-Type", "Content-Length", "Accept", "Traceparent", "X-Agentpack-Trace"} {
			if values := v.Values(key); len(values) != 0 {
				result[key] = redactValue(values, depth+1, budget)
			}
		}
		return result
	case map[string]any:
		result := make(map[string]any)
		for key, child := range v {
			if *budget <= 0 {
				result["truncated"] = true
				break
			}
			lower := strings.ToLower(key)
			if CredentialKey(key) || CredentialShaped(key) || lower == "cookie" || lower == "set-cookie" || lower == "env" || lower == "environment" || lower == "prompt" || lower == "reply" || lower == "body" || lower == "request_body" || lower == "response_body" {
				continue
			}
			key = boundedString(key, 128)
			*budget -= len(key)
			if lower == "headers" {
				switch headers := child.(type) {
				case map[string]any:
					allowed := http.Header{}
					for _, name := range []string{"Content-Type", "Content-Length", "Accept", "Traceparent", "X-Agentpack-Trace"} {
						for k, raw := range headers {
							if strings.EqualFold(k, name) {
								switch raw := raw.(type) {
								case string:
									allowed.Set(name, raw)
								case []string:
									allowed[name] = raw
								}
							}
						}
					}
					child = allowed
				case map[string]string:
					allowed := http.Header{}
					for k, v := range headers {
						allowed.Set(k, v)
					}
					child = allowed
				case http.Header:
				default:
					continue
				}
			}
			result[key] = redactValue(child, depth+1, budget)
		}
		return result
	case []any:
		result := make([]any, 0, min(len(v), 128))
		for _, child := range v {
			if *budget <= 0 {
				break
			}
			result = append(result, redactValue(child, depth+1, budget))
		}
		return result
	case []string:
		result := make([]any, 0, min(len(v), 128))
		for _, child := range v {
			if *budget <= 0 {
				break
			}
			result = append(result, redactValue(child, depth+1, budget))
		}
		return result
	default:
		return "[omitted]"
	}
}

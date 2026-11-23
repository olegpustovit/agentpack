package obs

import "testing"

func TestRedactMetadataAndPayloads(t *testing.T) {
	for _, test := range []struct {
		key, value string
		want       any
	}{
		{"signal", "TERM", "TERM"}, {"author", "test", "test"}, {"design", "test", "test"},
		{"clientSecret", "test", nil}, {"access_token", "test", nil},
		{"args", "test", nil}, {"arguments", "test", nil}, {"output", "test", nil},
		{"result", "test", nil}, {"text", "test", nil}, {"content", "test", nil}, {"stderr", "test", nil},
		{"message", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc123", "[redacted]"},
		{"message", "sk-ant-test1234567890", "[redacted]"},
	} {
		t.Run(test.key+"/"+test.value, func(t *testing.T) {
			got := Redact(map[string]any{test.key: test.value}).(map[string]any)[test.key]
			if got != test.want {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

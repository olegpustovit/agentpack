package codex

import (
	"testing"
	"time"
)

func TestCredentialValidityAndFreshness(t *testing.T) {
	refreshed := time.Date(2026, 1, 2, 3, 4, 5, 600, time.UTC)
	for data, want := range map[string]struct {
		valid bool
		fresh time.Time
	}{
		`{"tokens":{"access_token":"a"},"last_refresh":"2026-01-02T03:04:05.0000006Z"}`: {true, refreshed},
		`{"OPENAI_API_KEY":"k"}`:                  {true, time.Time{}},
		`{"OPENAI_API_KEY":null,"tokens":null}`:   {false, time.Time{}},
		`{"last_refresh":"2026-01-02T03:04:05Z"}`: {false, refreshed.Truncate(time.Second)},
		`{}`:       {false, time.Time{}},
		`not json`: {false, time.Time{}},
	} {
		if got := ValidCredential([]byte(data)); got != want.valid {
			t.Errorf("valid(%s) = %t", data, got)
		}
		if got := Freshness([]byte(data)); !got.Equal(want.fresh) {
			t.Errorf("freshness(%s) = %v", data, got)
		}
	}
}

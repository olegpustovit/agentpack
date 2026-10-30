package harness

import "time"

// LimitStateAvailable means the harness reported at least one usage window.
const LimitStateAvailable = "available"

// LimitStateUnsupported means no usage probe exists for this harness on this node.
const LimitStateUnsupported = "unsupported"

// LimitStateError means the probe ran but could not produce usage windows.
const LimitStateError = "error"

// LimitSnapshot is the sanitized usage observation for one harness on one node.
// Absent numbers are unknown; a measured zero is a real zero.
type LimitSnapshot struct {
	Harness    string        `json:"harness"`
	State      string        `json:"state"`
	Source     string        `json:"source,omitempty"`
	ObservedAt time.Time     `json:"observed_at"`
	Plan       string        `json:"plan,omitempty"`
	Error      string        `json:"error,omitempty"`
	Windows    []LimitWindow `json:"windows"`
}

// LimitWindow is one usage window reported by a harness. Provider IDs and scope are kept
// rather than assigning a generic meaning such as "primary = five-hour".
type LimitWindow struct {
	ID               string     `json:"id"`
	Label            string     `json:"label"`
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	DurationSeconds  *int64     `json:"duration_seconds,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	ResetText        string     `json:"reset_text,omitempty"`
}

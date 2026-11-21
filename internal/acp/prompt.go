package acp

import "context"

type acceptedKey struct{}

// WithAccepted returns a context whose prompt implementation calls accept once the prompt is durably sent.
func WithAccepted(ctx context.Context, accept func()) context.Context {
	return context.WithValue(ctx, acceptedKey{}, accept)
}

// Accepted reports that the prompt carried by ctx was logged and is being sent to the agent.
func Accepted(ctx context.Context) {
	if accept, ok := ctx.Value(acceptedKey{}).(func()); ok {
		accept()
	}
}

// PromptImage references an image blob stored under the worker session directory.
type PromptImage struct {
	Sha           string
	Name          string
	Mime          string
	Kind          string
	Width, Height int
}

// PromptResourceLink references a stored file that an agent can read by URI.
type PromptResourceLink struct {
	Sha  string
	Name string
}

// PromptInput is a user message sent to the agent.
type PromptInput struct {
	QueueID      string
	Text         string
	Image        *PromptImage
	Images       []PromptImage
	ResourceLink *PromptResourceLink
}

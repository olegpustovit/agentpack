package acp

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
	Text         string
	Image        *PromptImage
	Images       []PromptImage
	ResourceLink *PromptResourceLink
}

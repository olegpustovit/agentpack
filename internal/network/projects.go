package network

// ProjectContents is one folder's pack.lock as a node reports it.
type ProjectContents struct {
	Packages []ProjectPackage `json:"packages"`
	MCP      []ProjectMCP     `json:"mcp"`
	Modes    []ProjectMode    `json:"modes"`
}

// ProjectMode names one mode and what it turns off; selectors themselves stay on the node.
type ProjectMode struct {
	Name            string   `json:"name"`
	OffPackages     []string `json:"off_packages"`
	PartialPackages []string `json:"partial_packages"`
	OffMCP          []string `json:"off_mcp"`
}

type ProjectPackage struct {
	Module      string `json:"module"`
	Commit      string `json:"commit"`
	ContentHash string `json:"content_hash"`
	CacheKey    string `json:"cache_key"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Direct      bool   `json:"direct"`
}

type ProjectMCP struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Launcher  string `json:"launcher"`
	Status    string `json:"status"`
	Package   string `json:"package"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	Host      string `json:"host"`
	Reason    string `json:"reason"`
	// URL is set for a remote server that a shared MCP login can serve (mcp.LoginURL).
	URL string `json:"url,omitempty"`
	// Login is the credential ID this project picked for URL; empty means the first login.
	Login string `json:"login,omitempty"`
}

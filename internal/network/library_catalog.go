package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/github"
	"github.com/olegpustovit/agentpack/internal/obs"
)

const (
	catalogTTL         = 24 * time.Hour
	marketplaceOwner   = "anthropics"
	marketplaceRepo    = "claude-plugins-official"
	marketplacePath    = ".claude-plugin/marketplace.json"
	mcpRegistryServers = "https://registry.modelcontextprotocol.io/v0/servers"
)

// CatalogItem is one installable entry from a trusted marketplace or the MCP Registry.
// Plugins carry an add Spec; MCP servers carry the `agentpack mcp add` Command instead,
// because the gateway has no MCP install route.
type CatalogItem struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Source      string `json:"source"`
	Spec        string `json:"spec,omitempty"`
	Command     string `json:"command,omitempty"`
	URL         string `json:"url,omitempty"`
}

type CatalogSource struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

type Catalog struct {
	Items   []CatalogItem   `json:"items"`
	Sources []CatalogSource `json:"sources"`
}

// catalog fetches the two Discover sources. The marketplace file is cached in index.db by
// the GitHub client; registry pages are cached here per search for 24 h.
type catalog struct {
	marketplace func(context.Context) ([]byte, error)
	registry    string
	client      *http.Client
	now         func() time.Time

	mu    sync.Mutex
	pages map[string]catalogPage
}

type catalogPage struct {
	items   []CatalogItem
	fetched time.Time
}

var libraryCatalog = &catalog{
	marketplace: func(ctx context.Context) ([]byte, error) {
		return github.RepoFile(ctx, nil, marketplaceOwner, marketplaceRepo, marketplacePath, catalogTTL)
	},
	registry: mcpRegistryServers,
	client:   &http.Client{Timeout: 20 * time.Second},
	now:      time.Now,
}

func (c *catalog) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result Catalog
	plugins, err := c.plugins(ctx)
	result.Sources = append(result.Sources, catalogSource("claude-plugins-official", len(plugins), err))
	servers, serverErr := c.servers(ctx, strings.TrimSpace(r.URL.Query().Get("q")))
	result.Sources = append(result.Sources, catalogSource("MCP Registry", len(servers), serverErr))
	// One source failing still shows the other; each failure is recorded and named in its source.
	obs.From(r.Context()).Fail(err)
	obs.From(r.Context()).Fail(serverErr)
	result.Items = append(append(make([]CatalogItem, 0, len(plugins)+len(servers)), plugins...), servers...)
	writeJSON(w, result)
}

func catalogSource(name string, count int, err error) CatalogSource {
	source := CatalogSource{Name: name, Count: count}
	if err != nil {
		source.Error = err.Error()
	}
	return source
}

type marketplacePlugin struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Homepage    string                `json:"homepage"`
	Author      struct{ Name string } `json:"author"`
	Source      json.RawMessage       `json:"source"`
}

func (c *catalog) plugins(ctx context.Context) ([]CatalogItem, error) {
	data, err := c.marketplace(ctx)
	if err != nil {
		return nil, err
	}
	var file struct {
		Plugins []marketplacePlugin `json:"plugins"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("read %s/%s marketplace: %w", marketplaceOwner, marketplaceRepo, err)
	}
	items := make([]CatalogItem, 0, len(file.Plugins))
	for _, plugin := range file.Plugins {
		spec := marketplaceSpec(plugin.Source)
		if spec == "" || plugin.Name == "" {
			continue
		}
		items = append(items, CatalogItem{Kind: "plugin", Name: plugin.Name, Description: plugin.Description, Publisher: plugin.Author.Name, Source: "claude-plugins-official", Spec: spec, URL: plugin.Homepage})
	}
	return items, nil
}

// marketplaceSpec turns a marketplace source into an `add` spec, pinned to the marketplace's
// commit when it names one. Sources outside GitHub cannot be added and are left out.
func marketplaceSpec(raw json.RawMessage) string {
	var local string
	if json.Unmarshal(raw, &local) == nil {
		clean := path.Clean(strings.TrimPrefix(local, "./"))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return ""
		}
		return github.Host + "/" + marketplaceOwner + "/" + marketplaceRepo + "/" + clean
	}
	var remote struct {
		Source string `json:"source"`
		URL    string `json:"url"`
		Repo   string `json:"repo"`
		Path   string `json:"path"`
		Ref    string `json:"ref"`
		SHA    string `json:"sha"`
	}
	if json.Unmarshal(raw, &remote) != nil {
		return ""
	}
	repo := remote.Repo
	if remote.URL != "" {
		parsed, err := url.Parse(remote.URL)
		if err != nil || parsed.Host != github.Host {
			return ""
		}
		repo = strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	}
	if strings.Count(repo, "/") != 1 {
		return ""
	}
	spec := github.Host + "/" + repo
	if clean := strings.Trim(path.Clean("/"+remote.Path), "/"); clean != "" {
		spec += "/" + clean
	}
	if ref := firstNonEmpty(remote.SHA, remote.Ref); ref != "" {
		spec += "@" + ref
	}
	return spec
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type registryServer struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebsiteURL  string `json:"websiteUrl"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Packages []struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		Transport    struct {
			Type string `json:"type"`
		} `json:"transport"`
	} `json:"packages"`
	Remotes []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"remotes"`
}

// servers returns one registry page, the newest version of each server, matching the search.
// shortcut: one page of 100 without a search; follow nextCursor if people browse past it.
func (c *catalog) servers(ctx context.Context, search string) ([]CatalogItem, error) {
	c.mu.Lock()
	cached, ok := c.pages[search]
	c.mu.Unlock()
	if ok && c.now().Sub(cached.fetched) < catalogTTL {
		return cached.items, nil
	}
	query := url.Values{"limit": {"100"}, "version": {"latest"}}
	if search != "" {
		query.Set("search", search)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.registry+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return staleOr(cached, ok, fmt.Errorf("MCP Registry: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return staleOr(cached, ok, fmt.Errorf("MCP Registry: %s", response.Status))
	}
	var body struct {
		Servers []struct {
			Server registryServer `json:"server"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 8<<20)).Decode(&body); err != nil {
		return staleOr(cached, ok, fmt.Errorf("MCP Registry: %w", err))
	}
	items := make([]CatalogItem, 0, len(body.Servers))
	for _, entry := range body.Servers {
		if item, ok := registryItem(entry.Server); ok {
			items = append(items, item)
		}
	}
	c.mu.Lock()
	// shortcut: a full map is emptied rather than evicted by age; fine for one person's searches.
	if c.pages == nil || len(c.pages) >= 64 {
		c.pages = map[string]catalogPage{}
	}
	c.pages[search] = catalogPage{items: items, fetched: c.now()}
	c.mu.Unlock()
	return items, nil
}

func staleOr(cached catalogPage, ok bool, err error) ([]CatalogItem, error) {
	if ok {
		return cached.items, nil
	}
	return nil, err
}

// registryItem builds the `agentpack mcp add` command for the first launcher agentpack can run.
func registryItem(server registryServer) (CatalogItem, bool) {
	short := server.Name[strings.LastIndex(server.Name, "/")+1:]
	if short == "" {
		return CatalogItem{}, false
	}
	publisher, _, _ := strings.Cut(server.Name, "/")
	command := ""
	for _, pkg := range server.Packages {
		runner := map[string]string{"npm": "npx --args -y", "pypi": "uvx --args"}[pkg.RegistryType]
		if runner != "" && pkg.Transport.Type == "stdio" && pkg.Identifier != "" {
			command = fmt.Sprintf("agentpack mcp add %s --command %s %s", shellWord(short), runner, shellWord(pkg.Identifier))
			break
		}
	}
	if command == "" {
		for _, remote := range server.Remotes {
			kind := map[string]string{"streamable-http": "http", "sse": "sse"}[remote.Type]
			if kind != "" && remote.URL != "" && !strings.Contains(remote.URL, "{") {
				command = fmt.Sprintf("agentpack mcp add %s --url %s --type %s", shellWord(short), shellWord(remote.URL), kind)
				break
			}
		}
	}
	if command == "" {
		return CatalogItem{}, false
	}
	return CatalogItem{Kind: "mcp", Name: short, Description: server.Description, Publisher: publisher, Source: "MCP Registry", Command: command, URL: firstNonEmpty(server.WebsiteURL, server.Repository.URL)}, true
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9@/._:+-]+$`)

// shellWord quotes registry text for the copied command, so a URL's & or ? stays one argument.
func shellWord(value string) string {
	if plainShellWord.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

package tools

import (
	"strings"

	"github.com/rendicott/marble/internal/mcp"
)

// CatalogEntry is one tool available to the agent (native or MCP).
type CatalogEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"` // native | mcp
	Server      string `json:"server,omitempty"`
	Description string `json:"description,omitempty"`
}

// Catalog returns native built-ins plus discovered MCP tools (stable order).
func (r *Registry) Catalog() []CatalogEntry {
	out := NativeCatalog()
	if r != nil && r.MCP != nil {
		out = append(out, MCPCatalog(r.MCP)...)
	}
	return out
}

// NativeCatalog lists built-in harness tools.
func NativeCatalog() []CatalogEntry {
	specs := allSpecs()
	out := make([]CatalogEntry, 0, len(specs))
	for _, s := range specs {
		name := s.Function.Name
		desc := s.Function.Description
		if len(desc) > 160 {
			desc = desc[:157] + "…"
		}
		out = append(out, CatalogEntry{
			Name:        name,
			Source:      "native",
			Description: desc,
		})
	}
	return out
}

// MCPCatalog lists MCP-exposed tools with server attribution.
func MCPCatalog(m *mcp.Manager) []CatalogEntry {
	if m == nil {
		return nil
	}
	raw := m.ToolCatalog()
	out := make([]CatalogEntry, 0, len(raw))
	for _, t := range raw {
		desc := t.Description
		if len(desc) > 160 {
			desc = desc[:157] + "…"
		}
		out = append(out, CatalogEntry{
			Name:        t.Name,
			Source:      "mcp",
			Server:      t.Server,
			Description: desc,
		})
	}
	return out
}

// IsMCPName reports whether a tool name is MCP-namespaced.
func IsMCPName(name string) bool {
	return strings.HasPrefix(name, "mcp_")
}

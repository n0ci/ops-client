package ops

import (
	"context"
	"fmt"
	"sort"

	"github.com/n0ci/ops-client/internal/yamlite"
)

// Inventory is the subset of platform/inventory.yaml that ops uses.
type Inventory struct {
	Repos []string
	Apps  []AppInfo
}

// AppInfo is one entry under apps:.
type AppInfo struct {
	Name, URL, API string
}

// ParseInventory parses inventory.yaml.
func ParseInventory(src []byte) (*Inventory, error) {
	v, err := yamlite.Parse(src)
	if err != nil {
		return nil, err
	}
	root, _ := v.(map[string]any)
	if root == nil {
		return nil, fmt.Errorf("inventory: top level is not a mapping")
	}
	inv := &Inventory{}
	repos, _ := root["repositories"].([]any)
	for _, r := range repos {
		var name string
		switch r := r.(type) {
		case map[string]any:
			name, _ = r["name"].(string)
		case string:
			name = r
		}
		if err := check(repoNameRe, "repository name", name); err != nil {
			return nil, fmt.Errorf("inventory: %w", err)
		}
		inv.Repos = append(inv.Repos, name)
	}
	apps, _ := root["apps"].(map[string]any)
	for name, a := range apps {
		m, _ := a.(map[string]any)
		u, _ := m["url"].(string)
		api, _ := m["api"].(string)
		inv.Apps = append(inv.Apps, AppInfo{Name: name, URL: u, API: api})
	}
	sort.Slice(inv.Apps, func(i, j int) bool { return inv.Apps[i].Name < inv.Apps[j].Name })
	return inv, nil
}

// Inventory fetches and parses platform/inventory.yaml (source token).
func (o *Ops) Inventory(ctx context.Context) (*Inventory, error) {
	b, err := o.GH.Raw(ctx, "source", platformRepo, "inventory.yaml")
	if err != nil {
		return nil, fmt.Errorf("fetch inventory.yaml: %w", err)
	}
	return ParseInventory(b)
}

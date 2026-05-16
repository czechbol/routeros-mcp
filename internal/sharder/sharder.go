// Package sharder splits a RouterOS OpenAPI document into per-top-menu shards.
package sharder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	dirPerm  = 0o755
	filePerm = 0o600
)

// Index is written as openapi/index.json and lists the available shards.
type Index struct {
	SpecVersion string   `json:"spec_version"`
	OpenAPI     string   `json:"openapi"`
	Menus       []string `json:"menus"`
}

// Shard is the per-menu file.
type Shard struct {
	Menu       string                    `json:"menu"`
	Paths      map[string]map[string]any `json:"paths"`
	Components map[string]any            `json:"components,omitempty"`
}

type rawDoc struct {
	OpenAPI    string                    `json:"openapi"`
	Info       struct{ Version string }  `json:"info"`
	Paths      map[string]map[string]any `json:"paths"`
	Components map[string]any            `json:"components"`
}

// ShardFile reads the OpenAPI document at srcFile and writes per-menu shards
// under outDir. Returns the index describing what was written.
func ShardFile(srcFile, outDir string) (*Index, error) {
	doc, err := readDoc(srcFile)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, dirPerm); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	shards := partition(doc.Paths)
	menus := sortedMenus(shards)

	for _, menu := range menus {
		if err := writeShard(filepath.Join(outDir, sanitize(menu)+".json"), shards[menu]); err != nil {
			return nil, err
		}
	}

	idx := &Index{SpecVersion: doc.Info.Version, OpenAPI: doc.OpenAPI, Menus: menus}
	return idx, writeIndex(filepath.Join(outDir, "index.json"), idx)
}

func readDoc(srcFile string) (*rawDoc, error) {
	raw, err := os.ReadFile(srcFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcFile, err)
	}
	var doc rawDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", srcFile, err)
	}
	return &doc, nil
}

func partition(paths map[string]map[string]any) map[string]*Shard {
	shards := make(map[string]*Shard)
	for path, ops := range paths {
		menu := topMenu(path)
		if menu == "" {
			continue
		}
		s, ok := shards[menu]
		if !ok {
			s = &Shard{Menu: menu, Paths: map[string]map[string]any{}}
			shards[menu] = s
		}
		s.Paths[path] = ops
	}
	return shards
}

func sortedMenus(shards map[string]*Shard) []string {
	menus := make([]string, 0, len(shards))
	for menu := range shards {
		menus = append(menus, menu)
	}
	sort.Strings(menus)
	return menus
}

func writeShard(outPath string, s *Shard) error {
	buf, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode shard %s: %w", s.Menu, err)
	}
	if err := os.WriteFile(outPath, buf, filePerm); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

func writeIndex(outPath string, idx *Index) error {
	buf, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	if err := os.WriteFile(outPath, buf, filePerm); err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	return nil
}

func topMenu(path string) string {
	trim := strings.TrimLeft(path, "/")
	if trim == "" {
		return ""
	}
	parts := strings.SplitN(trim, "/", 2)
	return parts[0]
}

func sanitize(menu string) string {
	r := strings.NewReplacer("/", "_", "..", "_", " ", "_")
	return r.Replace(menu)
}

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultWorkspace is the literal workspace name used when nothing else has
// been chosen — see PRD § "Default workspace".
const DefaultWorkspace = "default"

// Global is ~/.bnkctl/config.yaml — non-secret user-wide preferences and
// the current_workspace pointer that all commands resolve through.
type Global struct {
	CurrentWorkspace string `yaml:"current_workspace,omitempty"`
	NoColor          bool   `yaml:"no_color,omitempty"`
	Output           string `yaml:"output,omitempty"` // text | json
}

// LoadGlobal reads ~/.bnkctl/config.yaml. Missing file returns a
// zero-valued Global (not an error) — first-run users haven't written one
// yet. Empty file is also OK.
func LoadGlobal() (*Global, error) {
	path, err := GlobalConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Global{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var g Global
	if err := yaml.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &g, nil
}

// SaveGlobal writes ~/.bnkctl/config.yaml, creating the directory tree if
// needed. Permissions: 0644 file, 0755 dir (config is non-secret).
func SaveGlobal(g *Global) error {
	path, err := GlobalConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	b, err := yaml.Marshal(g)
	if err != nil {
		return fmt.Errorf("encoding global config: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layout constants. Filenames inside a workspace dir match the global
// filename so users can mentally substitute one for the other.
const (
	defaultBaseDirName  = ".bnkctl"
	globalConfigFile    = "config.yaml"
	workspaceConfigFile = "config.yaml"
	stateSubdir         = "state"

	// BNKCTLHomeEnv overrides the default ~/.bnkctl base. Used by tests
	// (and power users who want non-home-dir state).
	BNKCTLHomeEnv = "BNKCTL_HOME"
)

// BaseDir returns the bnkctl root directory.
//
//   1. $BNKCTL_HOME if set (no expansion — used as-is)
//   2. $HOME/.bnkctl otherwise
func BaseDir() (string, error) {
	if v := os.Getenv(BNKCTLHomeEnv); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, defaultBaseDirName), nil
}

// GlobalConfigPath: ~/.bnkctl/config.yaml
func GlobalConfigPath() (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, globalConfigFile), nil
}

// WorkspaceDir: ~/.bnkctl/<name>/
func WorkspaceDir(name string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// WorkspaceConfigPath: ~/.bnkctl/<name>/config.yaml
func WorkspaceConfigPath(name string) (string, error) {
	dir, err := WorkspaceDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, workspaceConfigFile), nil
}

// WorkspaceStateDir: ~/.bnkctl/<name>/state/  (terraform.tfstate, kubeconfig, scratch/)
func WorkspaceStateDir(name string) (string, error) {
	dir, err := WorkspaceDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateSubdir), nil
}

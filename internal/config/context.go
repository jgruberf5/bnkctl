package config

import (
	"errors"
	"fmt"
)

// Context is the resolved bnkctl runtime context for a single command:
// which workspace we're operating on, plus the loaded global and (if the
// workspace exists) workspace config.
//
// CLI commands acquire a Context near their start and use it for the rest
// of their execution. `bnkctl init` is the one command that may run with
// Workspace == nil — every other command should treat that as "needs init".
type Context struct {
	WorkspaceName string
	Global        *Global
	Workspace     *Workspace // nil if the workspace hasn't been initialised yet
}

// New resolves the workspace name from (in priority order):
//
//  1. workspaceFlag (the -w/--workspace value, may be "")
//  2. Global.CurrentWorkspace
//  3. DefaultWorkspace ("default")
//
// It then loads the workspace config if it exists. Missing workspace is
// not propagated as an error — the caller decides whether that's OK
// (`bnkctl init` is fine with it; everything else should error).
func New(workspaceFlag string) (*Context, error) {
	g, err := LoadGlobal()
	if err != nil {
		return nil, fmt.Errorf("loading global config: %w", err)
	}

	name := workspaceFlag
	if name == "" {
		name = g.CurrentWorkspace
	}
	if name == "" {
		name = DefaultWorkspace
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	ctx := &Context{WorkspaceName: name, Global: g}

	ws, err := LoadWorkspace(name)
	switch {
	case err == nil:
		ctx.Workspace = ws
	case errors.Is(err, ErrWorkspaceNotFound):
		// Fine — caller is expected to handle (init creates; others error).
	default:
		return nil, err
	}
	return ctx, nil
}

// SetCurrent persists the workspace pointer in ~/.bnkctl/config.yaml so
// later commands without -w default to it. Refuses if the workspace
// doesn't exist on disk yet — pointing at a phantom would just produce
// confusing "workspace not found" errors on every subsequent command.
func SetCurrent(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !WorkspaceExists(name) {
		return fmt.Errorf("workspace %q does not exist; create it with `bnkctl init -w %s`", name, name)
	}
	g, err := LoadGlobal()
	if err != nil {
		return err
	}
	g.CurrentWorkspace = name
	return SaveGlobal(g)
}

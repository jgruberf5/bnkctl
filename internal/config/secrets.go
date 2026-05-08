package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

// API key resolution sources for IBMCloudCfg.APIKeySource.
const (
	APIKeySourceEnv      = "env"
	APIKeySourceKeychain = "keychain"
	APIKeySourceConfig   = "config" // base64-encoded in workspace config.yaml — obfuscation only
	APIKeySourcePrompt   = "prompt"

	// keychainService is the OS-keychain "service" namespace bnkctl uses.
	// Per-workspace entries are stored under user="<workspace>/ibmcloud_api_key".
	keychainService = "bnkctl"
)

// apiKeyEnvVars are the env vars consulted (in order) when resolving from
// "env" — same set bnk historically forwarded into the runner image.
var apiKeyEnvVars = []string{
	"IBMCLOUD_API_KEY",
	"IC_API_KEY",
	"TF_VAR_ibmcloud_api_key",
	"TF_VAR_IBMCLOUD_API_KEY",
	"TF_VAR_IC_API_KEY",
}

// ResolveAPIKey returns the IBM Cloud API key for the given workspace.
//
// source overrides the resolution chain when non-empty:
//
//	""         — env → keychain → config (base64) → prompt → error
//	"env"      — env only
//	"keychain" — keychain only
//	"config"   — base64-decoded api_key_b64 in workspace config.yaml only
//	"prompt"   — interactive prompt only (errors if stdin is not a TTY)
func ResolveAPIKey(workspace, source string) (string, error) {
	switch source {
	case "":
		if k, ok := apiKeyFromEnv(); ok {
			return k, nil
		}
		if k, err := apiKeyFromKeychain(workspace); err == nil && k != "" {
			return k, nil
		}
		if k, err := apiKeyFromConfig(workspace); err == nil && k != "" {
			return k, nil
		}
		return apiKeyFromPrompt(workspace)
	case APIKeySourceEnv:
		if k, ok := apiKeyFromEnv(); ok {
			return k, nil
		}
		return "", errors.New("no IBM Cloud API key in environment (looked for IBMCLOUD_API_KEY, IC_API_KEY, TF_VAR_ibmcloud_api_key, TF_VAR_IBMCLOUD_API_KEY, TF_VAR_IC_API_KEY)")
	case APIKeySourceKeychain:
		k, err := apiKeyFromKeychain(workspace)
		if err != nil {
			return "", err
		}
		if k == "" {
			return "", fmt.Errorf("no API key for workspace %q in OS keychain", workspace)
		}
		return k, nil
	case APIKeySourceConfig:
		k, err := apiKeyFromConfig(workspace)
		if err != nil {
			return "", err
		}
		if k == "" {
			return "", fmt.Errorf("no api_key_b64 set in workspace %q config.yaml", workspace)
		}
		return k, nil
	case APIKeySourcePrompt:
		return apiKeyFromPrompt(workspace)
	default:
		return "", fmt.Errorf("unknown api_key_source %q (want env|keychain|config|prompt)", source)
	}
}

func apiKeyFromEnv() (string, bool) {
	for _, v := range apiKeyEnvVars {
		if k := os.Getenv(v); k != "" {
			return k, true
		}
	}
	return "", false
}

// apiKeyFromConfig reads api_key_b64 from the workspace's config.yaml,
// base64-decodes it, and returns the plaintext.
//
// Returns empty (no error) when the field is unset or the workspace
// hasn't been initialised yet — falls through to the next source in
// the resolution chain.
//
// Reminder: base64 is OBFUSCATION, not encryption. Anyone with the
// file can decode it. The recommended secure path is the OS keychain.
func apiKeyFromConfig(workspace string) (string, error) {
	ws, err := LoadWorkspace(workspace)
	if err != nil {
		if errors.Is(err, ErrWorkspaceNotFound) {
			return "", nil
		}
		return "", err
	}
	if ws.IBMCloud.APIKeyB64 == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ws.IBMCloud.APIKeyB64))
	if err != nil {
		return "", fmt.Errorf("decoding api_key_b64 from %q config.yaml: %w", workspace, err)
	}
	key := strings.TrimSpace(string(decoded))
	if key == "" {
		return "", fmt.Errorf("api_key_b64 in %q config.yaml decodes to empty", workspace)
	}
	return key, nil
}

// EncodeAPIKeyForConfig base64-encodes a plaintext API key for storage
// in IBMCloudCfg.APIKeyB64. Convenience for callers (e.g. `bnkctl init
// --save-api-key` in v1.x); users can also encode by hand:
//
//	echo -n "$IBMCLOUD_API_KEY" | base64
func EncodeAPIKeyForConfig(plaintext string) string {
	return base64.StdEncoding.EncodeToString([]byte(plaintext))
}

func apiKeyFromKeychain(workspace string) (string, error) {
	user := workspace + "/ibmcloud_api_key"
	k, err := keyring.Get(keychainService, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading OS keychain: %w", err)
	}
	return k, nil
}

// apiKeyFromPrompt reads the key from the TTY without echo, then offers to
// save it to the OS keychain.
func apiKeyFromPrompt(workspace string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no IBM Cloud API key available and stdin is not a TTY (cannot prompt; set IBMCLOUD_API_KEY or run `bnkctl init`)")
	}
	fmt.Fprintf(os.Stderr, "Enter IBM Cloud API key for workspace %q: ", workspace)
	keyBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading API key: %w", err)
	}
	key := strings.TrimSpace(string(keyBytes))
	if key == "" {
		return "", errors.New("empty API key")
	}

	fmt.Fprintf(os.Stderr, "Save the key for future runs? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || strings.HasPrefix(answer, "y") {
		dest, err := SaveAPIKeyForWorkspace(workspace, key)
		if err != nil {
			// Both keychain and config save failed. Most common reason:
			// workspace doesn't exist yet (init flow). The caller will
			// re-attempt persistence after the workspace is saved.
			fmt.Fprintf(os.Stderr, "  warning: could not persist key now (%v); the calling command may retry after the workspace is created\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ saved to %s\n", dest)
		}
	}
	return key, nil
}

// SaveAPIKeyToKeychain stores the API key under the bnkctl service for the
// given workspace. Used by `bnkctl init` once the user has entered the key.
func SaveAPIKeyToKeychain(workspace, key string) error {
	if err := ValidateName(workspace); err != nil {
		return err
	}
	user := workspace + "/ibmcloud_api_key"
	return keyring.Set(keychainService, user, key)
}

// APIKeyInKeychain reports whether the workspace already has a key
// stored in the OS keychain. Used by callers that want to decide
// whether to also persist via config.yaml b64.
func APIKeyInKeychain(workspace string) bool {
	k, err := apiKeyFromKeychain(workspace)
	return err == nil && k != ""
}

// SaveAPIKeyForWorkspace persists the key to the most reliable
// destination available. Order:
//
//  1. OS keychain (recommended — process-isolated, system-managed).
//  2. config.yaml api_key_b64 (fallback for environments without a
//     working keychain — typically WSL2 without libsecret).
//
// Returns the destination it wrote to, or an error if both failed
// (e.g. keychain unavailable AND workspace doesn't exist yet — caller
// should retry after creating the workspace).
//
// Idempotent: calling repeatedly with the same key is safe.
func SaveAPIKeyForWorkspace(workspace, key string) (string, error) {
	if kerr := SaveAPIKeyToKeychain(workspace, key); kerr == nil {
		return "OS keychain", nil
	} else if cerr := saveAPIKeyToConfig(workspace, key); cerr == nil {
		return "config.yaml (base64)", nil
	} else {
		return "", fmt.Errorf("keychain failed (%v) and config save failed: %w", kerr, cerr)
	}
}

// saveAPIKeyToConfig writes api_key_b64 into the workspace's existing
// config.yaml. The workspace must already be initialised — for first
// `bnkctl init` flow, callers should run SaveWorkspace first then call
// this.
func saveAPIKeyToConfig(workspace, plaintext string) error {
	ws, err := LoadWorkspace(workspace)
	if err != nil {
		return err
	}
	ws.IBMCloud.APIKeyB64 = base64.StdEncoding.EncodeToString([]byte(plaintext))
	return SaveWorkspace(workspace, ws)
}

// DeleteAPIKeyFromKeychain removes the workspace's keychain entry. Used
// by `bnkctl workspaces delete` to leave no residue. Missing entry is
// not an error.
func DeleteAPIKeyFromKeychain(workspace string) error {
	if err := ValidateName(workspace); err != nil {
		return err
	}
	user := workspace + "/ibmcloud_api_key"
	err := keyring.Delete(keychainService, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

package config

import (
	"bufio"
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
//	""         — env → keychain → prompt → error
//	"env"      — env only
//	"keychain" — keychain only
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
	case APIKeySourcePrompt:
		return apiKeyFromPrompt(workspace)
	default:
		return "", fmt.Errorf("unknown api_key_source %q (want env|keychain|prompt)", source)
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

	fmt.Fprintf(os.Stderr, "Save to OS keychain so you don't have to re-enter it? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || strings.HasPrefix(answer, "y") {
		if err := SaveAPIKeyToKeychain(workspace, key); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save to keychain: %v\n", err)
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

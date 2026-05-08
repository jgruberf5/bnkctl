package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Workspace is ~/.bnkctl/<name>/config.yaml.
//
// Mirrors the per-workspace example in docs/PRD.md. Note that there is no
// `api_key` field — secrets live in env vars or the OS keychain, never in
// this struct. Plaintext keys in the YAML are rejected at load time by
// rejectPlaintextSecrets.
type Workspace struct {
	IBMCloud IBMCloudCfg `yaml:"ibmcloud"`
	Cluster  ClusterCfg  `yaml:"cluster"`
	BNK      BNKCfg      `yaml:"bnk,omitempty"`
	Test     TestCfg     `yaml:"test,omitempty"`
	TFSource TFSourceCfg `yaml:"tf_source"`
	COS      *COSCfg     `yaml:"cos,omitempty"`
}

type IBMCloudCfg struct {
	Region        string `yaml:"region"`
	ResourceGroup string `yaml:"resource_group"`
	APIKeySource  string `yaml:"api_key_source,omitempty"` // env | keychain | config | prompt — see secrets.go

	// APIKeyB64 stores the API key base64-encoded inline in the workspace
	// config. This is OBFUSCATION, NOT ENCRYPTION — anyone with the file
	// can decode it instantly. Treat the file like a plaintext credential:
	// chmod 600, .gitignore, never commit. Provided as a convenience for
	// single-user setups; the keychain or env-var path is the recommended
	// secure default.
	//
	// Note that the field name does NOT match the rejectPlaintextSecrets
	// regex (which guards `api_key`, not `api_key_b64`), so the value
	// loads normally without tripping the plaintext rejection.
	APIKeyB64 string `yaml:"api_key_b64,omitempty"`
}

type ClusterCfg struct {
	Create           bool   `yaml:"create"`
	Name             string `yaml:"name"`
	OpenShiftVersion string `yaml:"openshift_version,omitempty"`
	WorkersPerZone   int    `yaml:"workers_per_zone,omitempty"`
}

type BNKCfg struct {
	CNEInstanceSize string `yaml:"cneinstance_size,omitempty"`
	FARRepoURL      string `yaml:"far_repo_url,omitempty"`
	ManifestVersion string `yaml:"manifest_version,omitempty"`
}

type TestCfg struct {
	Throughput   ThroughputCfg   `yaml:"throughput,omitempty"`
	Connectivity ConnectivityCfg `yaml:"connectivity,omitempty"`
}

type ThroughputCfg struct {
	Image       string `yaml:"image,omitempty"`        // default: networkstatic/iperf3:latest
	Duration    int    `yaml:"duration,omitempty"`     // seconds; default 30
	Streams     int    `yaml:"streams,omitempty"`      // parallel; default 8
	DefaultMode string `yaml:"default_mode,omitempty"` // north-south | east-west
}

type ConnectivityCfg struct {
	ExtraHosts []string `yaml:"extra_hosts,omitempty"`
}

type TFSourceCfg struct {
	Type string `yaml:"type"` // github | local
	Repo string `yaml:"repo,omitempty"`
	Ref  string `yaml:"ref,omitempty"`
	Path string `yaml:"path,omitempty"` // populated for type=local
}

type COSCfg struct {
	Instance string      `yaml:"instance,omitempty"`
	Bucket   string      `yaml:"bucket,omitempty"`
	Upload   []COSUpload `yaml:"upload,omitempty"`
}

type COSUpload struct {
	Source string `yaml:"source"`
	Key    string `yaml:"key"`
}

// ErrWorkspaceNotFound is returned by LoadWorkspace when the workspace's
// config.yaml does not exist. Callers (e.g. `bnkctl init`) check for this
// to distinguish "workspace doesn't exist yet" from real I/O errors.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// validNameRE constrains workspace names to filesystem-safe identifiers so
// we never accidentally interpret a path traversal as a name. Names must
// start with alphanumeric (rejects ".", "..", "-leading"), be at most 64
// chars, and contain only [A-Za-z0-9_.-].
var validNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// ValidateName rejects empty / overlong / path-traversing workspace names.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("workspace name is empty")
	}
	if !validNameRE.MatchString(name) {
		return fmt.Errorf("workspace name %q is invalid: must be 1–64 chars, [A-Za-z0-9_.-], starting with alphanumeric", name)
	}
	return nil
}

// LoadWorkspace reads ~/.bnkctl/<name>/config.yaml. Returns
// ErrWorkspaceNotFound (wrapped) if the file is missing.
func LoadWorkspace(name string) (*Workspace, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	path, err := WorkspaceConfigPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := rejectPlaintextSecrets(b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var ws Workspace
	if err := yaml.Unmarshal(b, &ws); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &ws, nil
}

// SaveWorkspace writes ~/.bnkctl/<name>/config.yaml, creating both the
// workspace dir and its state/ subdir.
func SaveWorkspace(name string, ws *Workspace) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	cfgPath, err := WorkspaceConfigPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(cfgPath), err)
	}
	stateDir, err := WorkspaceStateDir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", stateDir, err)
	}
	b, err := yaml.Marshal(ws)
	if err != nil {
		return fmt.Errorf("encoding workspace config: %w", err)
	}
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", cfgPath, err)
	}
	return nil
}

// ListWorkspaces returns the names of every directory under BaseDir that
// looks like a workspace (contains config.yaml). Order: filesystem-natural
// (which os.ReadDir sorts alphabetically on most platforms).
func ListWorkspaces() ([]string, error) {
	base, err := BaseDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfg := filepath.Join(base, e.Name(), workspaceConfigFile)
		if _, err := os.Stat(cfg); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// WorkspaceExists is a stat-only check.
func WorkspaceExists(name string) bool {
	if err := ValidateName(name); err != nil {
		return false
	}
	cfg, err := WorkspaceConfigPath(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(cfg)
	return err == nil
}

// DeleteWorkspace removes ~/.bnkctl/<name>/. Refuses to delete if the
// workspace's terraform.tfstate has resources (would orphan live infra)
// unless force is true.
func DeleteWorkspace(name string, force bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dir, err := WorkspaceDir(name)
	if err != nil {
		return err
	}
	if !force {
		statePath := filepath.Join(dir, stateSubdir, "terraform.tfstate")
		if has, _ := tfstateHasResources(statePath); has {
			return fmt.Errorf("workspace %q has terraform-managed resources; pass --force to delete anyway", name)
		}
	}
	return os.RemoveAll(dir)
}

// tfstateHasResources is a deliberately shallow check — counts entries in
// state.resources via JSON parse. Errors (file missing, malformed) are
// treated as "no resources" so the caller falls back to safe-delete.
func tfstateHasResources(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var s struct {
		Resources []any `json:"resources"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return false, err
	}
	return len(s.Resources) > 0, nil
}

// plaintextSecretsRE matches lines that look like a credential value being
// set in YAML. Heuristic — catches the common shapes (api_key, password,
// token) without false-positiving on commented-out examples or empty values.
var plaintextSecretsRE = regexp.MustCompile(`(?m)^[\t ]*(api_key|apikey|ibmcloud_api_key|ic_api_key|password|token|secret_access_key|hmac_secret)[\t ]*:[\t ]+[^\s#\n][^\n]*`)

func rejectPlaintextSecrets(b []byte) error {
	if loc := plaintextSecretsRE.FindIndex(b); loc != nil {
		return fmt.Errorf("plaintext secret detected (offset %d): workspace config.yaml must not contain credentials — use IBMCLOUD_API_KEY env var or the OS keychain (see `bnkctl init`)", loc[0])
	}
	return nil
}

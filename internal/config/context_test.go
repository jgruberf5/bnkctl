package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// All tests redirect $BNKCTL_HOME via t.Setenv so they never touch the
// real ~/.bnkctl. t.TempDir auto-cleans on failure.

func TestNew_DefaultWorkspace_NoState(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	ctx, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ctx.WorkspaceName != DefaultWorkspace {
		t.Errorf("WorkspaceName = %q, want %q", ctx.WorkspaceName, DefaultWorkspace)
	}
	if ctx.Workspace != nil {
		t.Errorf("Workspace = %+v, want nil for fresh state", ctx.Workspace)
	}
}

func TestNew_FlagOverridesGlobalCurrent(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	if err := SaveGlobal(&Global{CurrentWorkspace: "prod"}); err != nil {
		t.Fatal(err)
	}
	ctx, err := New("demo")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ctx.WorkspaceName != "demo" {
		t.Errorf("flag did not override global; got %q", ctx.WorkspaceName)
	}
}

func TestNew_GlobalCurrentUsedWhenNoFlag(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	if err := SaveGlobal(&Global{CurrentWorkspace: "prod"}); err != nil {
		t.Fatal(err)
	}
	ctx, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ctx.WorkspaceName != "prod" {
		t.Errorf("global current ignored; got %q want prod", ctx.WorkspaceName)
	}
}

func TestSaveAndLoadWorkspace_Roundtrip(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	in := &Workspace{
		IBMCloud: IBMCloudCfg{Region: "us-south", ResourceGroup: "default", APIKeySource: APIKeySourceEnv},
		Cluster:  ClusterCfg{Create: true, Name: "bnk-demo", OpenShiftVersion: "4.18", WorkersPerZone: 1},
		TFSource: TFSourceCfg{Type: "github", Repo: "jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3", Ref: "v0.6.7"},
	}
	if err := SaveWorkspace("demo", in); err != nil {
		t.Fatalf("SaveWorkspace: %v", err)
	}

	out, err := LoadWorkspace("demo")
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}
	if out.IBMCloud.Region != "us-south" || out.Cluster.Name != "bnk-demo" || out.TFSource.Ref != "v0.6.7" {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
}

func TestLoadWorkspace_NotFound(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	_, err := LoadWorkspace("nope")
	if err == nil {
		t.Fatal("expected ErrWorkspaceNotFound, got nil")
	}
	if !strings.Contains(err.Error(), "workspace not found") {
		t.Errorf("error = %v, want it to wrap ErrWorkspaceNotFound", err)
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"default", "prod", "demo-1", "team_a", "ABC.123", "a"}
	bad := []string{
		"",
		"../escape",
		"foo/bar",
		strings.Repeat("a", 65),
		"-leading",
		".dot",
		"_underscore",
	}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q): unexpected error %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q): expected error, got nil", n)
		}
	}
}

func TestRejectPlaintextSecrets(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	tmpHome, _ := BaseDir()
	cfg := filepath.Join(tmpHome, "tainted", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "ibmcloud:\n  region: us-south\n  api_key: hunter2\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadWorkspace("tainted")
	if err == nil {
		t.Fatal("expected plaintext-secret rejection, got nil")
	}
	if !strings.Contains(err.Error(), "plaintext secret") {
		t.Errorf("error = %v, want plaintext-secret rejection", err)
	}
}

func TestRejectPlaintextSecrets_AllowsCommentedExamples(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	tmpHome, _ := BaseDir()
	cfg := filepath.Join(tmpHome, "ok", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	// Commented + empty-value forms must not trip the rejection.
	body := `ibmcloud:
  region: us-south
  resource_group: default
  api_key_source: env
  # api_key: this-would-be-bad-but-it-is-commented
cluster:
  create: true
  name: bnk-demo
tf_source:
  type: github
  repo: jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3
  ref: v0.6.7
`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace("ok"); err != nil {
		t.Errorf("expected commented api_key to be allowed; got %v", err)
	}
}

func TestListWorkspaces(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	for _, n := range []string{"alpha", "beta", "gamma"} {
		if err := SaveWorkspace(n, &Workspace{}); err != nil {
			t.Fatal(err)
		}
	}
	// A non-workspace dir (no config.yaml) must be skipped.
	tmpHome, _ := BaseDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, "not-a-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSetCurrent_RejectsMissingWorkspace(t *testing.T) {
	t.Setenv(BNKCTLHomeEnv, t.TempDir())

	if err := SetCurrent("phantom"); err == nil {
		t.Fatal("expected SetCurrent to reject missing workspace")
	}
}

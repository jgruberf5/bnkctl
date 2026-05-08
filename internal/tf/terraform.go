package tf

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/jgruberf5/bnkctl/internal/config"
)

// Workspace ties together the terraform working directory (resolved TF
// source), the per-bnkctl-workspace state directory, and a configured
// terraform-exec handle that drives plan/apply/destroy.
//
// One Workspace per command invocation. Not safe for concurrent reuse.
type Workspace struct {
	name      string
	sourceDir string
	stateDir  string
	tf        *tfexec.Terraform
}

// Open prepares a Workspace for terraform operations:
//
//   - Locates `terraform` on PATH; clear error if missing.
//   - Resolves the TF source via FetchSource (downloads if needed).
//   - Constructs a terraform-exec handle with TF_DATA_DIR pointing at
//     stateDir/terraform/, so .terraform/ doesn't pollute the source dir.
//   - Exports apiKey as TF_VAR_ibmcloud_api_key in the env terraform sees.
//     The key is never written to disk by bnkctl.
//
// stdout/stderr (if non-nil) get terraform's streamed output. Pass
// os.Stdout / os.Stderr from CLI commands.
func Open(
	ctx context.Context,
	name string,
	wsCfg *config.Workspace,
	stateDir string,
	apiKey string,
	stdout, stderr io.Writer,
) (*Workspace, error) {
	if wsCfg == nil {
		return nil, fmt.Errorf("workspace config is nil (run `bnkctl init`)")
	}

	tfBin, err := exec.LookPath("terraform")
	if err != nil {
		return nil, fmt.Errorf("terraform not found on PATH — install terraform >= 1.5 (https://developer.hashicorp.com/terraform/install)")
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating state dir %s: %w", stateDir, err)
	}
	srcRoot := filepath.Join(stateDir, "tf-source")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		return nil, fmt.Errorf("creating tf-source dir %s: %w", srcRoot, err)
	}

	sourceDir, err := FetchSource(ctx, wsCfg.TFSource, srcRoot)
	if err != nil {
		return nil, err
	}

	tf, err := tfexec.NewTerraform(sourceDir, tfBin)
	if err != nil {
		return nil, fmt.Errorf("initialising terraform-exec: %w", err)
	}

	if stdout != nil {
		tf.SetStdout(stdout)
	}
	if stderr != nil {
		tf.SetStderr(stderr)
	}

	// Build the env terraform sees. Start from the host env so PATH,
	// HOME, etc. are present, then layer in our overrides.
	env := envSnapshot()
	env["TF_DATA_DIR"] = filepath.Join(stateDir, "terraform")
	if apiKey != "" {
		env["TF_VAR_ibmcloud_api_key"] = apiKey
	}
	if err := tf.SetEnv(env); err != nil {
		return nil, fmt.Errorf("setting terraform env: %w", err)
	}

	return &Workspace{
		name:      name,
		sourceDir: sourceDir,
		stateDir:  stateDir,
		tf:        tf,
	}, nil
}

// SourceDir is the path containing the resolved .tf files.
func (w *Workspace) SourceDir() string { return w.sourceDir }

// StateDir is the bnkctl per-workspace state root.
func (w *Workspace) StateDir() string { return w.stateDir }

// TFVarsPath: <stateDir>/terraform.tfvars  (auto-rendered from config.yaml; do not hand-edit)
func (w *Workspace) TFVarsPath() string {
	return filepath.Join(w.stateDir, "terraform.tfvars")
}

// UserTFVarsPath: <workspace-dir>/terraform.tfvars.user (sibling to
// config.yaml). Optional — if present, bnkctl passes it to terraform
// as a second -var-file after the auto-rendered one, so values in the
// user file override values from config.yaml. Useful for variables
// bnkctl's RenderTFVars doesn't expose (testing_*, roks_min_worker_*,
// cert_manager_namespace, etc.) or for one-off overrides.
func (w *Workspace) UserTFVarsPath() string {
	return filepath.Join(filepath.Dir(w.stateDir), "terraform.tfvars.user")
}

// HasUserTFVars reports whether the optional override file exists.
func (w *Workspace) HasUserTFVars() bool {
	_, err := os.Stat(w.UserTFVarsPath())
	return err == nil
}

// varFiles returns the list of -var-file paths to pass terraform.
// Order matters: later files override earlier (terraform's spec).
//
//	1. auto-rendered terraform.tfvars (from config.yaml)
//	2. terraform.tfvars.user (workspace-persistent override, if present)
//	3. extra (--var-file flags from the CLI, in the order given)
//
// Later layers win — a --var-file value beats both the workspace
// override and the generated tfvars.
func (w *Workspace) varFiles(extra ...string) []string {
	paths := []string{w.TFVarsPath()}
	if w.HasUserTFVars() {
		paths = append(paths, w.UserTFVarsPath())
	}
	paths = append(paths, extra...)
	return paths
}

// StatePath: <stateDir>/terraform.tfstate
func (w *Workspace) StatePath() string {
	return filepath.Join(w.stateDir, "terraform.tfstate")
}

// WriteTFVars renders wsCfg into the workspace's terraform.tfvars file
// (excluding api_key — see WriteTFVars in vars.go).
func (w *Workspace) WriteTFVars(wsCfg *config.Workspace) error {
	return WriteTFVars(w.TFVarsPath(), wsCfg)
}

// Init runs `terraform init`.
func (w *Workspace) Init(ctx context.Context) error {
	return w.tf.Init(ctx, tfexec.Upgrade(false))
}

// Plan runs `terraform plan`. Returns true if changes are pending.
// extraVarFiles are appended to the var-file chain — see varFiles for
// the precedence order.
func (w *Workspace) Plan(ctx context.Context, extraVarFiles ...string) (bool, error) {
	opts := []tfexec.PlanOption{tfexec.State(w.StatePath())}
	for _, p := range w.varFiles(extraVarFiles...) {
		opts = append(opts, tfexec.VarFile(p))
	}
	return w.tf.Plan(ctx, opts...)
}

// Apply runs `terraform apply`. tfexec auto-passes -auto-approve since
// terraform-exec doesn't allow interactive prompts; bnkctl's own
// confirmation gate runs at the CLI layer instead.
func (w *Workspace) Apply(ctx context.Context, extraVarFiles ...string) error {
	opts := []tfexec.ApplyOption{tfexec.State(w.StatePath())}
	for _, p := range w.varFiles(extraVarFiles...) {
		opts = append(opts, tfexec.VarFile(p))
	}
	return w.tf.Apply(ctx, opts...)
}

// Destroy runs `terraform destroy`.
func (w *Workspace) Destroy(ctx context.Context, extraVarFiles ...string) error {
	opts := []tfexec.DestroyOption{tfexec.State(w.StatePath())}
	for _, p := range w.varFiles(extraVarFiles...) {
		opts = append(opts, tfexec.VarFile(p))
	}
	return w.tf.Destroy(ctx, opts...)
}

// Output reads terraform outputs (raw values + sensitivity flags).
func (w *Workspace) Output(ctx context.Context) (map[string]tfexec.OutputMeta, error) {
	return w.tf.Output(ctx, tfexec.State(w.StatePath()))
}

// envSnapshot copies os.Environ() into a map[string]string suitable for
// tfexec.SetEnv.
func envSnapshot() map[string]string {
	src := os.Environ()
	m := make(map[string]string, len(src))
	for _, kv := range src {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return m
}

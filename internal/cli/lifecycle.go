package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/ibm"
	"github.com/jgruberf5/bnkctl/internal/k8s"
	"github.com/jgruberf5/bnkctl/internal/tf"
)

// Apply retry tuning. ROKS master endpoints take 1–5 minutes to fully
// propagate after creation; the cneinstance/license/cert-manager
// modules race that propagation by curl-ing the master directly. When
// terraform-exec surfaces a transient-shaped failure, sleep and retry
// rather than making the user type `bnkctl up` again.
const (
	applyMaxAttempts = 3
	applyRetryWait   = 60 * time.Second
)

// Shared across init/up/apply/down — only one runs per invocation, so a
// single backing var per logically-distinct flag is fine.
var (
	flagAuto         bool
	flagTFSource     string
	flagUpgradeTF    bool
	flagNoKubeconfig bool
	flagVarFiles     []string // -var-file (repeatable; matches terraform's flag)
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Interactive setup; writes the workspace config.yaml",
	Long: `bnkctl init walks through the prompts (region, resource group, cluster,
BNK version) and writes ~/.bnkctl/<workspace>/config.yaml.

On first run with no -w flag, creates and uses the 'default' workspace.
Re-run with --upgrade-tf to bump the pinned Terraform source to its latest
release.`,
	RunE: runInit,
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Provision (or attach) and deploy BNK — terraform plan + apply",
	Long: `bnkctl up validates credentials, resolves the pinned Terraform source,
runs plan, and (after confirmation, unless --auto) applies. Idempotent and
resumable: a partial failure is recovered by re-running 'bnkctl up'.`,
	RunE: runUp,
}

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Read-only; show what bnkctl up would change",
	RunE:  runPlan,
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply Terraform without re-prompting (assumes config.yaml exists)",
	RunE:  runApply,
}

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Destroy everything in the workspace — terraform destroy",
	RunE:  runDown,
}

func init() {
	initCmd.Flags().BoolVar(&flagUpgradeTF, "upgrade-tf", false, "resolve and pin the latest TF release into config.yaml")
	initCmd.Flags().StringVar(&flagTFSource, "tf-source", "", "override TF source (path or URL); pinned into config.yaml")

	upCmd.Flags().BoolVar(&flagAuto, "auto", false, "skip the confirmation prompt before apply")
	upCmd.Flags().StringVar(&flagTFSource, "tf-source", "", "override TF source for this run only")
	upCmd.Flags().BoolVar(&flagNoKubeconfig, "no-kubeconfig", false, "skip the post-apply admin kubeconfig fetch")

	applyCmd.Flags().BoolVar(&flagAuto, "auto", false, "skip the confirmation prompt")
	applyCmd.Flags().BoolVar(&flagNoKubeconfig, "no-kubeconfig", false, "skip the post-apply admin kubeconfig fetch")
	downCmd.Flags().BoolVar(&flagAuto, "auto", false, "skip the destroy confirmation")

	// --var-file matches terraform's own flag: repeatable, later wins.
	// Layered after the bnkctl-generated tfvars and the workspace's
	// optional terraform.tfvars.user override.
	for _, c := range []*cobra.Command{upCmd, planCmd, applyCmd, downCmd} {
		c.Flags().StringArrayVar(&flagVarFiles, "var-file", nil, "extra TF var-file (repeatable; later files override earlier)")
	}

	rootCmd.AddCommand(initCmd, upCmd, planCmd, applyCmd, downCmd)
}

// ── lifecycle implementations ───────────────────────────────────────

// runUp = plan + confirm + apply + (optional) kubeconfig fetch. The
// "everyday" deploy command.
func runUp(cmd *cobra.Command, _ []string) error {
	cctx, tfws, err := openTF(cmd.Context(), true)
	if err != nil {
		return err
	}
	if err := writeAndInit(cmd.Context(), tfws, cctx.Workspace); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "→ terraform plan")
	changes, err := tfws.Plan(cmd.Context(), flagVarFiles...)
	if err != nil {
		return err
	}
	if !changes {
		fmt.Fprintln(os.Stderr, "✓ no changes")
		// Even with no infra changes, fetching the kubeconfig is useful
		// (cluster may already exist; user wants creds locally).
		tryAutoKubeconfig(cmd.Context(), cctx, tfws)
		return nil
	}
	if !flagAuto && !promptYesNo("Apply this plan?", false) {
		return errors.New("aborted")
	}

	fmt.Fprintln(os.Stderr, "→ terraform apply")
	if err := applyWithRetry(cmd.Context(), tfws, flagVarFiles); err != nil {
		return err
	}
	tryAutoKubeconfig(cmd.Context(), cctx, tfws)
	return nil
}

// runPlan = plan only. Read-only — never prompts.
func runPlan(cmd *cobra.Command, _ []string) error {
	cctx, tfws, err := openTF(cmd.Context(), true)
	if err != nil {
		return err
	}
	if err := writeAndInit(cmd.Context(), tfws, cctx.Workspace); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ terraform plan")
	_, err = tfws.Plan(cmd.Context(), flagVarFiles...)
	return err
}

// runApply = direct apply, no plan-and-confirm gate. For users who know
// what they're doing (CI, scripted flows, post-`bnkctl plan`).
func runApply(cmd *cobra.Command, _ []string) error {
	cctx, tfws, err := openTF(cmd.Context(), true)
	if err != nil {
		return err
	}
	if err := writeAndInit(cmd.Context(), tfws, cctx.Workspace); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ terraform apply")
	if err := applyWithRetry(cmd.Context(), tfws, flagVarFiles); err != nil {
		return err
	}
	tryAutoKubeconfig(cmd.Context(), cctx, tfws)
	return nil
}

// runDown = destroy with confirmation gate. --auto skips the prompt.
func runDown(cmd *cobra.Command, _ []string) error {
	cctx, tfws, err := openTF(cmd.Context(), true)
	if err != nil {
		return err
	}
	if !flagAuto {
		fmt.Fprintf(os.Stderr, "This will destroy workspace %q's resources.\n", cctx.WorkspaceName)
		if !promptYesNo("Continue?", false) {
			return errors.New("aborted")
		}
	}
	if err := writeAndInit(cmd.Context(), tfws, cctx.Workspace); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ terraform destroy")
	return tfws.Destroy(cmd.Context(), flagVarFiles...)
}

// ── shared helpers ──────────────────────────────────────────────────

// openTF loads the workspace config, resolves the API key (if needed),
// and opens a terraform workspace ready for init/plan/apply/destroy.
//
// needAPIKey controls whether ResolveAPIKey is called. plan technically
// reads the API key path-to-validation but real cluster fetches happen
// at apply time, so this is mostly a flag for documentation; we set it
// true everywhere right now.
func openTF(ctx context.Context, needAPIKey bool) (*config.Context, *tf.Workspace, error) {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return nil, nil, err
	}
	if cctx.Workspace == nil {
		return nil, nil, fmt.Errorf("workspace %q is not initialised; run `bnkctl init` first", cctx.WorkspaceName)
	}

	var apiKey string
	if needAPIKey {
		apiKey, err = config.ResolveAPIKey(cctx.WorkspaceName, cctx.Workspace.IBMCloud.APIKeySource)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving API key: %w", err)
		}
	}

	stateDir, err := config.WorkspaceStateDir(cctx.WorkspaceName)
	if err != nil {
		return nil, nil, err
	}

	tfws, err := tf.Open(ctx, cctx.WorkspaceName, cctx.Workspace, stateDir, apiKey, os.Stdout, os.Stderr)
	if err != nil {
		return nil, nil, err
	}
	return cctx, tfws, nil
}

// writeAndInit renders tfvars and runs terraform init. Common preamble
// for plan/apply/up/down. Notes when a user-supplied tfvars override
// is going to be layered on top — visible cue so users aren't
// surprised when their values land.
func writeAndInit(ctx context.Context, tfws *tf.Workspace, ws *config.Workspace) error {
	if err := tfws.WriteTFVars(ws); err != nil {
		return fmt.Errorf("writing tfvars: %w", err)
	}
	if tfws.HasUserTFVars() {
		fmt.Fprintf(os.Stderr, "→ Layering user tfvars from %s (overrides config.yaml-derived values)\n", tfws.UserTFVarsPath())
	}
	fmt.Fprintln(os.Stderr, "→ terraform init")
	return tfws.Init(ctx)
}

// tryAutoKubeconfig fetches the admin kubeconfig from IBM Cloud and
// writes it to $KUBECONFIG (or ~/.kube/config). Best-effort: any error
// is logged as a warning rather than failing the parent command —
// `bnkctl up` succeeded if terraform succeeded; the kubeconfig is a
// convenience the user can still grab via `bnkctl kubeconfig --download`.
//
// Skipped entirely with --no-kubeconfig.
func tryAutoKubeconfig(ctx context.Context, cctx *config.Context, tfws *tf.Workspace) {
	if flagNoKubeconfig {
		return
	}
	if cctx == nil || cctx.Workspace == nil {
		return
	}
	cluster := resolveClusterIdentity(ctx, cctx, tfws)
	if cluster == "" {
		return
	}
	apiKey, err := config.ResolveAPIKey(cctx.WorkspaceName, cctx.Workspace.IBMCloud.APIKeySource)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: skipping kubeconfig fetch (api key): %v\n", err)
		return
	}
	ic, err := ibm.New(apiKey, cctx.Workspace.IBMCloud.Region)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: skipping kubeconfig fetch: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "→ Fetching admin kubeconfig for %q\n", cluster)
	body, err := ic.FetchClusterConfig(ctx, cluster)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: kubeconfig fetch failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "         (run `bnkctl kubeconfig --download` to retry)")
		return
	}
	target := k8s.DefaultKubeconfigPath()
	if target == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintf(os.Stderr, "warning: resolving home dir: %v\n", herr)
			return
		}
		target = filepath.Join(home, ".kube", "config")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: creating %s: %v\n", filepath.Dir(target), err)
		return
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: writing %s: %v\n", target, err)
		return
	}
	fmt.Fprintf(os.Stderr, "✓ Wrote kubeconfig to %s\n", target)
}

// resolveClusterIdentity figures out which cluster to fetch the
// kubeconfig for. Order:
//
//  1. Terraform output `roks_cluster_id` — post-apply truth, the actual
//     ID provisioned. Beats config.yaml when the user's --var-file
//     overrides cluster.name.
//  2. Terraform output `roks_cluster_name` — same idea, slightly less
//     stable if the cluster was renamed.
//  3. cctx.Workspace.Cluster.Name — config.yaml fallback (pre-apply or
//     if outputs aren't reachable).
//
// Returns "" if no source produced a usable identity — caller skips
// auto-fetch silently.
func resolveClusterIdentity(ctx context.Context, cctx *config.Context, tfws *tf.Workspace) string {
	if tfws != nil {
		if outputs, err := tfws.Output(ctx); err == nil {
			for _, key := range []string{"roks_cluster_id", "roks_cluster_name"} {
				if om, ok := outputs[key]; ok && len(om.Value) > 0 {
					var s string
					if json.Unmarshal(om.Value, &s) == nil && s != "" {
						return s
					}
				}
			}
		}
	}
	if cctx != nil && cctx.Workspace != nil {
		return cctx.Workspace.Cluster.Name
	}
	return ""
}

// applyWithRetry wraps tfws.Apply with bounded retries on transient
// failures. Terraform's natural idempotence makes retry safe — already
// created resources are skipped on subsequent runs; only the failed
// null_resources / data sources re-execute.
//
// Triggers a retry on any of the heuristic patterns in looksTransient,
// up to applyMaxAttempts total. Sleeps applyRetryWait between attempts
// so the master endpoint or other timing-sensitive resources can settle.
func applyWithRetry(ctx context.Context, tfws *tf.Workspace, varFiles []string) error {
	var err error
	for attempt := 1; attempt <= applyMaxAttempts; attempt++ {
		err = tfws.Apply(ctx, varFiles...)
		if err == nil {
			return nil
		}
		if !looksTransient(err) {
			return err
		}
		if attempt == applyMaxAttempts {
			fmt.Fprintf(os.Stderr, "\n✗ apply still failing after %d attempts — giving up\n", applyMaxAttempts)
			return err
		}
		fmt.Fprintf(os.Stderr, "\n→ apply attempt %d hit a transient-looking failure; waiting %s and retrying...\n",
			attempt, applyRetryWait)
		select {
		case <-time.After(applyRetryWait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// looksTransient reports whether an apply error matches one of the
// known apply-time race or transient-network patterns. Heuristic, not
// exhaustive — false negatives just mean the user retries manually
// like before, false positives are harmless because terraform's apply
// is naturally idempotent for resources already in state.
//
// Cases covered:
//   - "exit status 7" — curl couldn't connect (master endpoint not yet
//     propagated; the cneinstance SCC binding curls hit this)
//   - "Connection refused" / "i/o timeout" / "no route to host" /
//     "network is unreachable" / "TLS handshake timeout" — generic
//     transient-network class. WSL2 / VPN flapping / IBM IAM blips all
//     surface as one of these.
//   - "no such host" — DNS hiccup (transient, almost always self-heals)
//   - "failed to dial" — Go net stdlib transient
//   - "to download the config doesn't exist" — the IBM provider's
//     ibm_container_cluster_config target dir is missing (we pre-create
//     it now, but the safety net stays for older state)
func looksTransient(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, pat := range []string{
		"exit status 7",
		"Connection refused",
		"connection refused",
		"i/o timeout",
		"no route to host",
		"network is unreachable",
		"no such host",
		"TLS handshake timeout",
		"failed to dial",
		"to download the config doesn't exist",
	} {
		if strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

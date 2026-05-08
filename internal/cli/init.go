package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/ibm"
	"github.com/jgruberf5/bnkctl/internal/tf"
)

// githubRepoPattern matches a GitHub-shaped "owner/repo" slug. Used by
// the init prompt to decide whether a user-typed TF source is a GitHub
// repo or a local path. Must match the full input.
var githubRepoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*/[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// looksLikeGitHubRepo reports whether s matches the "owner/repo" pattern.
// Anything else (paths, URLs, blank) is treated as a local path.
func looksLikeGitHubRepo(s string) bool {
	return githubRepoPattern.MatchString(strings.TrimSpace(s))
}

const (
	// defaultTFRepo is the source bnkctl drives by default. Per the
	// PRD's "unified tag stream" decision, bnkctl pins to the latest
	// release of this repo at init time.
	defaultTFRepo = "jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3"

	// initTimeout caps the network operations init does (IAM verify,
	// resource group lookup, GitHub release resolution). Prompts run
	// outside the timeout so users can take their time typing.
	initTimeout = 60 * time.Second
)

// runInit implements `bnkctl init` — interactive setup that writes the
// workspace's config.yaml and (if no global pointer is set) sets the
// current_workspace pointer.
//
// Behaviours:
//   - If --upgrade-tf and the workspace exists, just bumps tf_source.ref.
//   - If the workspace exists and --upgrade-tf is not set, prompts to
//     overwrite (existing values become the default for each prompt).
//   - If stdin is not a TTY, accepts every default — usable from CI as
//     long as IBMCLOUD_API_KEY and the existing config (or workspace
//     name) provide enough context.
func runInit(_ *cobra.Command, _ []string) error {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return err
	}

	// --upgrade-tf is the cheap path: re-resolve TF source on existing config.
	if flagUpgradeTF {
		if cctx.Workspace == nil {
			return fmt.Errorf("workspace %q does not exist; run `bnkctl init` (without --upgrade-tf) to create it", cctx.WorkspaceName)
		}
		ctx, cancel := contextWithTimeout(initTimeout)
		defer cancel()
		return runUpgradeTF(ctx, cctx)
	}

	// Existing workspace + interactive overwrite confirmation.
	if cctx.Workspace != nil {
		fmt.Fprintf(os.Stderr, "Workspace %q already exists.\n", cctx.WorkspaceName)
		if !promptYesNo("Overwrite config?", false) {
			return errors.New("aborted")
		}
	}

	fmt.Fprintf(os.Stderr, "Setting up workspace %q\n\n", cctx.WorkspaceName)

	// Existing values become defaults; otherwise PRD-stated defaults.
	dRegion, dRG, dCluster, dOCP, dWorkers, dCreate := initDefaults(cctx)

	// API key — env, then keychain, then prompt; offer to save on prompt.
	apiKey, err := config.ResolveAPIKey(cctx.WorkspaceName, "")
	if err != nil {
		return fmt.Errorf("resolving API key: %w", err)
	}

	region := promptString("Region", dRegion)

	// Network ops below — bound to a timeout.
	ctx, cancel := contextWithTimeout(initTimeout)
	defer cancel()

	fmt.Fprintln(os.Stderr, "\n→ Verifying IBM Cloud credentials...")
	ic, err := ibm.New(apiKey, region)
	if err != nil {
		return err
	}
	id, err := ic.Verify(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ %s\n\n", id)

	rgName := promptString("Resource group", dRG)
	rgID, err := ic.ResolveResourceGroup(ctx, rgName)
	if err != nil {
		return fmt.Errorf("verifying resource group: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Resource group %q (id %s)\n\n", rgName, rgID)

	create := promptYesNo("Create new ROKS cluster?", dCreate)

	cluster := config.ClusterCfg{Create: create}
	if create {
		cluster.Name = promptString("Cluster name", dCluster)
		cluster.OpenShiftVersion = promptString("OpenShift version", dOCP)
		cluster.WorkersPerZone = promptInt("Workers per zone", dWorkers)
	} else {
		cluster.Name = promptString("Existing cluster name or ID", dCluster)
		if cluster.Name == "" {
			return errors.New("existing cluster name is required when not creating")
		}
	}

	tfCfg, err := promptTFSource(ctx, cctx)
	if err != nil {
		return err
	}

	ws := &config.Workspace{
		IBMCloud: config.IBMCloudCfg{
			Region:        region,
			ResourceGroup: rgName,
		},
		Cluster:  cluster,
		TFSource: tfCfg,
	}
	if err := config.SaveWorkspace(cctx.WorkspaceName, ws); err != nil {
		return fmt.Errorf("saving workspace: %w", err)
	}
	cfgPath, _ := config.WorkspaceConfigPath(cctx.WorkspaceName)
	fmt.Fprintf(os.Stderr, "\n✓ Wrote %s\n", cfgPath)

	// Set current_workspace pointer if nothing was set globally yet.
	// Don't clobber an existing pointer — the user may have set it on purpose.
	if cctx.Global.CurrentWorkspace == "" {
		if err := config.SetCurrent(cctx.WorkspaceName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not set current workspace: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ Current workspace: %s\n", cctx.WorkspaceName)
		}
	}

	fmt.Fprintln(os.Stderr, "\nNext: bnkctl up")
	return nil
}

// runUpgradeTF re-resolves the TF source ref against the workspace's
// existing repo (or accepts --tf-source for a local override) and
// rewrites the workspace config. No prompts.
func runUpgradeTF(ctx context.Context, cctx *config.Context) error {
	if flagTFSource != "" {
		// Local-path override.
		tfCfg := config.TFSourceCfg{Type: "local", Path: flagTFSource}
		return saveTFSourceUpdate(cctx, tfCfg)
	}
	// Re-resolve latest release of whatever repo this workspace is
	// pinned to (default if it was never explicitly set).
	repo := defaultTFRepo
	if cctx.Workspace.TFSource.Type == "github" && cctx.Workspace.TFSource.Repo != "" {
		repo = cctx.Workspace.TFSource.Repo
	}
	tfCfg, err := resolveLatestRelease(ctx, repo)
	if err != nil {
		return err
	}
	return saveTFSourceUpdate(cctx, tfCfg)
}

// saveTFSourceUpdate writes a new TF source into the workspace config,
// or no-ops if it matches what's already there. Used by --upgrade-tf.
func saveTFSourceUpdate(cctx *config.Context, tfCfg config.TFSourceCfg) error {
	if cctx.Workspace.TFSource == tfCfg {
		fmt.Fprintf(os.Stderr, "TF source already at %s\n", refDescription(tfCfg))
		return nil
	}
	prev := cctx.Workspace.TFSource
	cctx.Workspace.TFSource = tfCfg
	if err := config.SaveWorkspace(cctx.WorkspaceName, cctx.Workspace); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ TF source updated %s → %s\n", refDescription(prev), refDescription(tfCfg))
	return nil
}

// promptTFSource asks the user where Terraform should pull from. Accepts
// either a GitHub `owner/repo` slug (resolves to that repo's latest
// release) or any other input (treated as a local filesystem path).
//
// --tf-source short-circuits the prompt with a local override, matching
// the existing flag's behaviour.
//
// On re-init, the existing workspace's TF source is the default — users
// just press Enter to keep it.
func promptTFSource(ctx context.Context, cctx *config.Context) (config.TFSourceCfg, error) {
	if flagTFSource != "" {
		cfg := config.TFSourceCfg{Type: "local", Path: flagTFSource}
		fmt.Fprintf(os.Stderr, "✓ TF source: local path %s\n", flagTFSource)
		return cfg, nil
	}

	def := defaultTFRepo
	if cctx.Workspace != nil {
		switch cctx.Workspace.TFSource.Type {
		case "github":
			if cctx.Workspace.TFSource.Repo != "" {
				def = cctx.Workspace.TFSource.Repo
			}
		case "local":
			if cctx.Workspace.TFSource.Path != "" {
				def = cctx.Workspace.TFSource.Path
			}
		}
	}

	fmt.Fprintln(os.Stderr, "\nTerraform source — owner/repo for a GitHub repo, or a path for a local checkout.")
	input := promptString("TF source", def)

	if looksLikeGitHubRepo(input) {
		cfg, err := resolveLatestRelease(ctx, input)
		if err != nil {
			return config.TFSourceCfg{}, err
		}
		return cfg, nil
	}

	// Anything that's not GitHub-shaped is treated as a local path.
	fmt.Fprintf(os.Stderr, "✓ TF source: local path %s\n", input)
	return config.TFSourceCfg{Type: "local", Path: input}, nil
}

// resolveLatestRelease queries GitHub for the latest release of repo and
// returns a fully-formed TFSourceCfg pinned to that tag.
func resolveLatestRelease(ctx context.Context, repo string) (config.TFSourceCfg, error) {
	fmt.Fprintf(os.Stderr, "→ Resolving latest release of %s...\n", repo)
	ref, err := tf.ResolveLatestRelease(ctx, repo)
	if err != nil {
		return config.TFSourceCfg{}, fmt.Errorf("resolving TF source from GitHub: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ TF source: %s@%s\n", repo, ref)
	return config.TFSourceCfg{Type: "github", Repo: repo, Ref: ref}, nil
}

// initDefaults returns prompt defaults: existing workspace values first,
// PRD-stated defaults second. Workspace may be nil (fresh init).
func initDefaults(cctx *config.Context) (region, rg, cluster, ocp string, workers int, create bool) {
	region, rg, cluster, ocp = "ca-tor", "default", "bnk-demo", "4.18"
	workers, create = 1, true
	if cctx.Workspace == nil {
		return
	}
	if v := cctx.Workspace.IBMCloud.Region; v != "" {
		region = v
	}
	if v := cctx.Workspace.IBMCloud.ResourceGroup; v != "" {
		rg = v
	}
	if v := cctx.Workspace.Cluster.Name; v != "" {
		cluster = v
	}
	if v := cctx.Workspace.Cluster.OpenShiftVersion; v != "" {
		ocp = v
	}
	if v := cctx.Workspace.Cluster.WorkersPerZone; v != 0 {
		workers = v
	}
	create = cctx.Workspace.Cluster.Create
	return
}

// refDescription renders a TFSourceCfg for log output.
func refDescription(c config.TFSourceCfg) string {
	switch c.Type {
	case "github":
		return fmt.Sprintf("%s@%s", c.Repo, c.Ref)
	case "local":
		return fmt.Sprintf("local:%s", c.Path)
	default:
		return "<unknown>"
	}
}

// contextWithTimeout returns a child of context.Background with the
// given timeout. Used to keep init's network ops bounded.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

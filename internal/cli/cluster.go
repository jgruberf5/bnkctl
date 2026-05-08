package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/k8s"
	"github.com/jgruberf5/bnkctl/internal/tf"
)

var (
	flagExportKubeconfig   bool
	flagKubeconfigDownload bool
	flagKubeconfigCluster  string
)

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Interactive bash with KUBECONFIG, IBMCLOUD_API_KEY, and region pre-loaded",
	Long: `bnkctl shell drops into a $SHELL subshell with the workspace's
KUBECONFIG, IBMCLOUD_API_KEY, IC_API_KEY, and IBMCLOUD_REGION exported so
locally-installed kubectl / oc / ibmcloud commands work without further
setup. Exits when the subshell does.`,
	RunE: runShell,
}

var execCmd = &cobra.Command{
	Use:                "exec [command...]",
	Short:              "Run a single command with cluster context loaded",
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true,
	RunE:               runExec,
}

var kubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig",
	Short: "Print the kubeconfig path (or contents with --export)",
	RunE:  runKubeconfig,
}

// Passthrough commands — DisableFlagParsing so cobra doesn't grab flags
// intended for the wrapped tool (e.g. `bnkctl kubectl get pods --all-namespaces`).
var kubectlCmd = &cobra.Command{
	Use:                "kubectl [args...]",
	Short:              "Passthrough to local kubectl with workspace KUBECONFIG loaded",
	DisableFlagParsing: true,
	RunE:               runKubectlPassthrough,
}

var ocCmd = &cobra.Command{
	Use:                "oc [args...]",
	Short:              "Passthrough to local oc with workspace KUBECONFIG loaded",
	DisableFlagParsing: true,
	RunE:               runOCPassthrough,
}

var ibmcloudCmd = &cobra.Command{
	Use:                "ibmcloud [args...]",
	Short:              "Passthrough to local ibmcloud with workspace API key + region loaded",
	DisableFlagParsing: true,
	RunE:               runIBMCloudPassthrough,
}

func init() {
	kubeconfigCmd.Flags().BoolVar(&flagExportKubeconfig, "export", false, "print kubeconfig contents instead of path")
	kubeconfigCmd.Flags().BoolVar(&flagKubeconfigDownload, "download", false, "fetch admin kubeconfig from IBM Cloud and save to ~/.kube/config")
	kubeconfigCmd.Flags().StringVar(&flagKubeconfigCluster, "cluster", "", "cluster name or ID for --download (default: workspace cluster.name)")
	rootCmd.AddCommand(shellCmd, execCmd, kubeconfigCmd, kubectlCmd, ocCmd, ibmcloudCmd)
}

// ── runE implementations ────────────────────────────────────────────

func runShell(_ *cobra.Command, _ []string) error {
	_, env, err := workspaceEnv()
	if err != nil {
		return err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	return runWithEnv(shell, nil, env)
}

func runExec(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("exec requires a command to run")
	}
	_, env, err := workspaceEnv()
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("%s not found on PATH", args[0])
	}
	return runWithEnv(bin, args[1:], env)
}

func runKubeconfig(cmd *cobra.Command, _ []string) error {
	if flagKubeconfigDownload {
		return runKubeconfigDownload(cmd)
	}

	path := k8s.DefaultKubeconfigPath()
	if path == "" {
		return fmt.Errorf("no kubeconfig found; run `bnkctl kubeconfig --download` or `ibmcloud ks cluster config --admin -c <cluster>`")
	}
	if flagExportKubeconfig {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	}
	fmt.Println(path)
	return nil
}

// runKubeconfigDownload fetches the admin kubeconfig directly from
// IBM's container service and saves it to $KUBECONFIG (or ~/.kube/config).
// Picks the target cluster from (in order):
//
//  1. --cluster flag (explicit user override)
//  2. terraform output `roks_cluster_id` from the workspace state
//     (post-apply truth, beats config.yaml when --var-file overrode
//     the cluster name)
//  3. terraform output `roks_cluster_name`
//  4. workspace config.yaml's cluster.name (pre-apply fallback)
func runKubeconfigDownload(cmd *cobra.Command) error {
	cctx, ic, err := openIBMClient()
	if err != nil {
		return err
	}

	cluster := flagKubeconfigCluster
	if cluster == "" {
		// Try terraform output first (catches --var-file-overridden names).
		cluster = clusterFromTFOutput(cmd.Context(), cctx)
	}
	if cluster == "" && cctx.Workspace != nil {
		cluster = cctx.Workspace.Cluster.Name
	}
	if cluster == "" {
		return fmt.Errorf("--cluster required (or set cluster.name in the workspace config, or run after `bnkctl up`)")
	}

	fmt.Fprintf(os.Stderr, "→ Fetching admin kubeconfig for %q\n", cluster)
	body, err := ic.FetchClusterConfig(cmd.Context(), cluster)
	if err != nil {
		return err
	}

	target := k8s.DefaultKubeconfigPath()
	if target == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("resolving home directory: %w", herr)
		}
		target = filepath.Join(home, ".kube", "config")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// 0o600 — kubeconfig has cluster admin certs.
	if err := os.WriteFile(target, body, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Wrote %s (%d bytes)\n", target, len(body))
	return nil
}

func runKubectlPassthrough(_ *cobra.Command, args []string) error {
	return runPassthrough("kubectl", args)
}

func runOCPassthrough(_ *cobra.Command, args []string) error {
	return runPassthrough("oc", args)
}

func runIBMCloudPassthrough(_ *cobra.Command, args []string) error {
	bin, err := exec.LookPath("ibmcloud")
	if err != nil {
		return fmt.Errorf("ibmcloud not found on PATH (install it to use `bnkctl ibmcloud`)")
	}
	_, env, err := workspaceEnv()
	if err != nil {
		return err
	}
	if err := ensureIBMCloudLoggedIn(bin, env); err != nil {
		return err
	}
	return runWithEnv(bin, args, env)
}

// ensureIBMCloudLoggedIn establishes a valid ibmcloud session before
// passthrough commands run. Stateful CLI commands (ks, target, etc.)
// fail with "Log in to the IBM Cloud CLI" if no session is cached
// in ~/.bluemix/, even with IBMCLOUD_API_KEY set in env.
//
// Strategy:
//  1. Probe with `ibmcloud account show` (fast, no side effects).
//  2. If that succeeds, session is good — skip login.
//  3. Otherwise run `ibmcloud login -r <region>` with IBMCLOUD_API_KEY
//     in env. The CLI does non-interactive apikey login when the env
//     var is set.
//
// Login output is streamed to stderr so users see what's happening
// when bnkctl is taking the extra second.
func ensureIBMCloudLoggedIn(bin string, env []string) error {
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, bin, "account", "show")
	probe.Env = env
	if err := probe.Run(); err == nil {
		return nil
	}
	fmt.Fprintln(os.Stderr, "→ ibmcloud login")
	loginArgs := []string{"login"}
	if region := envValue(env, "IBMCLOUD_REGION"); region != "" {
		loginArgs = append(loginArgs, "-r", region)
	}
	login := exec.Command(bin, loginArgs...)
	login.Env = env
	login.Stdout = os.Stderr
	login.Stderr = os.Stderr
	if err := login.Run(); err != nil {
		return fmt.Errorf("ibmcloud login failed: %w", err)
	}
	return nil
}

// envValue is a small helper for reading a key out of an env slice
// (KEY=VALUE strings). Returns "" if not found.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

func runPassthrough(tool string, args []string) error {
	bin, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("%s not found on PATH (install it to use `bnkctl %s`)", tool, tool)
	}
	_, env, err := workspaceEnv()
	if err != nil {
		return err
	}
	return runWithEnv(bin, args, env)
}

// ── helpers ─────────────────────────────────────────────────────────

// workspaceEnv composes the env a child process should inherit:
// host env + workspace's IBMCLOUD_API_KEY / IC_API_KEY / IBMCLOUD_REGION
// + KUBECONFIG (from the host's lookup chain — v1 doesn't auto-fetch).
//
// Returns the resolved Context too in case the caller wants to log
// "loaded workspace foo" before exec'ing.
func workspaceEnv() (*config.Context, []string, error) {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return nil, nil, err
	}
	if cctx.Workspace == nil {
		return nil, nil, fmt.Errorf("workspace %q is not initialised; run `bnkctl init` first", cctx.WorkspaceName)
	}

	apiKey, err := config.ResolveAPIKey(cctx.WorkspaceName, cctx.Workspace.IBMCloud.APIKeySource)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving API key: %w", err)
	}

	env := os.Environ()
	env = append(env, "IBMCLOUD_API_KEY="+apiKey)
	env = append(env, "IC_API_KEY="+apiKey)
	if r := cctx.Workspace.IBMCloud.Region; r != "" {
		env = append(env, "IBMCLOUD_REGION="+r)
	}
	if path := k8s.DefaultKubeconfigPath(); path != "" {
		env = append(env, "KUBECONFIG="+path)
	}
	// Silence the "New plug-in version available" / "TIP: --check-version"
	// banner the ibmcloud CLI prints on every invocation. It's noise the
	// user can't act on inside the bnkctl flow.
	env = append(env, "IBMCLOUD_VERSION_CHECK=false")
	return cctx, env, nil
}

// runWithEnv runs bin with args + env, wired to the host's stdin/out/err,
// and propagates the child's exit code. Cross-platform — uses os/exec
// rather than syscall.Exec so it works on Windows.
func runWithEnv(bin string, args, env []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// clusterFromTFOutput attempts to read the post-apply cluster identity
// from terraform's outputs (roks_cluster_id then roks_cluster_name).
// Returns "" silently on any failure — caller falls back to config.yaml.
//
// Opens a minimal tf.Workspace (no source fetch beyond what's already
// resolved on disk; no API key needed) just to call Output.
func clusterFromTFOutput(ctx context.Context, cctx *config.Context) string {
	if cctx == nil || cctx.Workspace == nil {
		return ""
	}
	stateDir, err := config.WorkspaceStateDir(cctx.WorkspaceName)
	if err != nil {
		return ""
	}
	// Open without an API key — terraform output doesn't need creds and
	// we don't want to trigger a prompt here. Use io.Discard equivalents
	// for stdout/stderr — these aren't user-facing surfaces.
	tfws, err := tf.Open(ctx, cctx.WorkspaceName, cctx.Workspace, stateDir, "", nil, nil)
	if err != nil {
		return ""
	}
	outputs, err := tfws.Output(ctx)
	if err != nil {
		return ""
	}
	for _, key := range []string{"roks_cluster_id", "roks_cluster_name"} {
		if om, ok := outputs[key]; ok && len(om.Value) > 0 {
			var s string
			if json.Unmarshal(om.Value, &s) == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

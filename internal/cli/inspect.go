package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/k8s"
)

var flagFollow bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Summary of the workspace: cluster, components, last apply",
	Long: `bnkctl status reports a quick read of the workspace:

  - workspace name + region
  - configured cluster name
  - pinned Terraform source
  - last terraform apply timestamp (mtime of terraform.tfstate)
  - kubeconfig path (if any)
  - cluster reachability (node count + ready count)

v1.x will add per-BNK-component readiness (flo, cis, cert-manager,
cneinstance) once the component-discovery shape is finalised.`,
	RunE: runStatus,
}

var logsCmd = &cobra.Command{
	Use:   "logs <component>",
	Short: "Tail logs for a BNK component (flo, cis, cert-manager, cneinstance)",
	Long: `Looks up the named BNK component, finds its pod(s) by label, and
streams logs to stdout. With --follow, streams live. With multiple
matching pods, tails the first and prints a hint about using
bnkctl kubectl for per-pod selection.

The component → namespace/selector map is hardcoded for v1 against the
upstream TF chart's default labels; if your install renamed namespaces
or relabelled, fall back to:

  bnkctl kubectl logs -n <ns> <pod>`,
	Args: cobra.ExactArgs(1),
	RunE: runLogs,
}

func init() {
	logsCmd.Flags().BoolVarP(&flagFollow, "follow", "f", false, "follow log output")
	rootCmd.AddCommand(statusCmd, logsCmd)
}

// runStatus prints a human-readable workspace summary. Always best-effort
// — every section reports its own missing pieces so a partial state
// (no cluster reachable, no state file yet, etc.) still produces useful
// output rather than a hard error.
func runStatus(cmd *cobra.Command, _ []string) error {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer tw.Flush()

	fmt.Fprintf(tw, "Workspace:\t%s\n", cctx.WorkspaceName)
	if cctx.Workspace == nil {
		fmt.Fprintln(tw, "Status:\t(not initialised — run `bnkctl init`)")
		return nil
	}

	fmt.Fprintf(tw, "Region:\t%s\n", or(cctx.Workspace.IBMCloud.Region, "(unset)"))
	fmt.Fprintf(tw, "Resource group:\t%s\n", or(cctx.Workspace.IBMCloud.ResourceGroup, "(unset)"))
	fmt.Fprintf(tw, "Cluster:\t%s\t%s\n", or(cctx.Workspace.Cluster.Name, "(unset)"), createOrAttach(cctx.Workspace.Cluster.Create))
	fmt.Fprintf(tw, "TF source:\t%s\n", tfSourceDescription(cctx.Workspace.TFSource))

	// Last terraform apply timestamp from tfstate mtime.
	stateDir, _ := config.WorkspaceStateDir(cctx.WorkspaceName)
	statePath := filepath.Join(stateDir, "terraform.tfstate")
	if info, err := os.Stat(statePath); err == nil {
		age := time.Since(info.ModTime()).Round(time.Second)
		fmt.Fprintf(tw, "Last apply:\t%s\t(%s ago)\n", info.ModTime().Format("2006-01-02 15:04:05 MST"), age)
	} else {
		fmt.Fprintln(tw, "Last apply:\t(no state — run `bnkctl up`)")
	}

	// Kubeconfig + cluster reachability.
	kcPath := k8s.DefaultKubeconfigPath()
	if kcPath == "" {
		fmt.Fprintln(tw, "Kubeconfig:\t(none — run `bnkctl kubeconfig --download`)")
		return nil
	}
	fmt.Fprintf(tw, "Kubeconfig:\t%s\n", kcPath)

	// Flush so the cluster check can stream its own line cleanly after.
	tw.Flush()

	clusterStatus := probeCluster(cmd.Context(), kcPath)
	fmt.Fprintf(os.Stdout, "Cluster:        %s\n", clusterStatus)
	return nil
}

// probeCluster does a single timed call to list nodes and summarises
// what it found. Never returns an error — a failed probe shows up as
// the cluster-status string.
func probeCluster(ctx context.Context, kubeconfigPath string) string {
	kc, err := k8s.NewFromKubeconfigFile(kubeconfigPath)
	if err != nil {
		return fmt.Sprintf("(unreachable: %v)", err)
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodes, err := kc.Clientset().CoreV1().Nodes().List(tctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Sprintf("(unreachable: %v)", err)
	}
	ready := 0
	for i := range nodes.Items {
		if nodeReady(&nodes.Items[i]) {
			ready++
		}
	}
	if len(nodes.Items) == 0 {
		return "0 nodes (unusual — check cluster)"
	}
	return fmt.Sprintf("%d/%d nodes ready", ready, len(nodes.Items))
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// ── small formatters ─────────────────────────────────────────────────

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func createOrAttach(create bool) string {
	if create {
		return "(create new)"
	}
	return "(attach existing)"
}

func tfSourceDescription(s config.TFSourceCfg) string {
	switch s.Type {
	case "github":
		return fmt.Sprintf("%s@%s", s.Repo, s.Ref)
	case "local":
		return "local:" + s.Path
	default:
		return "(unset)"
	}
}

// ── logs ─────────────────────────────────────────────────────────────

// bnkComponent is one row of the hardcoded component → namespace/selector
// map. Hardcoded for v1 to match the upstream TF chart's default labels.
// When BNK chart releases rename labels these need updating.
type bnkComponent struct {
	Name     string
	Desc     string
	Ns       string
	Selector string
}

var bnkComponents = []bnkComponent{
	{"flo", "F5 Lifecycle Operator", "f5-bnk", "app.kubernetes.io/name=f5-lifecycle-operator"},
	{"cis", "F5 BNK CIS controller", "f5-bnk", "app=f5-bnk-cis"},
	{"cert-manager", "cert-manager", "cert-manager", "app.kubernetes.io/instance=cert-manager"},
	{"cneinstance", "BIG-IP TMM data plane (CNEInstance pods)", "f5-bnk", "app.kubernetes.io/component=tmm"},
}

func runLogs(cmd *cobra.Command, args []string) error {
	component := args[0]
	comp := lookupComponent(component)
	if comp == nil {
		names := make([]string, 0, len(bnkComponents))
		for _, c := range bnkComponents {
			names = append(names, c.Name)
		}
		return fmt.Errorf("unknown component %q — available: %s", component, strings.Join(names, ", "))
	}

	kc, err := k8s.NewFromDefault()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	pods, err := kc.Clientset().CoreV1().Pods(comp.Ns).List(ctx, metav1.ListOptions{
		LabelSelector: comp.Selector,
	})
	if err != nil {
		return fmt.Errorf("listing %s pods in %s: %w", component, comp.Ns, err)
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no %s pods found in namespace %s with selector %s — chart label may have changed; try `bnkctl kubectl get pods -A | grep <name>`",
			component, comp.Ns, comp.Selector)
	}
	pod := &pods.Items[0]
	if len(pods.Items) > 1 {
		fmt.Fprintf(os.Stderr, "→ %d %s pods found; tailing %s (use `bnkctl kubectl logs -n %s <pod>` for a specific one)\n",
			len(pods.Items), component, pod.Name, pod.Namespace)
	} else {
		fmt.Fprintf(os.Stderr, "→ Tailing logs from %s/%s\n", pod.Namespace, pod.Name)
	}

	req := kc.Clientset().CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Follow: flagFollow,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("opening log stream: %w", err)
	}
	defer stream.Close()
	_, err = io.Copy(os.Stdout, stream)
	return err
}

func lookupComponent(name string) *bnkComponent {
	for i := range bnkComponents {
		if bnkComponents[i].Name == name {
			return &bnkComponents[i]
		}
	}
	return nil
}

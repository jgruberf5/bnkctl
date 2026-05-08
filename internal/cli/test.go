package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/k8s"
	"github.com/jgruberf5/bnkctl/internal/test"
)

var (
	flagThroughputMode      string
	flagThroughputCrossNode bool
	flagKeepFixtures        bool
	flagInsecureTLS         bool
)

var testCmd = &cobra.Command{
	Use:   "test [suite]",
	Short: "Run deployment validation tests (default: all)",
	Long: `bnkctl test runs deployment validation against the current workspace.

Suites:
  connectivity   HTTP/HTTPS reachability of deployed BNK services
  dns            DNS resolution of ingress and service hostnames
  throughput     iperf3 measurements (north-south by default; v1.x)
  all            run all of the above (default if no suite is specified)

Honors -o json with the bnkctl.v1 schema. Exit code 0 on all-pass,
non-zero on any-fail — CI-friendly.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTestDispatch,
}

var testConnectivityCmd = &cobra.Command{
	Use:   "connectivity",
	Short: "HTTP/HTTPS reachability against configured hosts",
	RunE:  runTestConnectivityCmd,
}

var testDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS resolution of configured hosts",
	RunE:  runTestDNSCmd,
}

var testThroughputCmd = &cobra.Command{
	Use:   "throughput",
	Short: "iperf3 throughput; deploys server pod automatically (v1.x)",
	Long: `Deploys an iperf3 server in the test namespace and runs the client
either from the bnkctl host (--mode north-south, default) or from a second
in-cluster pod (--mode east-west).

Not yet implemented — landing in v1.x once the internal/k8s client-go
fixture lifecycle is wired.`,
	RunE: runTestThroughputCmd,
}

var testListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available test suites",
	RunE:  runTestListCmd,
}

func init() {
	testCmd.Flags().BoolVar(&flagInsecureTLS, "insecure", false, "skip TLS certificate validation (connectivity only)")

	testThroughputCmd.Flags().StringVar(&flagThroughputMode, "mode", "north-south", "throughput mode: north-south | east-west")
	testThroughputCmd.Flags().BoolVar(&flagThroughputCrossNode, "cross-node", false, "force east-west client and server onto different nodes")
	testThroughputCmd.Flags().BoolVar(&flagKeepFixtures, "keep", false, "leave the iperf3 server pod running after the test")

	testCmd.AddCommand(testConnectivityCmd, testDNSCmd, testThroughputCmd, testListCmd)
	rootCmd.AddCommand(testCmd)
}

// runTestDispatch handles `bnkctl test [suite]` — dispatches the bare
// suite name to the corresponding subcommand impl.
func runTestDispatch(cmd *cobra.Command, args []string) error {
	suite := "all"
	if len(args) > 0 {
		suite = args[0]
	}
	switch suite {
	case "all":
		return runTestAllCmd(cmd, nil)
	case "connectivity":
		return runTestConnectivityCmd(cmd, nil)
	case "dns":
		return runTestDNSCmd(cmd, nil)
	case "throughput":
		return runTestThroughputCmd(cmd, nil)
	case "list":
		return runTestListCmd(cmd, nil)
	default:
		return fmt.Errorf("unknown test suite %q (try connectivity, dns, throughput, all)", suite)
	}
}

func runTestAllCmd(cmd *cobra.Command, _ []string) error {
	cctx, hosts, err := loadHosts()
	if err != nil {
		return err
	}
	_ = cctx
	all := test.RunAll(cmd.Context(), hosts, flagInsecureTLS)
	return outputAll(all)
}

func runTestConnectivityCmd(cmd *cobra.Command, _ []string) error {
	_, hosts, err := loadHosts()
	if err != nil {
		return err
	}
	s := test.RunConnectivity(cmd.Context(), hosts, flagInsecureTLS)
	return outputSuite(s)
}

func runTestDNSCmd(cmd *cobra.Command, _ []string) error {
	_, hosts, err := loadHosts()
	if err != nil {
		return err
	}
	s := test.RunDNS(cmd.Context(), hosts)
	return outputSuite(s)
}

func runTestThroughputCmd(cmd *cobra.Command, _ []string) error {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return err
	}
	if cctx.Workspace == nil {
		return fmt.Errorf("workspace %q is not initialised; run `bnkctl init` first", cctx.WorkspaceName)
	}

	kc, err := k8s.NewFromDefault()
	if err != nil {
		return err
	}

	ns := k8s.Iperf3Namespace
	mode := flagThroughputMode
	if mode != "north-south" && mode != "east-west" {
		return fmt.Errorf("--mode must be north-south or east-west (got %q)", mode)
	}

	svcType := corev1.ServiceTypeClusterIP
	if mode == "north-south" {
		svcType = corev1.ServiceTypeLoadBalancer
	}

	image := cctx.Workspace.Test.Throughput.Image
	if image == "" {
		image = k8s.Iperf3DefaultImage
	}

	fmt.Fprintln(os.Stderr, "→ Deploying iperf3 fixture")
	if err := kc.DeployIperf3(cmd.Context(), k8s.Iperf3Options{
		Namespace:   ns,
		Image:       image,
		ServiceType: svcType,
	}); err != nil {
		return err
	}
	if !flagKeepFixtures {
		defer teardownIperf3Best(cmd.Context(), kc, ns)
	}

	fmt.Fprintln(os.Stderr, "→ Waiting for iperf3 server pod ready")
	if err := kc.WaitIperf3Ready(cmd.Context(), ns, 0); err != nil {
		return err
	}

	endpoint, err := resolveIperf3Endpoint(cmd.Context(), kc, ns, mode)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ iperf3 endpoint: %s\n", endpoint)

	duration := cctx.Workspace.Test.Throughput.Duration
	streams := cctx.Workspace.Test.Throughput.Streams
	s := test.RunThroughput(cmd.Context(), test.ThroughputOptions{
		Mode:     mode,
		Endpoint: endpoint,
		Duration: duration,
		Streams:  streams,
	})
	return outputSuite(s)
}

// resolveIperf3Endpoint picks the address the iperf3 client connects to.
// north-south = LoadBalancer external IP/hostname (BNK data path);
// east-west   = Service ClusterIP (in-cluster client; client-from-host
// won't reach a ClusterIP, so v1 east-west still uses the host as the
// client — this means east-west measures host→ClusterIP-via-NodePort-equivalent.
// True pod-to-pod east-west lands in v1.x with an in-cluster client pod.).
func resolveIperf3Endpoint(ctx context.Context, kc *k8s.Client, ns, mode string) (string, error) {
	if mode == "north-south" {
		fmt.Fprintln(os.Stderr, "→ Waiting for LoadBalancer endpoint (can take 30–90s on IBM Cloud)")
		return kc.WaitLoadBalancerEndpoint(ctx, ns, 0)
	}
	return kc.ClusterIPEndpoint(ctx, ns)
}

// teardownIperf3Best is the deferred cleanup when --keep is not passed.
// Uses a fresh background context with a short timeout so a cancelled
// outer ctx doesn't skip the teardown entirely.
func teardownIperf3Best(_ context.Context, kc *k8s.Client, ns string) {
	tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := kc.TeardownIperf3(tctx, ns); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tearing down iperf3 fixture: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "✓ iperf3 fixture removed")
}

func runTestListCmd(_ *cobra.Command, _ []string) error {
	suites := []struct{ Name, Desc string }{
		{"connectivity", "HTTP/HTTPS reachability of configured hosts"},
		{"dns", "DNS resolution of configured hosts"},
		{"throughput", "iperf3 throughput (v1.x)"},
		{"all", "runs connectivity + dns (throughput once available)"},
	}
	for _, s := range suites {
		fmt.Printf("  %-15s %s\n", s.Name, s.Desc)
	}
	return nil
}

// loadHosts pulls the workspace's test host list. Returns a clear error
// when nothing is configured — better than silently passing an empty
// list and returning all-skipped.
func loadHosts() (*config.Context, []string, error) {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return nil, nil, err
	}
	if cctx.Workspace == nil {
		return nil, nil, fmt.Errorf("workspace %q is not initialised; run `bnkctl init` first", cctx.WorkspaceName)
	}
	hosts := test.HostsFromConfig(cctx.Workspace)
	if len(hosts) == 0 {
		return nil, nil, fmt.Errorf("no hosts configured to probe; add to test.connectivity.extra_hosts in config.yaml")
	}
	return cctx, hosts, nil
}

// outputSuite writes a SuiteRun in JSON (to stdout) or text (to stderr)
// per -o, then exits non-zero if the suite failed.
func outputSuite(s test.SuiteRun) error {
	if flagOutput == "json" {
		if err := test.WriteJSON(os.Stdout, s); err != nil {
			return err
		}
	} else {
		test.PrintSuiteText(os.Stderr, s)
	}
	if s.Overall == test.StatusFail {
		os.Exit(1)
	}
	return nil
}

// outputAll handles AllRun output (multi-suite). Same JSON-on-stdout vs
// text-on-stderr split, then exits non-zero on any-fail.
func outputAll(all test.AllRun) error {
	if flagOutput == "json" {
		if err := test.WriteJSON(os.Stdout, all); err != nil {
			return err
		}
	} else {
		for _, s := range all.Suites {
			test.PrintSuiteText(os.Stderr, s)
		}
		passed := 0
		for _, s := range all.Suites {
			if s.Overall == test.StatusPass {
				passed++
			}
		}
		fmt.Fprintf(os.Stderr, "\n%s overall (%d/%d suites passed)\n", all.Overall, passed, len(all.Suites))
	}
	if all.Overall == test.StatusFail {
		os.Exit(1)
	}
	return nil
}

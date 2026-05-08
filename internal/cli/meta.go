package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/doctor"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, and build date",
	RunE: func(_ *cobra.Command, _ []string) error {
		fmt.Printf("bnkctl %s (commit %s, built %s)\n", Version, Commit, BuildDate)
		return nil
	},
}

var selfCmd = &cobra.Command{
	Use:   "self",
	Short: "Manage the bnkctl binary itself",
}

var selfUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Pull the latest bnkctl release matching the host arch",
	Long: `Downloads the latest GitHub release tarball for this platform,
verifies its SHA256 against the release's checksums.txt, and replaces
the running binary in place.

Linux/macOS only — Windows can't replace a running .exe in place; use
` + "`scoop update bnkctl`" + ` instead.

Requires write permission on the binary's directory (typical install
under /usr/local/bin needs sudo; brew/scoop should use their own
upgrade verb).`,
	RunE: runSelfUpdate,
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check prerequisites and report missing pieces",
	Long: `Verifies the host has what bnkctl needs:
  - terraform on PATH (required)
  - iperf3 / kubectl / oc / ibmcloud on PATH (optional but recommended)
  - kubeconfig is reachable
  - the workspace is initialised
  - the IBM Cloud API key resolves and authenticates

Exits non-zero on failures (warnings don't block).`,
	RunE: runDoctor,
}

func init() {
	selfCmd.AddCommand(selfUpdateCmd)
	rootCmd.AddCommand(versionCmd, selfCmd, doctorCmd)
}

// runDoctor loads the workspace context (best-effort — doctor still runs
// usefully even when no workspace is initialised) and prints the report.
func runDoctor(cmd *cobra.Command, _ []string) error {
	// config.New tolerates a missing workspace; doctor's check methods
	// downgrade workspace-dependent checks accordingly.
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		// Even an unreadable global config shouldn't kill doctor — emit
		// what we can.
		fmt.Fprintf(os.Stderr, "warning: loading global config: %v\n", err)
		cctx = &config.Context{WorkspaceName: "(unknown)"}
	}

	results := doctor.Run(cmd.Context(), cctx)
	if err := doctor.PrintResults(os.Stdout, results); err != nil {
		return err
	}
	if doctor.HasFailures(results) {
		os.Exit(1)
	}
	return nil
}

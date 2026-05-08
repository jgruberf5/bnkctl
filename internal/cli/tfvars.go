package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/tf"
)

var (
	flagTFVarsOutput string
	flagTFVarsForce  bool
)

var tfvarsCmd = &cobra.Command{
	Use:   "tfvars",
	Short: "Emit the upstream TF's terraform.tfvars.example for editing",
	Long: `Resolves the workspace's pinned TF source (downloading the tarball
if not yet cached) and writes its terraform.tfvars.example as a
starting point you can edit and pass to bnkctl up.

Default writes to ./terraform.tfvars in the current directory.
Pass -o <path> to write elsewhere, or -o - to print to stdout.

Refuses to overwrite an existing destination unless --force is set.

Workflow:
  bnkctl init            # pins a TF source
  bnkctl tfvars          # writes ./terraform.tfvars from the upstream example
  $EDITOR ./terraform.tfvars
  bnkctl up --var-file ./terraform.tfvars`,
	RunE: runTFVars,
}

func init() {
	tfvarsCmd.Flags().StringVarP(&flagTFVarsOutput, "output", "o", "./terraform.tfvars", "destination file (or - for stdout)")
	tfvarsCmd.Flags().BoolVar(&flagTFVarsForce, "force", false, "overwrite the destination if it already exists")
	rootCmd.AddCommand(tfvarsCmd)
}

func runTFVars(cmd *cobra.Command, _ []string) error {
	cctx, err := config.New(flagWorkspace)
	if err != nil {
		return err
	}
	if cctx.Workspace == nil {
		return fmt.Errorf("workspace %q is not initialised; run `bnkctl init` first", cctx.WorkspaceName)
	}

	stateDir, err := config.WorkspaceStateDir(cctx.WorkspaceName)
	if err != nil {
		return err
	}
	srcRoot := filepath.Join(stateDir, "tf-source")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		return err
	}

	// Resolve source (fetches the tarball if not already cached). Skip
	// tf.Open so we don't write the backend override file into the
	// source dir for a read-only operation.
	sourceDir, err := tf.FetchSource(cmd.Context(), cctx.Workspace.TFSource, srcRoot)
	if err != nil {
		return fmt.Errorf("resolving TF source: %w", err)
	}

	examplePath := filepath.Join(sourceDir, "terraform.tfvars.example")
	body, err := os.ReadFile(examplePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no terraform.tfvars.example at %s (pinned TF: %s)",
				examplePath, refDescription(cctx.Workspace.TFSource))
		}
		return fmt.Errorf("reading %s: %w", examplePath, err)
	}

	// stdout path
	if flagTFVarsOutput == "-" {
		_, err = os.Stdout.Write(body)
		return err
	}

	// File path — refuse to clobber unless --force.
	if _, err := os.Stat(flagTFVarsOutput); err == nil && !flagTFVarsForce {
		return fmt.Errorf("%s already exists; pass --force to overwrite or -o <other-path>", flagTFVarsOutput)
	}
	if err := os.WriteFile(flagTFVarsOutput, body, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ Wrote %s (%d bytes)\n", flagTFVarsOutput, len(body))
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Next: edit the file, then deploy with")
	fmt.Fprintf(os.Stderr, "  bnkctl up --var-file %s\n", flagTFVarsOutput)
	return nil
}

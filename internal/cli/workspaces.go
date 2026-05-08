package cli

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jgruberf5/bnkctl/internal/config"
)

var flagWSForce bool

var workspacesCmd = &cobra.Command{
	Use:     "workspaces",
	Aliases: []string{"ws"},
	Short:   "Manage bnkctl workspaces (per-environment config + state bundles)",
	Long: `Each workspace lives under ~/.bnkctl/<name>/ with its own config.yaml
and state. The current_workspace pointer in ~/.bnkctl/config.yaml decides
which one commands run against; -w/--workspace overrides for one invocation.`,
}

var wsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspaces and their states",
	RunE:  runWSList,
}

var wsCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Print the current workspace name",
	RunE:  runWSCurrent,
}

var wsUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the current workspace pointer",
	Args:  cobra.ExactArgs(1),
	RunE:  runWSUse,
}

var wsNewCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new (empty) workspace skeleton — run `bnkctl init -w <name>` to populate",
	Args:  cobra.ExactArgs(1),
	RunE:  runWSNew,
}

var wsDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a workspace (refuses if state is non-empty unless --force)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWSDelete,
}

func init() {
	wsDeleteCmd.Flags().BoolVar(&flagWSForce, "force", false, "delete even if Terraform state lists provisioned resources")
	workspacesCmd.AddCommand(wsListCmd, wsCurrentCmd, wsUseCmd, wsNewCmd, wsDeleteCmd)
	rootCmd.AddCommand(workspacesCmd)
}

// runWSList prints workspaces in a table with a "*" marker on the current
// one. Best-effort: if a workspace's config can't be loaded (corrupt YAML,
// permissions), the row still shows the name with empty fields.
func runWSList(_ *cobra.Command, _ []string) error {
	g, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	names, err := config.ListWorkspaces()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "(no workspaces yet — run `bnkctl init` to create one)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCURRENT\tREGION\tCLUSTER\tTF SOURCE")
	for _, n := range names {
		marker := ""
		if n == g.CurrentWorkspace {
			marker = "*"
		}
		var region, cluster, tfRef string
		if ws, err := config.LoadWorkspace(n); err == nil {
			region = ws.IBMCloud.Region
			cluster = ws.Cluster.Name
			switch ws.TFSource.Type {
			case "github":
				tfRef = ws.TFSource.Repo + "@" + ws.TFSource.Ref
			case "local":
				tfRef = "local:" + ws.TFSource.Path
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n, marker, region, cluster, tfRef)
	}
	return tw.Flush()
}

// runWSCurrent prints the current_workspace pointer. Returns "(none)" on
// stderr (and nothing on stdout) when no pointer is set yet, so scripts
// using `$(bnkctl ws current)` get an empty string they can detect.
func runWSCurrent(_ *cobra.Command, _ []string) error {
	g, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	if g.CurrentWorkspace == "" {
		fmt.Fprintln(os.Stderr, "(no current workspace; run `bnkctl ws use <name>` or `bnkctl init`)")
		return nil
	}
	fmt.Println(g.CurrentWorkspace)
	return nil
}

// runWSUse sets the current_workspace pointer. config.SetCurrent already
// rejects pointing at a non-existent workspace.
func runWSUse(_ *cobra.Command, args []string) error {
	if err := config.SetCurrent(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ Current workspace: %s\n", args[0])
	return nil
}

// runWSNew creates an empty workspace. Useful when you want the directory
// to exist (so `ws use` works) before going through `bnkctl init`.
// Most users will skip this and just run `bnkctl init -w <name>` directly.
func runWSNew(_ *cobra.Command, args []string) error {
	name := args[0]
	if err := config.ValidateName(name); err != nil {
		return err
	}
	if config.WorkspaceExists(name) {
		return fmt.Errorf("workspace %q already exists", name)
	}
	if err := config.SaveWorkspace(name, &config.Workspace{}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ Created workspace %q (run `bnkctl init -w %s` to configure)\n", name, name)
	return nil
}

// runWSDelete removes a workspace's directory and its keychain entry.
// Refuses to delete the current workspace (leaves the pointer dangling)
// and refuses if Terraform state lists resources unless --force is set.
func runWSDelete(_ *cobra.Command, args []string) error {
	name := args[0]
	g, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	if g.CurrentWorkspace == name {
		return fmt.Errorf("cannot delete current workspace %q; switch first: `bnkctl ws use <other>`", name)
	}

	if !flagWSForce {
		if !promptYesNo(fmt.Sprintf("Delete workspace %q?", name), false) {
			return errors.New("aborted")
		}
	}

	if err := config.DeleteWorkspace(name, flagWSForce); err != nil {
		return err
	}

	// Best-effort keychain cleanup. Missing entry is not an error.
	if err := config.DeleteAPIKeyFromKeychain(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: removing keychain entry for %q: %v\n", name, err)
	}

	fmt.Fprintf(os.Stderr, "✓ Deleted workspace %q\n", name)
	return nil
}

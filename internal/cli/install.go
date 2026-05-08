package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var (
	flagInstallDir string
	flagInstallForce  bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Copy the running bnkctl binary into a directory on PATH",
	Long: `Install the bnkctl binary you're currently running into a directory
on $PATH so you can invoke it as ` + "`bnkctl`" + ` from any working
directory.

Default destination, in order of preference:
  $HOME/.local/bin  (preferred — typically writable without sudo)
  $HOME/bin         (older convention; still on PATH for some setups)
  /usr/local/bin    (system-wide; usually needs sudo)

Override the destination with --dir.

Idempotent: if the running binary already lives at the destination,
prints a message and exits 0. Use --force to overwrite (useful right
after a local rebuild that landed at the install path).

Examples:
  bnkctl install                       # default — ~/.local/bin
  bnkctl install --dir ~/bin           # specific user dir
  sudo bnkctl install --dir /usr/local/bin   # system-wide

Note: this is distinct from ` + "`bnkctl self update`" + `, which
pulls the latest GitHub release tarball over the network.`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVar(&flagInstallDir, "dir", "", "destination directory (default: ~/.local/bin or /usr/local/bin)")
	installCmd.Flags().BoolVar(&flagInstallForce, "force", false, "overwrite even if destination resolves to the running binary")
	rootCmd.AddCommand(installCmd)
}

func runInstall(_ *cobra.Command, _ []string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	binName := "bnkctl"
	if runtime.GOOS == "windows" {
		binName = "bnkctl.exe"
	}

	destDir := flagInstallDir
	if destDir == "" {
		destDir = chooseInstallDir()
	}
	// Expand a leading ~ since shells don't expand it inside a quoted
	// flag value the way they do for a positional bareword.
	if home, err := os.UserHomeDir(); err == nil {
		if destDir == "~" {
			destDir = home
		} else if strings.HasPrefix(destDir, "~/") {
			destDir = filepath.Join(home, destDir[2:])
		}
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w (try --dir DIR or sudo)", destDir, err)
	}

	dest := filepath.Join(destDir, binName)

	// Idempotent: running binary IS the destination → no-op.
	if !flagInstallForce {
		if absSelf, err := filepath.Abs(self); err == nil {
			if absDest, err := filepath.Abs(dest); err == nil && absSelf == absDest {
				fmt.Fprintf(os.Stderr, "✓ Already installed at %s\n", dest)
				return nil
			}
		}
	}

	fmt.Fprintf(os.Stderr, "→ Copying %s → %s\n", self, dest)
	if err := copyExecutable(self, dest); err != nil {
		return fmt.Errorf("copying: %w (write permission? try --dir or sudo)", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Installed %s\n", dest)

	if !isOnPATH(destDir) {
		fmt.Fprintf(os.Stderr, "\nwarning: %s is not on $PATH\n", destDir)
		fmt.Fprintln(os.Stderr, "  add this to your shell's rc file (~/.bashrc / ~/.zshrc / etc.):")
		fmt.Fprintf(os.Stderr, "    export PATH=\"%s:$PATH\"\n", destDir)
		fmt.Fprintln(os.Stderr, "  then `hash -r` or open a new shell.")
	} else {
		fmt.Fprintln(os.Stderr, "  (open a new shell or run `hash -r` if `bnkctl` doesn't resolve immediately)")
	}
	return nil
}

// chooseInstallDir picks a sensible default destination, preferring
// paths that don't need sudo. ~/.local/bin is the modern convention
// (XDG-ish, on PATH by default in most distros' login profiles).
func chooseInstallDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		local := filepath.Join(home, ".local", "bin")
		if isOnPATH(local) || dirExists(local) {
			return local
		}
		homeBin := filepath.Join(home, "bin")
		if isOnPATH(homeBin) || dirExists(homeBin) {
			return homeBin
		}
		// Neither exists yet — create ~/.local/bin and warn about PATH
		// after the install. Better than failing on /usr/local/bin's
		// permission check for a user who has no sudo.
		return local
	}
	return "/usr/local/bin"
}

// isOnPATH reports whether dir is in $PATH (after Abs-ing both sides
// so trailing slashes / relative entries match).
func isOnPATH(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if pAbs, err := filepath.Abs(p); err == nil && pAbs == abs {
			return true
		}
	}
	return false
}

func dirExists(d string) bool {
	info, err := os.Stat(d)
	return err == nil && info.IsDir()
}

// copyExecutable writes src to dest atomically: temp file in dest's
// dir, chmod 0755, then rename onto dest. Same-dir is required for
// rename to be atomic on most filesystems.
func copyExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp.*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", filepath.Dir(dest), err)
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

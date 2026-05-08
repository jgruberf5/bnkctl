package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jgruberf5/bnkctl/internal/config"
	"github.com/jgruberf5/bnkctl/internal/ibm"
	"github.com/jgruberf5/bnkctl/internal/k8s"
)

// Status is the outcome of a single check.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// CheckResult is one row in `bnkctl doctor` output.
type CheckResult struct {
	Name   string
	Why    string // why bnkctl cares — one short clause
	Status Status
	Detail string
}

// Run executes all diagnostic checks. cctx may carry a nil Workspace —
// workspace-dependent checks downgrade to a clear "no workspace" detail.
func Run(ctx context.Context, cctx *config.Context) []CheckResult {
	var out []CheckResult

	// Required + optional tooling.
	out = append(out, checkBinary("terraform", true, "required for `bnkctl up`"))
	out = append(out, checkBinary("iperf3", false, "needed for `bnkctl test throughput`"))
	out = append(out, checkBinary("kubectl", false, "optional; `bnkctl kubectl` passthrough"))
	out = append(out, checkBinary("oc", false, "optional; `bnkctl oc` passthrough"))
	out = append(out, checkBinary("ibmcloud", false, "optional; `bnkctl ibmcloud` passthrough"))

	// Kubeconfig: warn if missing, since throughput/status/etc need it.
	out = append(out, checkKubeconfig())

	// Workspace + creds.
	if cctx == nil {
		out = append(out, CheckResult{Name: "workspace", Status: StatusFail, Detail: "no config context"})
		return out
	}
	out = append(out, checkWorkspace(cctx))
	if cctx.Workspace != nil {
		out = append(out, checkAPIKey(cctx))
		out = append(out, checkIBMAuth(ctx, cctx))
	}

	return out
}

// checkBinary reports whether name is on PATH and (best-effort) which version.
func checkBinary(name string, required bool, why string) CheckResult {
	cr := CheckResult{Name: name, Why: why}
	path, err := exec.LookPath(name)
	if err != nil {
		if required {
			cr.Status = StatusFail
		} else {
			cr.Status = StatusWarn
		}
		cr.Detail = "not on PATH"
		return cr
	}
	cr.Status = StatusOK
	cr.Detail = path
	if v := versionLine(name); v != "" {
		cr.Detail = fmt.Sprintf("%s (%s)", path, v)
	}
	return cr
}

// versionLine runs the binary's --version-equivalent and returns the
// first non-empty line, trimmed. Best-effort — empty on any error.
func versionLine(name string) string {
	var args []string
	switch name {
	case "terraform":
		args = []string{"version"}
	case "iperf3":
		args = []string{"--version"}
	case "kubectl":
		args = []string{"version", "--client=true", "--output=yaml"}
	case "oc":
		args = []string{"version", "--client=true"}
	case "ibmcloud":
		args = []string{"--version"}
	default:
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func checkKubeconfig() CheckResult {
	cr := CheckResult{Name: "kubeconfig", Why: "needed for cluster-side ops"}
	path := k8s.DefaultKubeconfigPath()
	if path == "" {
		cr.Status = StatusWarn
		cr.Detail = "$KUBECONFIG and ~/.kube/config both missing — fetch with `ibmcloud ks cluster config --admin`"
		return cr
	}
	cr.Status = StatusOK
	cr.Detail = path
	return cr
}

func checkWorkspace(cctx *config.Context) CheckResult {
	cr := CheckResult{Name: "workspace", Why: "per-environment config + state"}
	if cctx.Workspace == nil {
		cr.Status = StatusWarn
		cr.Detail = fmt.Sprintf("%q not initialised — run `bnkctl init`", cctx.WorkspaceName)
		return cr
	}
	cr.Status = StatusOK
	cr.Detail = cctx.WorkspaceName
	return cr
}

func checkAPIKey(cctx *config.Context) CheckResult {
	cr := CheckResult{Name: "ibmcloud api key", Why: "auth for terraform + IBM SDK calls"}
	_, err := config.ResolveAPIKey(cctx.WorkspaceName, cctx.Workspace.IBMCloud.APIKeySource)
	if err != nil {
		cr.Status = StatusFail
		cr.Detail = err.Error()
		return cr
	}
	cr.Status = StatusOK
	cr.Detail = "resolved"
	return cr
}

func checkIBMAuth(ctx context.Context, cctx *config.Context) CheckResult {
	cr := CheckResult{Name: "ibm cloud auth", Why: "verifies API key works against IBM IAM"}
	apiKey, err := config.ResolveAPIKey(cctx.WorkspaceName, cctx.Workspace.IBMCloud.APIKeySource)
	if err != nil {
		cr.Status = StatusFail
		cr.Detail = "no api key: " + err.Error()
		return cr
	}
	c, err := ibm.New(apiKey, cctx.Workspace.IBMCloud.Region)
	if err != nil {
		cr.Status = StatusFail
		cr.Detail = err.Error()
		return cr
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	id, err := c.Verify(tctx)
	if err != nil {
		cr.Status = StatusFail
		cr.Detail = err.Error()
		return cr
	}
	cr.Status = StatusOK
	cr.Detail = id.String()
	return cr
}

// PrintResults writes a tabular human-readable rendering to w.
func PrintResults(w io.Writer, results []CheckResult) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range results {
		sym := "✗"
		switch r.Status {
		case StatusOK:
			sym = "✓"
		case StatusWarn:
			sym = "⚠"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", sym, r.Name, r.Detail, dim(r.Why))
	}
	return tw.Flush()
}

// HasFailures reports whether any check failed (exit-code-worthy).
// Warnings don't count — they're informational.
func HasFailures(results []CheckResult) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return true
		}
	}
	return false
}

// AsError returns a single error summarising the first failure, or nil.
func AsError(results []CheckResult) error {
	for _, r := range results {
		if r.Status == StatusFail {
			return errors.New(r.Name + ": " + r.Detail)
		}
	}
	return nil
}

// dim wraps text in a parenthetical for the "why" column. Kept simple
// (no ANSI) so output is grep-friendly and works on Windows terminals.
func dim(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

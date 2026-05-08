package test

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// SchemaVersion is the JSON output schema. Bumped only on breaking
// changes to the result shape — additive changes (new fields) keep the
// version. CI consumers depend on this for stability.
const SchemaVersion = "bnkctl.v1"

// Status is the outcome of a probe or suite.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

// ProbeResult is one observation: e.g. "DNS for foo.example.com" or
// "HTTPS GET to https://api.example.com".
type ProbeResult struct {
	Suite      string         `json:"suite"`             // connectivity | dns | throughput
	Name       string         `json:"name"`              // human-readable target
	Status     Status         `json:"status"`            // pass | fail | skipped
	DurationMS int64          `json:"duration_ms"`       // wall-clock for this probe
	Detail     string         `json:"detail,omitempty"`  // short human summary
	Extra      map[string]any `json:"extra,omitempty"`   // suite-specific structured data
}

// SuiteRun is a collection of probe results for one suite invocation.
// Self-describing (Schema + Command + Suite) so a JSON consumer can
// identify it without surrounding context.
type SuiteRun struct {
	Schema     string        `json:"schema"`
	Command    string        `json:"command"`
	Suite      string        `json:"suite"`
	Timestamp  time.Time     `json:"timestamp"`
	DurationMS int64         `json:"duration_ms"`
	Results    []ProbeResult `json:"results"`
	Overall    Status        `json:"overall"`
}

// AllRun is the umbrella result for `bnkctl test all` — composes
// multiple SuiteRuns plus a single overall status.
type AllRun struct {
	Schema     string     `json:"schema"`
	Command    string     `json:"command"`
	Timestamp  time.Time  `json:"timestamp"`
	DurationMS int64      `json:"duration_ms"`
	Suites     []SuiteRun `json:"suites"`
	Overall    Status     `json:"overall"`
}

// WriteJSON writes v as indented JSON to w.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Aggregate computes the overall status across probes:
//   - any fail → fail
//   - else any pass → pass
//   - else (all skipped, or empty) → skipped
func Aggregate(probes []ProbeResult) Status {
	hasPass := false
	for _, p := range probes {
		if p.Status == StatusFail {
			return StatusFail
		}
		if p.Status == StatusPass {
			hasPass = true
		}
	}
	if hasPass {
		return StatusPass
	}
	return StatusSkipped
}

// PrintSuiteText writes a human-readable rendering of a SuiteRun to w.
// Symbols: ✓ pass · skipped ✗ fail.
func PrintSuiteText(w io.Writer, s SuiteRun) {
	fmt.Fprintf(w, "## %s (%s, %dms)\n", s.Suite, s.Overall, s.DurationMS)
	for _, r := range s.Results {
		sym := "✗"
		switch r.Status {
		case StatusPass:
			sym = "✓"
		case StatusSkipped:
			sym = "·"
		}
		fmt.Fprintf(w, "  %s %s", sym, r.Name)
		if r.Detail != "" {
			fmt.Fprintf(w, " — %s", r.Detail)
		}
		fmt.Fprintln(w)
	}
}

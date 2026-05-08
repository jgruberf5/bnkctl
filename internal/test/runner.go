package test

import (
	"context"
	"time"

	"github.com/jgruberf5/bnkctl/internal/config"
)

// HostsFromConfig pulls the user-configured host list from the
// workspace's test.connectivity.extra_hosts. Empty result is allowed —
// callers should produce a clear error pointing at the config key.
func HostsFromConfig(ws *config.Workspace) []string {
	if ws == nil {
		return nil
	}
	return ws.Test.Connectivity.ExtraHosts
}

// RunAll runs DNS + connectivity (and, in v1.x, throughput) against the
// same host list and returns a composed AllRun. Throughput is currently
// not included in v1.0; it will be added with its own opts.
func RunAll(ctx context.Context, hosts []string, insecureSkipVerify bool) AllRun {
	start := time.Now()
	dns := RunDNS(ctx, hosts)
	conn := RunConnectivity(ctx, hosts, insecureSkipVerify)

	suites := []SuiteRun{dns, conn}
	overall := StatusPass
	hasPass := false
	for _, s := range suites {
		if s.Overall == StatusFail {
			overall = StatusFail
			break
		}
		if s.Overall == StatusPass {
			hasPass = true
		}
	}
	if !hasPass && overall == StatusPass {
		overall = StatusSkipped
	}

	return AllRun{
		Schema:     SchemaVersion,
		Command:    "test",
		Timestamp:  start,
		DurationMS: time.Since(start).Milliseconds(),
		Suites:     suites,
		Overall:    overall,
	}
}

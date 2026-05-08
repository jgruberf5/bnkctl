package test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// RunDNS resolves each host. hosts may be plain names or URLs (URLs get
// parsed down to the host component).
func RunDNS(ctx context.Context, hosts []string) SuiteRun {
	start := time.Now()
	probes := make([]ProbeResult, 0, len(hosts))
	for _, h := range hosts {
		probes = append(probes, dnsProbe(ctx, hostOnly(h)))
	}
	return SuiteRun{
		Schema:     SchemaVersion,
		Command:    "test",
		Suite:      "dns",
		Timestamp:  start,
		DurationMS: time.Since(start).Milliseconds(),
		Results:    probes,
		Overall:    Aggregate(probes),
	}
}

func dnsProbe(ctx context.Context, host string) ProbeResult {
	start := time.Now()
	p := ProbeResult{Suite: "dns", Name: host, Status: StatusPass}
	if host == "" {
		p.Status = StatusFail
		p.Detail = "empty host"
		return p
	}

	resolver := net.Resolver{}
	ips, err := resolver.LookupHost(ctx, host)
	p.DurationMS = time.Since(start).Milliseconds()

	if err != nil {
		p.Status = StatusFail
		p.Detail = err.Error()
		return p
	}
	if len(ips) == 0 {
		p.Status = StatusFail
		p.Detail = "no addresses returned"
		return p
	}
	p.Detail = fmt.Sprintf("resolved %d address(es)", len(ips))
	p.Extra = map[string]any{"addresses": ips}
	return p
}

// hostOnly extracts the hostname from a URL or "host:port". Returns the
// input unchanged for plain hostnames.
func hostOnly(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	// Strip :port — but only if not part of an IPv6 literal.
	if !strings.Contains(h, "[") {
		if i := strings.LastIndex(h, ":"); i > 0 {
			h = h[:i]
		}
	}
	return h
}

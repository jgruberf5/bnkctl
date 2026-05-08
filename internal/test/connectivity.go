package test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunConnectivity probes each URL with a single HTTP GET. URLs without
// a scheme are prefixed with "https://".
//
// insecureSkipVerify=true disables certificate validation — useful for
// internal endpoints with self-signed certs during early bring-up.
// v1.x will plumb a per-host trust setting through config.yaml.
func RunConnectivity(ctx context.Context, urls []string, insecureSkipVerify bool) SuiteRun {
	start := time.Now()
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecureSkipVerify, //nolint:gosec // explicit toggle, off by default
			},
		},
	}

	probes := make([]ProbeResult, 0, len(urls))
	for _, u := range urls {
		probes = append(probes, httpProbe(ctx, client, normalizeURL(u)))
	}
	return SuiteRun{
		Schema:     SchemaVersion,
		Command:    "test",
		Suite:      "connectivity",
		Timestamp:  start,
		DurationMS: time.Since(start).Milliseconds(),
		Results:    probes,
		Overall:    Aggregate(probes),
	}
}

func httpProbe(ctx context.Context, client *http.Client, url string) ProbeResult {
	start := time.Now()
	p := ProbeResult{Suite: "connectivity", Name: url, Status: StatusPass}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		p.Status = StatusFail
		p.Detail = err.Error()
		p.DurationMS = time.Since(start).Milliseconds()
		return p
	}
	req.Header.Set("User-Agent", "bnkctl/test")

	resp, err := client.Do(req)
	p.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		p.Status = StatusFail
		p.Detail = err.Error()
		return p
	}
	defer resp.Body.Close()
	// Drain a bit so the connection can be reused; don't read everything.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	p.Detail = fmt.Sprintf("%s in %dms", resp.Status, p.DurationMS)
	p.Extra = map[string]any{
		"status_code": resp.StatusCode,
		"tls_version": tlsVersionString(resp.TLS),
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		p.Status = StatusFail
	}
	return p
}

func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		return "https://" + s
	}
	return s
}

func tlsVersionString(s *tls.ConnectionState) string {
	if s == nil {
		return ""
	}
	switch s.Version {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	}
	return ""
}

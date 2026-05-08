package ibm

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"gopkg.in/yaml.v3"
)

// containerServiceBase is the IBM Container Service public REST endpoint.
// We hit it directly (rather than via container-services-go-sdk) because
// the SDK's surface for kubeconfig download has shifted across versions
// and a direct HTTP call is more stable to write against.
const containerServiceBase = "https://containers.cloud.ibm.com"

// Retry tuning for kubeconfig fetch. Just-created clusters take a minute
// or two to register with the container service's kubeconfig endpoint.
const (
	kubeconfigMaxAttempts = 12
	kubeconfigRetryWait   = 15 * time.Second
)

// kubeconfigHTTPClient has its own timeout so a hung container-service
// endpoint doesn't take down a long-running parent request.
var kubeconfigHTTPClient = &http.Client{Timeout: 60 * time.Second}

// FetchClusterConfig downloads the admin kubeconfig for the given
// cluster (name or ID). Returns a self-contained kubeconfig YAML with
// admin certs embedded as base64 data — no companion .pem files needed
// on disk.
//
// Mirrors what `ibmcloud ks cluster config --admin` does on the wire:
// POST to /global/v2/applyRBACAndGetKubeconfig with a JSON body, parse
// the returned ZIP, inline admin.pem and admin-key.pem into the YAML.
//
// Retries on transient 404 / 503 — just-created clusters propagate to
// the container service kubeconfig endpoint a minute or two after the
// IBM provider reports them ready.
func (c *Client) FetchClusterConfig(ctx context.Context, clusterIDOrName string) ([]byte, error) {
	if clusterIDOrName == "" {
		return nil, errors.New("cluster name/id is empty")
	}

	auth := &core.IamAuthenticator{ApiKey: c.apiKey}
	token, err := auth.GetToken()
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= kubeconfigMaxAttempts; attempt++ {
		body, status, err := c.fetchClusterConfigOnce(ctx, clusterIDOrName, token)
		if err == nil {
			if attempt > 1 {
				fmt.Fprintf(os.Stderr, "  ✓ kubeconfig available after %d attempts\n", attempt)
			}
			return body, nil
		}
		lastErr = err
		if !isRetryableStatus(status) {
			return nil, err
		}
		if attempt == kubeconfigMaxAttempts {
			break
		}
		if attempt == 1 {
			fmt.Fprintf(os.Stderr, "  cluster %q not yet registered with the container service (HTTP %d); waiting up to %s...\n",
				clusterIDOrName, status, time.Duration(kubeconfigMaxAttempts)*kubeconfigRetryWait)
		}
		select {
		case <-time.After(kubeconfigRetryWait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("kubeconfig still failing after %d attempts: %w", kubeconfigMaxAttempts, lastErr)
}

func isRetryableStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusServiceUnavailable
}

// fetchClusterConfigOnce performs a single POST against the container
// service. Returns body, HTTP status (0 on transport error), error.
func (c *Client) fetchClusterConfigOnce(ctx context.Context, clusterIDOrName, token string) ([]byte, int, error) {
	url := containerServiceBase + "/global/v2/applyRBACAndGetKubeconfig"
	reqBody, _ := json.Marshal(map[string]any{
		"cluster":      clusterIDOrName,
		"format":       "zip",
		"admin":        true,
		"network":      false,
		"endpointType": "",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bnkctl")
	if c.region != "" {
		req.Header.Set("X-Region", c.region)
	}

	resp, err := kubeconfigHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calling container service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, fmt.Errorf("container service returned %s for cluster %q: %s",
			resp.Status, clusterIDOrName, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading kubeconfig response: %w", err)
	}

	if !isZIP(body) {
		// Defensive: the API contract is ZIP, but if IBM ever switches
		// to a direct YAML response we don't want to silently fail.
		return body, resp.StatusCode, nil
	}
	out, err := buildSelfContainedKubeconfig(body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return out, resp.StatusCode, nil
}

func isZIP(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && b[2] == 0x03 && b[3] == 0x04
}

// buildSelfContainedKubeconfig parses the admin ZIP (kube-config.yaml +
// admin.pem + admin-key.pem) and returns kubeconfig YAML with the cert
// files inlined as client-certificate-data / client-key-data so the
// result needs no companion files on disk.
func buildSelfContainedKubeconfig(zipBytes []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("opening admin zip: %w", err)
	}

	var kubeconfigYAML []byte
	files := map[string][]byte{} // base name → bytes (for cert lookup)

	for _, f := range r.File {
		base := f.Name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if base == "" {
			continue // directory entry
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %s in zip: %w", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s from zip: %w", f.Name, err)
		}
		files[base] = body

		// Heuristic for the kubeconfig itself: kube-config*.y[a]ml or
		// any plain .y[a]ml at the root of the archive.
		isKubeconfig := strings.HasPrefix(base, "kube-config") ||
			strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")
		if isKubeconfig && kubeconfigYAML == nil {
			kubeconfigYAML = body
		}
	}
	if kubeconfigYAML == nil {
		return nil, errors.New("no kubeconfig YAML found in admin archive")
	}

	return inlineCertRefs(kubeconfigYAML, files)
}

// inlineCertRefs walks the kubeconfig users[].user map and converts
// client-certificate / client-key file refs into their *-data inline
// forms by looking up the referenced file in `files`. Refs that don't
// resolve to a file in the archive are left alone.
func inlineCertRefs(kubeconfigYAML []byte, files map[string][]byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(kubeconfigYAML, &doc); err != nil {
		return nil, fmt.Errorf("parsing kubeconfig yaml: %w", err)
	}

	users, _ := doc["users"].([]any)
	for _, u := range users {
		userMap, _ := u.(map[string]any)
		if userMap == nil {
			continue
		}
		inner, _ := userMap["user"].(map[string]any)
		if inner == nil {
			continue
		}
		swapCertField(inner, "client-certificate", "client-certificate-data", files)
		swapCertField(inner, "client-key", "client-key-data", files)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-emitting kubeconfig yaml: %w", err)
	}
	return out, nil
}

// swapCertField replaces inner[refKey] (a relative file path into the
// archive) with inner[dataKey] (base64 of the file contents). No-op if
// the file isn't in the archive — caller's choice whether to treat that
// as an error; for our purposes leaving the original ref is the safer
// default.
func swapCertField(inner map[string]any, refKey, dataKey string, files map[string][]byte) {
	ref, ok := inner[refKey].(string)
	if !ok || ref == "" {
		return
	}
	// IBM's archive uses bare filenames (admin.pem). Be lenient with
	// any leading "./" or directory components.
	base := ref
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	body, ok := files[base]
	if !ok {
		return
	}
	delete(inner, refKey)
	inner[dataKey] = base64.StdEncoding.EncodeToString(body)
}

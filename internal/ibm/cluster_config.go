package ibm

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
)

// containerServiceBase is the IBM Container Service public REST endpoint.
// We hit it directly (rather than via container-services-go-sdk) because
// the SDK's surface for kubeconfig download has shifted across versions
// and a direct HTTP call is more stable to write against.
const containerServiceBase = "https://containers.cloud.ibm.com"

// Retry tuning for kubeconfig fetch. Just-created clusters take a minute
// or two to register with the container service's kubeconfig endpoint;
// 404s during that window are expected and benign.
const (
	kubeconfigMaxAttempts = 12
	kubeconfigRetryWait   = 15 * time.Second
)

// kubeconfigHTTPClient has its own timeout so a hung container-service
// endpoint doesn't take down a long-running parent request.
var kubeconfigHTTPClient = &http.Client{Timeout: 60 * time.Second}

// FetchClusterConfig downloads the admin kubeconfig for the given
// cluster (name or ID). Returns the raw YAML bytes — ready to write to
// disk or hand to k8s.NewFromKubeconfigBytes.
//
// Retries on 404 — just-created clusters propagate to IBM's container
// service kubeconfig endpoint a minute or two after the IBM provider
// reports them ready. Non-404 errors fail fast.
//
// IBM's container service may return either a YAML kubeconfig directly
// or a ZIP archive containing it (depending on the cluster's
// provisioning era and the API call shape). We sniff the response and
// extract from the ZIP transparently.
func (c *Client) FetchClusterConfig(ctx context.Context, clusterIDOrName string) ([]byte, error) {
	if clusterIDOrName == "" {
		return nil, errors.New("cluster name/id is empty")
	}

	// Mint an IAM bearer token via go-sdk-core. The same authenticator
	// the platform-services SDK uses, so credential resolution is
	// consistent across bnkctl.
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
		// Only 404 is worth waiting on (cluster known to IBM control
		// plane but not yet registered with the kubeconfig endpoint).
		// Anything else (auth, rate-limit, server error) fails fast.
		if status != http.StatusNotFound {
			return nil, err
		}
		if attempt == kubeconfigMaxAttempts {
			break
		}
		if attempt == 1 {
			fmt.Fprintf(os.Stderr, "  cluster %q not yet registered with the container service (404); waiting up to %s...\n",
				clusterIDOrName, time.Duration(kubeconfigMaxAttempts)*kubeconfigRetryWait)
		}
		select {
		case <-time.After(kubeconfigRetryWait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("kubeconfig still 404 after %d attempts: %w", kubeconfigMaxAttempts, lastErr)
}

// fetchClusterConfigOnce performs a single GET against the container
// service. Returns body, HTTP status (0 on transport error), error.
// The status separation lets the retry loop check 404 cheaply.
func (c *Client) fetchClusterConfigOnce(ctx context.Context, clusterIDOrName, token string) ([]byte, int, error) {
	url := fmt.Sprintf("%s/global/v2/applications/kubeconfig?cluster=%s&admin=true&format=yaml",
		containerServiceBase, clusterIDOrName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/yaml, application/zip, */*")
	req.Header.Set("User-Agent", "bnkctl")

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

	if isZIP(body) {
		out, err := extractKubeconfigFromZIP(body)
		if err != nil {
			return nil, resp.StatusCode, err
		}
		return out, resp.StatusCode, nil
	}
	return body, resp.StatusCode, nil
}

// isZIP checks for the standard PK\x03\x04 archive magic.
func isZIP(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && b[2] == 0x03 && b[3] == 0x04
}

// extractKubeconfigFromZIP pulls the kubeconfig YAML out of an admin
// archive. IBM's archive layout uses names like
// "kube-config-<region>-<cluster>.yml" alongside the embedded
// admin-key.pem / admin.pem. We pick the first .yml/.yaml that looks
// like a kubeconfig.
func extractKubeconfigFromZIP(zipBytes []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("opening zip: %w", err)
	}
	for _, f := range r.File {
		base := f.Name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		// Heuristic: kube-config-* OR plain *.yml/*.yaml at top level.
		isKubeconfig := strings.HasPrefix(base, "kube-config") ||
			strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")
		if !isKubeconfig {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s from zip: %w", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		return body, nil
	}
	return nil, errors.New("no kubeconfig YAML found in admin archive")
}

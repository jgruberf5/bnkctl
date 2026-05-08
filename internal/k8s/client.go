package k8s

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps a Kubernetes clientset and the REST config used to build
// it. One Client per command invocation; not safe for concurrent reuse.
type Client struct {
	config    *rest.Config
	clientset *kubernetes.Clientset
}

// NewFromKubeconfigBytes builds a Client from raw kubeconfig YAML.
// Used in v1.x when bnkctl fetches the kubeconfig itself via the IBM
// container service SDK.
func NewFromKubeconfigBytes(b []byte) (*Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(b)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}
	return &Client{config: cfg, clientset: cs}, nil
}

// NewFromKubeconfigFile builds a Client from a kubeconfig file on disk.
// Honors $KUBECONFIG (colon-separated list, like kubectl).
func NewFromKubeconfigFile(path string) (*Client, error) {
	if path == "" {
		return nil, errors.New("kubeconfig path is empty")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %s: %w", path, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}
	return &Client{config: cfg, clientset: cs}, nil
}

// NewFromDefault builds a Client by walking the same lookup chain
// kubectl uses: $KUBECONFIG (first existing path in a colon list) →
// ~/.kube/config. Returns a clear error if nothing's found.
func NewFromDefault() (*Client, error) {
	path := DefaultKubeconfigPath()
	if path == "" {
		return nil, errors.New("no kubeconfig found: set $KUBECONFIG or run `ibmcloud ks cluster config --admin -c <cluster>`")
	}
	return NewFromKubeconfigFile(path)
}

// DefaultKubeconfigPath returns the first existing path in $KUBECONFIG
// (colon-separated), falling back to ~/.kube/config. Empty if neither
// exists.
func DefaultKubeconfigPath() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		// $KUBECONFIG is a list; pick the first that exists.
		for _, p := range filepath.SplitList(v) {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	def := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(def); err == nil {
		return def
	}
	return ""
}

// Clientset returns the underlying client-go clientset.
func (c *Client) Clientset() *kubernetes.Clientset { return c.clientset }

// RESTConfig returns the rest.Config used to construct the clientset.
// Useful for building secondary clients (dynamic, controller-runtime).
func (c *Client) RESTConfig() *rest.Config { return c.config }

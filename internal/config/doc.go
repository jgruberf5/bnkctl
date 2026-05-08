// Package config loads workspace and global configuration, resolves the
// IBM Cloud API key (env / OS keychain / prompt), and renders Terraform
// variables files.
//
// File layout:
//
//	~/.bnkctl/config.yaml             — global preferences, current_workspace
//	~/.bnkctl/<workspace>/config.yaml — per-workspace inputs
//	~/.bnkctl/<workspace>/state/      — terraform.tfstate, kubeconfig, scratch/
//
// Override the base directory via $BNKCTL_HOME (used by tests; advanced
// users with non-home-dir state).
//
// Secrets policy: workspace config.yaml is rejected at load time if it
// contains plaintext credentials (api_key, password, token, etc.). The
// IBM Cloud API key lives in $IBMCLOUD_API_KEY or the OS keychain only.
package config

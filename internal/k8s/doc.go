// Package k8s wraps client-go for bnkctl's internal Kubernetes
// operations:
//
//   - kubeconfig loading (env / file / raw bytes)
//   - iperf3 test fixture lifecycle (deploy, wait-ready, wait-LB,
//     teardown)
//   - (v1.x) component log fetching, pod-readiness watch for status
//
// `bnkctl kubectl` and `bnkctl oc` shell to local installs and do not
// use this package — they're convenience verbs that just load the
// workspace's KUBECONFIG before exec'ing.
//
// Kubeconfig source for v1: $KUBECONFIG env or ~/.kube/config. v1.x
// adds direct fetch from the IBM container service SDK so users don't
// need to run `ibmcloud ks cluster config --admin` themselves.
package k8s

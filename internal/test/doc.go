// Package test contains the deployment-validation probe runners for
// `bnkctl test`.
//
//   - dns          — DNS resolution via net.Resolver
//   - connectivity — HTTP/HTTPS reachability via net/http
//   - throughput   — iperf3 client + in-cluster fixture lifecycle (v1.x)
//
// Output conforms to the bnkctl.v1 JSON schema (see SchemaVersion in
// result.go). Text output is for humans; JSON is the contract for CI
// consumers.
//
// Probes share an input host list (workspace's test.connectivity.extra_hosts)
// in v1.0; v1.x will add cluster-discovered service endpoints.
package test

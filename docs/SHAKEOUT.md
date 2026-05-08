# bnkctl First-Build Shake-Out Checklist

> **Status:** Drafted at end of scaffolding session, 2026-05-08.
> **Purpose:** the things I wrote on faith without a Go toolchain in
> front of me. Verify each item on first `go mod tidy` / `make build`
> against the real SDKs; fix as you go. Most are tiny renames, a few
> are real shape questions.

## How to drive this

```bash
cd /mnt/d/project/bnkctl
go mod tidy                 # resolves indirect deps, fills go.sum
make vet                    # syntactic + cheap correctness checks
make test                   # unit tests (~30 cases, no network)
make build                  # full binary
./bin/bnkctl --help         # confirm help tree
./bin/bnkctl doctor         # confirm runtime checks shape
```

When something breaks, fix the smallest thing that compiles, then move on. Don't pre-emptively redesign — most issues here are 1-character fixes (Api → API, etc.).

---

## 1. Dependency version pins

All in `go.mod`. Pinned to versions I believed existed at scaffolding time. If a `go mod tidy` errors with "no matching versions for module X", run `go get -u <module>` and pin to whatever it picks.

| Module | Pinned | Confidence | Notes |
|--------|--------|------------|-------|
| `github.com/IBM/go-sdk-core/v5` | `v5.17.5` | medium | mid-2024 era; bump fine |
| `github.com/IBM/platform-services-go-sdk` | `v0.66.0` | medium | likely newer in 2026 |
| `github.com/IBM/ibm-cos-sdk-go` | `v1.11.0` | medium | likely newer |
| `github.com/hashicorp/terraform-exec` | `v0.21.0` | high | stable lib |
| `github.com/spf13/cobra` | `v1.8.1` | high | stable |
| `github.com/zalando/go-keyring` | `v0.2.5` | high | stable |
| `golang.org/x/term` | `v0.20.0` | high | bumps frequently; tidy will pick |
| `gopkg.in/yaml.v3` | `v3.0.1` | high | only one v3 release; this is it |
| `k8s.io/{api,apimachinery,client-go}` | `v0.30.0` | high | quarterly cadence; bump to current |

**If `go mod tidy` adds a `// indirect` flood**, that's normal — IBM SDKs and client-go pull large transitive trees.

---

## 2. SDK method-name capitalization (most likely fix needed)

The IBM SDKs are inconsistent about `Api` vs `API`. I guessed `API` (uppercase) on these — `go build` will tell you in seconds if I guessed wrong.

### `internal/ibm/identity.go`

```go
opts := c.iam.NewGetAPIKeysDetailsOptions()      // could be NewGetApiKeysDetailsOptions
opts.SetIamAPIKey(c.apiKey)                      // could be SetIamApiKey
res, _, err := c.iam.GetAPIKeysDetailsWithContext(ctx, opts)  // same
```

If wrong: search/replace `APIKey` → `ApiKey` and `APIKeys` → `ApiKeys` within this file only. The SDK might also use `Apikey` (no second cap) — check the actual method signature in `iamidentityv1` godoc.

### `internal/ibm/resource_group.go`

```go
opts := c.rmg.NewListResourceGroupsOptions()
opts.SetName(name)
opts.SetAccountID(accountID)
res, _, err := c.rmg.ListResourceGroupsWithContext(ctx, opts)
```

Probably fine — these are vanilla Get/List names.

### `internal/ibm/cos_instance.go`

```go
opts := rc.NewListResourceInstancesOptions()
opts.SetStart(*startToken)                         // verify pagination param name
res, _, err := rc.ListResourceInstancesWithContext(ctx, opts)

opts := rc.NewCreateResourceInstanceOptions(name, target, resourceGroupID, planID)
                                                   // verify positional arg order
res, _, err := rc.CreateResourceInstanceWithContext(ctx, opts)

opts := rc.NewDeleteResourceInstanceOptions(idOrCRN)
opts.SetRecursive(true)
_, err := rc.DeleteResourceInstanceWithContext(ctx, opts)
```

The Resource Controller SDK is generally good about Go conventions. Likely all pass.

### `internal/ibm/cluster_config.go`

This sidesteps the container-services-go-sdk and hits the HTTP endpoint directly via `core.IamAuthenticator.GetToken()`. The `GetToken()` method is stable across go-sdk-core versions. The HTTP URL itself (`/global/v2/applications/kubeconfig`) needs real-cluster verification — see §4.

---

## 3. terraform-exec API surface

Used in `internal/tf/terraform.go`. The lib is stable but worth confirming on first build:

```go
tf, err := tfexec.NewTerraform(sourceDir, tfBin)
tf.SetStdout(stdout)
tf.SetStderr(stderr)
tf.SetEnv(envMap)                                     // map[string]string
tf.Init(ctx, tfexec.Upgrade(false))
tf.Plan(ctx, tfexec.VarFile(...), tfexec.State(...))  // returns (bool, error)
tf.Apply(ctx, tfexec.VarFile(...), tfexec.State(...))
tf.Destroy(ctx, ...)
tf.Output(ctx, tfexec.State(...))                     // map[string]tfexec.OutputMeta
```

**Auto-approve concern:** I asserted in a code comment that `tfexec.Apply` automatically passes `-auto-approve`. Verify against the v0.21 docs — if it instead expects `tfexec.Refresh(false)` or some other opt to mean "don't prompt", a one-line addition fixes it.

---

## 4. IBM Cloud API response shapes (real-cluster needed)

These can't be tested without IBM Cloud credentials. Mark for first deploy.

### Container service kubeconfig endpoint

`internal/ibm/cluster_config.go` calls:

```
GET https://containers.cloud.ibm.com/global/v2/applications/kubeconfig?cluster=X&admin=true&format=yaml
```

I sniff the response body for the ZIP magic (`PK\x03\x04`) and extract the kubeconfig from `kube-config-*.yml` if it's a ZIP. **Verify on first deploy:**

- HTTP status — 200 expected.
- Content-Type — `application/yaml` OR `application/zip`.
- If ZIP: file naming inside (`kube-config-<region>-<cluster>.yml`?).

If the endpoint shape differs significantly, fall back to the official `container-services-go-sdk/kubernetesserviceapiv1` package. The direct-HTTP path is for stability against SDK version drift, but the SDK is the canonical source.

### Resource Controller pagination

`ListResourceInstances` paginates via `NextURL` (a full URL string). I extract the `start` query param manually. Verify on real account — if IBM uses a different param name (e.g., `cursor`, `page_token`), `extractStartFromURL` in `cos_instance.go` needs adjusting.

---

## 5. Hardcoded values to verify against the real BNK chart

### COS plan UUIDs

`internal/ibm/cos_instance.go`:

```go
var COSPlanUUIDs = map[string]string{
    "standard": "744bfc56-d12c-4866-88d5-dac9139e0e5d",
    "lite":     "2fdf0c08-2d32-4f46-84b5-32e0c92fffd8",
}
```

These came from memory and may be wrong. Confirm against the IBM Cloud catalog — search for "Cloud Object Storage" service plans:

```bash
# If you have ibmcloud installed:
ibmcloud catalog service cloud-object-storage
```

Update the map. `--plan-id <UUID>` already lets users override at the CLI, so this isn't a hard blocker — just update for ergonomic correctness.

### BNK component label selectors

`internal/cli/inspect.go`:

```go
var bnkComponents = []bnkComponent{
    {"flo", "...", "f5-bnk", "app.kubernetes.io/name=f5-lifecycle-operator"},
    {"cis", "...", "f5-bnk", "app=f5-bnk-cis"},
    {"cert-manager", "...", "cert-manager", "app.kubernetes.io/instance=cert-manager"},
    {"cneinstance", "...", "f5-bnk", "app.kubernetes.io/component=tmm"},
}
```

These are educated guesses based on Helm chart conventions. Verify after a real `bnkctl up`:

```bash
kubectl -n f5-bnk get pods --show-labels
kubectl -n cert-manager get pods --show-labels
```

Update the selectors to match. v1.x can lift these into the workspace config.

---

## 6. Goreleaser asset naming

`internal/cli/self.go::assetName`:

```go
func assetName(tag string) string {
    ver := strings.TrimPrefix(tag, "v")     // v1.2.3 → 1.2.3
    ext := ".tar.gz"
    if runtime.GOOS == "windows" { ext = ".zip" }
    return fmt.Sprintf("bnkctl_%s_%s_%s%s", ver, runtime.GOOS, runtime.GOARCH, ext)
}
```

This matches goreleaser's default `name_template`. The `.goreleaser.yml` we wrote uses the default. If you customize the template, update this in lockstep. **First release will reveal any mismatch — `bnkctl self update` will report `no asset matching X`.**

---

## 7. Cross-platform shake-out

### Windows

- Self-update: gated with a clear error message pointing at `scoop update` (a running .exe can't be replaced in place).
- Path handling: `filepath` everywhere (not `path`); should be portable.
- `os.Stdin.Fd()` for `term.ReadPassword` and `term.IsTerminal`: cross-platform.
- Shell command in `bnkctl shell`: defaults to `/bin/bash` if `$SHELL` is unset; on Windows that fails. **Add Windows handling**: try `cmd.exe` or `pwsh.exe` if `runtime.GOOS == "windows"`.

### macOS Apple Silicon

- All deps build for `darwin/arm64`. goreleaser builds it.

### Linux distro variability

- terraform-exec and client-go are pure Go.
- `iperf3` for `bnkctl test throughput`: package-managed; `bnkctl doctor` already flags missing.

---

## 8. Smoke-test order on first real deploy

Once compile + unit tests pass, drive the verbs in order against a real workspace:

1. `bnkctl doctor` — surfaces every dep that's wrong. Fix what fails before the rest.
2. `bnkctl init` — interactive; verify the prompts match the PRD's happy-path script.
3. `bnkctl ws list` / `bnkctl ws current` — confirm workspace plumbing.
4. `bnkctl version` — confirms ldflags wiring.
5. `bnkctl up` (with a *small* test cluster, not your prod one) — exercises:
   - terraform source fetch (GitHub tarball)
   - tfvars rendering (compare to a known-good `terraform.tfvars` from the existing TF repo)
   - `terraform init` against the upstream module
   - confirmation prompt
   - `terraform apply` streaming
   - auto-kubeconfig fetch
6. `bnkctl status` — should show the just-applied workspace.
7. `bnkctl logs flo` — exercises the component map (this is the most likely place for a label-selector bug).
8. `bnkctl test connectivity` / `bnkctl test dns` — pure-Go probes; should be solid if hosts are configured.
9. `bnkctl test throughput` — exercises k8s client-go fixture lifecycle. **This is the highest-risk bnkctl-local code path.**
10. `bnkctl cos instance list` — exercises Resource Controller pagination + CRN filter.
11. `bnkctl cos object put`/`get` — exercises ibm-cos-sdk-go IAM auth.
12. `bnkctl down` — exercises destroy path; verify state is cleaned up.

---

## 9. Things deliberately left undone (v1.x backlog)

These aren't bugs; they're ship-after-feedback decisions captured in PRD's Open Questions:

- **east-west iperf3 with in-cluster client pod** (current east-west uses host as client; only useful for ClusterIP-reachable test rigs)
- **`bnkctl logs --all-pods`** (multi-pod tail)
- **component-aware `bnkctl status`** (BNK pod readiness, CRD health)
- **`cos.upload:` config block + auto-orchestration in `bnkctl up`**
- **Auto-install terraform via `hashicorp/hc-install`** (currently requires terraform on PATH)
- **HMAC keys for COS auth** (currently IAM bearer only)
- **Custom `tests.yaml`** (extra hostnames, custom HTTP paths)
- **Telemetry / usage analytics** (opt-in, never on by default)

---

## 10. The "I know I don't know" list

Real unknowns I can't pre-resolve without running the code:

1. **Does the IBM container-service kubeconfig endpoint return YAML directly with `format=yaml&admin=true`, or always a ZIP?** Code handles both, but only the YAML path has been mentally walked through.
2. **Does `tfexec.Apply` actually pass `-auto-approve` automatically, or do we need `tfexec.AutoApprove(true)`?**
3. **Are the COS plan UUIDs current?** They've been stable for years but verify against the catalog.
4. **Do the BNK chart's actual deployed labels match my `bnkComponents` map?** Most likely place for a one-line fix.
5. **Does `iam.GetAPIKeysDetails` accept `IamAPIKey` or `IamApiKey`?** Trivial fix either way.

Write down what you find as you encounter it — the next round of fixes should mostly mirror this list.

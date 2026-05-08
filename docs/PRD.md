# bnkctl — Product Requirements Document

> **Status:** Draft v0.2 · 2026-05-08
> **Owner:** John Gruber (j.gruber@f5.com)

## TL;DR

`bnkctl` is a single Go binary that deploys F5 BIG-IP Next for Kubernetes (BNK) onto an IBM Cloud Red Hat OpenShift on IBM Cloud (ROKS) cluster, manages the IBM Cloud Object Storage (COS) supply chain (instances, buckets, keyed objects) BNK depends on, and validates the deployment with built-in connectivity, DNS, and throughput tests. It replaces the current `bnk` bash-script-plus-docker-runner with a cross-platform, install-once, run-anywhere CLI in the kubectl tradition.

The Terraform that does the actual provisioning continues to live in its own repo (`ibmcloud_terraform_bigip_next_for_kubernetes_2_3`) and remains the source of truth for the cluster + BNK deployment. `bnkctl` is an orchestrator, a supply-chain manager, and a test harness; it does not author Terraform. All IBM Cloud calls go through the official Go SDKs — no `ibmcloud` CLI dependency at runtime.

## Problem

The current `bnk` tool gets the job done but its shape produces recurring friction:

- **Docker-as-runtime**: every command spins a container. Slow on cold start, fragile on Windows/WSL, and the bash + busybox + bind-mount layering produced a cascade of bugs (`cp -n` semantics, `set -u` unbound vars, `/opt/tf-project` ownership) over the last few releases.
- **Bash inside the runner**: ~1100 lines of orchestration in shell. Hard to test, hard to refactor, easy to break across busybox/GNU differences.
- **No testing surface**: validating a BNK deployment after `bnk apply` is a manual exercise — `kubectl get pods`, `curl` from a jumpbox, eyeball the throughput numbers. Customers and SEs ask "is this working?" and we have no machine answer.
- **Distribution**: requires Docker installed and running. Excludes Windows users without WSL, complicates air-gapped customer environments.

## Goals

1. **One binary, three commands**: `bnkctl init`, `bnkctl up`, `bnkctl test` covers the 90% path.
2. **Drive the existing Terraform unchanged.** The TF modules in `ibmcloud_terraform_bigip_next_for_kubernetes_2_3` continue to be developed, versioned, and released independently; `bnkctl` consumes them via a pinned source URL.
3. **Built-in deployment validation.** Connectivity (curl), DNS (dig), and throughput (iperf3) tests ship with the tool, run against the just-deployed BNK, and produce structured pass/fail output.
4. **Cross-platform.** Linux, macOS, Windows (native, no WSL required). Single statically-linked binary.
5. **Sensible defaults, escape hatches everywhere.** Simple users never see a flag; power users can override anything.
6. **Idempotent and resumable.** Re-running `bnkctl up` after a partial failure picks up where it left off — same as `terraform apply` already does.
7. **Manage the COS supply chain.** First-class commands to create/delete COS instances and buckets, and to put/get/delete/list keyed objects. The customer goes from "I have an API key" to "deployed BNK" with no console clicks for the prerequisite artifacts BNK needs (FAR pull keys, JWT licenses, future per-tenant config).

## Non-goals

- **Not a Terraform IDE / authoring tool.** TF is written and tested in the TF repo. `bnkctl` is consumer-side.
- **Not a general-purpose IBM Cloud CLI.** `ibmcloud` exists for that. `bnkctl`'s scope on IBM Cloud is the BNK supply chain — ROKS for the cluster, COS for prerequisite artifacts, IAM for what BNK consumes — not Code Engine, Satellite, IKS, Watson, etc.
- **Not a general-purpose Kubernetes CLI.** `kubectl` and `oc` exist for that; `bnkctl shell` drops into a context where they Just Work.
- **Not an arbitrary workload deployer.** BNK is the workload; iperf3 / nginx test fixtures are deployed only in service of testing.
- **Not a replacement for `terraform plan`/`apply` for TF developers.** They keep using terraform directly against the TF repo.

## Target users

| Persona | Use case | Sophistication |
|---------|----------|----------------|
| F5 SE / pre-sales | Spin up a demo BNK environment on IBM Cloud, show throughput numbers to a prospect, tear down | Knows kubectl, mostly opaque on Terraform |
| F5 product engineer | Iterate on BNK builds against a shared dev cluster | Knows TF, k8s, networking deeply |
| Customer evaluator | First-time stand-up of BNK on their IBM Cloud account | May not know kubectl, follows a runbook |
| CI / automation | Deterministic deploy + test cycle, JSON output, exit codes | Headless, no TTY |

The simplest user flow must work for the customer evaluator. Power-user flags must exist for the engineer.

## The user experience

### The 3-command happy path

```
$ bnkctl init
✓ IBM Cloud API key (from $IBMCLOUD_API_KEY)
? Region [ca-tor]: us-south
? Resource group [default]:
? Create new ROKS cluster? [Y/n]: y
? Cluster name [bnk-demo]:
? Workers per zone [1]:
? OpenShift version [4.18]:
✓ Wrote ~/.bnkctl/default/config.yaml

$ bnkctl up
→ Validating IBM Cloud credentials      ✓ (j.gruber@f5.com, account "Main F5")
→ Resolving Terraform source            ✓ (github.com/jgruberf5/...@v0.6.7, latest at init time)
→ terraform init                        ✓
→ terraform plan: +47 / ~0 / -0
? Apply this plan? [y/N]: y
→ terraform apply                       (this will take ~25 minutes)
  ████████████████████░░░░  82%   modules/flo (helm install f5_lifecycle_operator)
✓ BNK deployed
  cluster:    bnk-demo (us-south)
  console:    https://...
  cneinstance: 1 ready, 3/3 TMM pods running

$ bnkctl test
→ DNS                                   ✓ ingress.bnk-demo... resolves to 169.x.x.x
→ Connectivity                          ✓ HTTPS 200 in 184ms
→ Throughput                            ✓ 9.42 Gbit/s (iperf3, 30s, 8 streams)
✓ All tests passed (3/3)
```

Three commands. No flags. No reading docs. Output is brief but informative; verbose details available with `-v`.

### Tear-down

```
$ bnkctl down
? This will destroy cluster "bnk-demo" and all BNK resources. Continue? [y/N]: y
→ terraform destroy                     ✓
✓ All resources removed
```

### Iteration loop (engineer)

```
$ bnkctl plan                # read-only, what would change
$ bnkctl apply               # apply without re-init prompts
$ bnkctl logs flo            # tail logs of the lifecycle operator
$ bnkctl shell               # drop into a cluster-context bash with kubectl/oc/ibmcloud
$ bnkctl test throughput     # rerun just the perf test
```

### Multi-environment (workspaces)

Each workspace is a named, isolated config + state bundle under `~/.bnkctl/<name>/`. The default workspace is `default`. Switch with `--workspace` or `-w`:

```
$ bnkctl -w prod up
$ bnkctl -w demo up
$ bnkctl workspaces list
NAME      CLUSTER         REGION     STATE
default   bnk-demo        us-south   applied
prod      bnk-prod-1      ca-tor     applied
demo      bnk-canada      ca-tor     destroyed
```

### Pointing at a local Terraform checkout (engineer)

```
$ bnkctl up --tf-source ~/code/ibmcloud_terraform_bigip_next_for_kubernetes_2_3
```

Bypasses the pinned release; uses the local working tree. Used when iterating on TF and `bnkctl` together.

## Command surface

```
bnkctl init                      Interactive setup; writes workspace config.yaml
bnkctl up [--auto]               plan + apply; the everyday deploy command
bnkctl plan                      Read-only; show what would change
bnkctl apply [--auto]            Apply without re-prompting (assumes config exists)
bnkctl down [--auto]             Destroy everything in the workspace
bnkctl status                    Summary: cluster, components, last apply timestamp
bnkctl logs <component> [-f]     Tail logs (flo / cis / cert-manager / cneinstance)
bnkctl shell                     Interactive bash with kubectl/oc/ibmcloud on PATH
bnkctl exec <cmd>...             Run a command with cluster context loaded
bnkctl kubeconfig [--export]     Print the workspace's kubeconfig path or contents
bnkctl kubectl <args>...         Passthrough to local kubectl with workspace KUBECONFIG loaded
bnkctl oc <args>...              Passthrough to local oc with workspace KUBECONFIG loaded
bnkctl ibmcloud <args>...        Passthrough to local ibmcloud with workspace API key + region loaded
bnkctl test [suite]              Run tests; default suite is "all"
bnkctl test connectivity         HTTP(S) reachability against deployed services
bnkctl test dns                  DNS resolution of ingress and service hostnames
bnkctl test throughput           iperf3 throughput, deploys server pod automatically
bnkctl test list                 Available test suites
bnkctl workspaces list           List workspaces and their states
bnkctl workspaces use <name>     Set default workspace
bnkctl workspaces delete <name>  Delete workspace (refuses if state is non-empty)
bnkctl ws ...                    Short alias for `workspaces`
bnkctl cos instance create <name>                     Create a COS instance
bnkctl cos instance delete <name>                     Delete a COS instance
bnkctl cos instance list                              List COS instances in account
bnkctl cos bucket create <bucket> --instance <name>   Create a bucket on the named instance
bnkctl cos bucket delete <bucket> --instance <name>   Delete a bucket
bnkctl cos bucket list --instance <name>              List buckets on the named instance
bnkctl cos object put <bucket>/<key> <local-file>     Upload (multipart for large files, streaming)
bnkctl cos object get <bucket>/<key> <local-file>     Download (streaming)
bnkctl cos object delete <bucket>/<key>               Delete an object
bnkctl cos object list <bucket>[/<prefix>]            List objects (optionally under a prefix)
bnkctl version                   Print version + tf-source pin
bnkctl self update               Pull the latest bnkctl release
bnkctl completion <shell>        Print shell completion script
bnkctl doctor                    Check prerequisites and report missing pieces
```

Global flags: `-w/--workspace`, `-v/--verbose`, `-q/--quiet`, `-o/--output {text|json}`, `--no-color`.

## The test surface in detail

The test suite ships *with* `bnkctl` so that "did the deployment succeed" has a machine-readable answer.

### `bnkctl test connectivity`
- Discovers the deployed BNK service endpoints from cluster state (annotations on the CNEInstance / its services).
- For each endpoint, performs an HTTP GET (or HTTPS with cert validation as configured).
- Pass criteria: 2xx response within timeout, TLS handshake completes, optionally a body match.
- Implemented with Go's `net/http` — no external `curl` dependency.

### `bnkctl test dns`
- Looks up the cluster ingress hostname, the BNK GSLB datacenter hostname (if configured), and any user-specified hosts in `config.yaml`.
- Verifies records resolve, A/AAAA values are sane (not 127.x.x.x, not RFC1918 unless expected).
- Implemented with Go's `net.Resolver` — no external `dig`.

### `bnkctl test throughput`
- Two modes, selected via `--mode {north-south,east-west}` (default `north-south`):
  - **north-south**: deploys an `iperf3 -s` pod behind a Service that exercises BNK's data path (typically LoadBalancer / TMM VIP). Client runs on the bnkctl host. This is what the demo and customer-evaluation flows care about — measures the data plane BNK is meant to accelerate.
  - **east-west**: deploys both client and server as in-cluster pods. Useful for triage ("is BNK slow, or is the cluster slow?"). `--cross-node` forces client and server onto different nodes.
- Server image is configurable (`test.throughput.image:` in `config.yaml`); default `networkstatic/iperf3:latest` (public, no F5 supply chain to manage). Customers with a mirror or F5-built image override.
- Reports peak / average throughput, retransmits, jitter.
- Tears down the iperf3 pod + service unless `--keep` is passed.
- iperf3 client binary required on PATH locally; doctor flags if missing. (Server side always runs in-cluster via the image.)

### `bnkctl test all`
- Runs the three above sequentially with structured output (table by default, JSON with `-o json`).
- Exit code 0 on all-pass, 1 on any-fail. CI-friendly.

### Custom test plans (later)
A v2 feature: declarative `tests.yaml` in the workspace describing additional probes (extra hostnames, custom HTTP paths, larger iperf3 durations).

## Configuration

### Per-workspace config
File: `~/.bnkctl/<workspace>/config.yaml`

```yaml
ibmcloud:
  region: us-south
  resource_group: default
  api_key_source: env  # env | keychain | prompt
cluster:
  create: true
  name: bnk-demo
  openshift_version: "4.18"
  workers_per_zone: 1
bnk:
  cneinstance_size: Small
  far_repo_url: repo.f5.com
  manifest_version: "2.3.0-..."
  # ... only the fields a customer would actually edit
test:
  throughput:
    image: networkstatic/iperf3:latest   # override with a mirror or F5-built image
    duration: 30
    streams: 8
    default_mode: north-south            # or east-west
  connectivity:
    extra_hosts: []
tf_source:
  type: github
  repo: jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3
  ref: v0.6.7   # resolved at init time from latest release; bump with `bnkctl init --upgrade-tf`

# Optional, future (see "Out-of-scope for v1"). v1 ships the primitives via
# `bnkctl cos object put …`; this block is the eventual auto-orchestration shape.
# cos:
#   instance: bnk-orchestration
#   bucket: bnk-schematics-resources
#   upload:
#     - source: ./pull-keys/non-ga-prod-pull-key.tgz
#       key: non-ga-prod-pull-key.tgz
#     - source: ./licenses/test-terraform.jwt
#       key: test-terraform.jwt
```

`bnkctl init` produces this file via interactive prompts. Sensible defaults are picked for everything; users only see prompts for unavoidable choices (region, cluster name, create-new-or-attach).

### Global config
File: `~/.bnkctl/config.yaml` — non-secret defaults (default workspace, telemetry on/off, color/output preferences).

### State
Directory: `~/.bnkctl/<workspace>/state/`
- `terraform.tfstate` (driven by hashicorp/terraform-exec)
- `kubeconfig`
- `scratch/` (transient downloads, FAR tarballs, kubeconfigs fetched via the IBM container-services SDK)

State location is fixed per workspace and not user-relocatable in v1 (kept simple). v2 may add remote state backends.

### Secrets

API key resolution order, all in v1:

1. `IBMCLOUD_API_KEY` env var — honored without prompting.
2. OS keychain (per-workspace entry: `bnkctl/<workspace>/ibmcloud_api_key`):
   - macOS: Keychain via `zalando/go-keyring`.
   - Linux: libsecret via `zalando/go-keyring` (gnome-keyring, kwallet, KeePassXC, etc.).
   - Windows: Credential Manager via `zalando/go-keyring`.
3. Interactive prompt (only on TTY) — offers to save to keychain after.
4. Hard error.

Plaintext in `config.yaml` is rejected at load time. `--api-key-from {env|keychain|prompt}` pins the source for one invocation.

## Terraform integration

- **Source**: pinned to a release tag of the TF repo by default. Resolved at `bnkctl init` time and persisted in `config.yaml`'s `tf_source` block. Re-run `bnkctl init --upgrade-tf` (or edit the file) to bump.
- **Driver**: `hashicorp/terraform-exec` Go library wraps the `terraform` binary. Streams plan/apply progress to bnkctl's UI.
- **Terraform binary**: required on PATH. `bnkctl doctor` reports the version and a clear install hint if missing. (v2 may auto-install via `hashicorp/hc-install` into `~/.bnkctl/bin/`.)
- **Module fetching**: `terraform init` handles it. `bnkctl` does no source resolution itself beyond writing the right `source = "..."` for the root module.
- **Variables**: bnkctl translates `config.yaml` into `terraform.tfvars` in the workspace state dir before each apply. Users do not edit `terraform.tfvars` directly; the file is treated as build output.

## IBM Cloud integration

- **Approach**: Go SDKs only. `bnkctl` does not shell out to the `ibmcloud` CLI for any of its operations — `ibmcloud` is not a runtime dependency. (Users can still install it; `bnkctl shell` will load credentials so it works.)
- **SDKs**:
  - [`IBM/platform-services-go-sdk`](https://github.com/IBM/platform-services-go-sdk) — IAM (auth verification, identity), Resource Controller (COS instance CRUD, generic resource lookup), Resource Manager (resource group resolution).
  - [`IBM/container-services-go-sdk`](https://github.com/IBM/container-services-go-sdk) — IKS / ROKS cluster config download, cluster status.
  - [`IBM/ibm-cos-sdk-go`](https://github.com/IBM/ibm-cos-sdk-go) — COS bucket and object I/O. S3-compatible (literally a fork of the AWS S3 SDK with IBM auth bolted in); handles multipart uploads, streaming downloads, retries.
- **Why not the CLI**: avoids the plugin / state / version-drift class of bugs that consumed v0.6.4 – v0.6.6 (plugins not visible to non-root user, busybox `cp -n` breaking the seed, deprecation banners on every login). Output parsing is brittle. Binary I/O via shell-out is awkward (multipart, progress, streaming). SDKs add ~5 – 10 MB to the binary, well within the < 30 MB budget.
- **Auth model**: API key (env or workspace) → IAM bearer. COS uses IAM bearer auth by default; HMAC keys are an opt-in for users wiring third-party S3-compatible tooling at the same buckets.

## External dependencies at runtime

| Tool | Required for | If missing |
|------|--------------|------------|
| `terraform` | All deploy operations | doctor reports + clear install hint |
| `iperf3` (client) | `bnkctl test throughput` | doctor reports; throughput skipped, others run |
| `oc` (optional) | `bnkctl shell --oc` (preferred shell mode) | falls back to kubectl |
| `kubectl` (optional) | `bnkctl shell --kubectl` | bundled client-go covers most ops |
| `ibmcloud` | **not required at runtime** | bnkctl uses Go SDKs (`platform-services-go-sdk`, `container-services-go-sdk`, `ibm-cos-sdk-go`) for everything it does on IBM Cloud — auth, cluster config, COS instance/bucket/object I/O |

`bnkctl shell` deserves a note: when a user wants a real interactive shell with full IBM Cloud + OpenShift CLIs, those CLIs do need to be installed on the host. `bnkctl shell` doesn't bundle them, but it does set `KUBECONFIG`, `IBMCLOUD_API_KEY`, etc. correctly so that whichever CLIs the user has installed Just Work.

## Distribution

- **Releases**: GitHub Releases via goreleaser. Cross-compiled to `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}`.
- **Install methods**:
  - `brew install jgruberf5/tap/bnkctl` (homebrew tap)
  - `scoop install bnkctl` (Windows)
  - `curl -fsSL .../install.sh | bash` (mirrors current bnk pattern)
  - `go install github.com/jgruberf5/bnkctl/cmd/bnkctl@latest` (Go users)
- **Self-update**: `bnkctl self update` downloads the latest release matching the host arch, replaces the binary in place. Optional, never automatic.
- **Binary size budget**: target < 30 MB stripped. client-go pulls weight; acceptable.

## Architecture sketch (high level)

```
cmd/bnkctl/             cobra root, command files
internal/
  config/               workspace + global config, secrets resolution
  tf/                   hashicorp/terraform-exec wrapper, tfvars rendering
  ibm/                  IAM, Resource Controller (incl. COS instance CRUD),
                        IKS/ROKS cluster config — IBM/platform-services-go-sdk
                        + IBM/container-services-go-sdk
  cos/                  COS bucket and object I/O — IBM/ibm-cos-sdk-go
                        (S3-compatible, streaming, multipart)
  k8s/                  client-go for bnkctl's own ops (apply iperf3 pod, fetch
                        logs, watch readiness). `bnkctl kubectl` / `bnkctl oc`
                        passthrough commands shell to the local install with
                        the workspace's KUBECONFIG loaded — they are not
                        required to be installed for bnkctl's internal work.
  test/
    connectivity/       net/http probes
    dns/                net.Resolver probes
    throughput/         iperf3 client + cluster-side server lifecycle
  ui/                   spinners, tables, JSON output, log levels
  doctor/               prerequisite checks
```

Cobra for CLI structure (kubectl-like). Bubbletea or simpler for the progress UI. zerolog or slog for structured logs.

## Out-of-scope for v1, on the radar for v2

- Remote Terraform state backends (S3, COS, Terraform Cloud)
- Custom test plan files (`tests.yaml`)
- Auto-install of terraform binary into `~/.bnkctl/bin/`
- Multi-cluster workspaces (one workspace = many clusters)
- Telemetry / opt-in usage analytics
- A web UI / TUI dashboard
- Plugin model (`bnkctl plugin install <thing>`)
- Auto-upload of FAR pull key + JWT from local files via a `cos.upload:` block in `config.yaml` — `bnkctl up` orchestrating the COS supply chain end-to-end. v1 ships the primitives (`bnkctl cos object put …`); the orchestration glue follows once usage patterns settle.
- HMAC keys for COS auth (v1 uses IAM bearer; HMAC opt-in for third-party S3 tooling against the same buckets).
- Air-gapped install bundle (binary + terraform + container images in one tarball)

## Open questions

1. **Failure recovery**: when `terraform apply` fails halfway, `bnkctl up` re-runs apply transparently. But what about logical failure modes — pods crash-looping, CRD not ready — that don't surface in TF? `bnkctl status` should detect these; do we surface them as deploy-time failures, or only when `bnkctl test` runs?
2. **What does `bnkctl` do when the TF repo's contract changes** (new required variable in some future tag)? `bnkctl init --upgrade-tf` re-prompts for any new fields; old config without them gets a clear error pointing at the new key. Worth a small RFC.
3. **`bnkctl up` and the COS supply chain**: should `up` (driven by a `cos:` block in `config.yaml`) auto-create the COS instance + bucket and auto-upload the FAR pull key + JWT, or keep that as an explicit prerequisite via `bnkctl cos …` commands? v1 has the primitives either way; the question is whether the 3-command happy path includes COS prep or assumes it's been done.
4. **`bnkctl status` shape**: what fields and how deep? Cluster + namespace + component status is the minimum; should it also probe BNK CRDs (CNEInstance.status, License.status), or stop at "are the pods healthy"?

### Decided

| Decision | Outcome | Date |
|----------|---------|------|
| Naming | `bnkctl` | 2026-05-08 |
| Repo URL | `github.com/jgruberf5/bnkctl` | 2026-05-08 |
| License | MIT | 2026-05-08 |
| State directory | `~/.bnkctl/<workspace>/` | 2026-05-08 |
| Default workspace | literal `default` | 2026-05-08 |
| TF source pinning | latest release at init time, written into `config.yaml`, bumped via `bnkctl init --upgrade-tf` | 2026-05-08 |
| TF tag scheme | one unified tag stream (TF + bnk image share tags) | 2026-05-08 |
| IBM Cloud integration | Go SDKs only (`platform-services-go-sdk`, `container-services-go-sdk`, `ibm-cos-sdk-go`); no `ibmcloud` CLI runtime dep | 2026-05-08 |
| Kubernetes integration | Hybrid — `client-go` for internals, `bnkctl kubectl` / `bnkctl oc` passthrough for user verbs | 2026-05-08 |
| v1 probes | All three: connectivity + DNS + throughput | 2026-05-08 |
| iperf3 fixture image | Configurable; default `networkstatic/iperf3:latest` | 2026-05-08 |
| Throughput shape | Both north-south (default) and east-west (`--mode east-west`) | 2026-05-08 |
| Secrets | Env or OS keychain on day 1 (zalando/go-keyring) | 2026-05-08 |
| Cluster lifecycle | Both create-new and attach-to-existing | 2026-05-08 |
| Self update | `bnkctl self update` ships in v1.0 | 2026-05-08 |
| JSON output | `-o json` in v1.0 with versioned schema (`"schema":"bnkctl.v1"`) | 2026-05-08 |
| COS scope | Full instance/bucket/object CRUD via Go SDK | 2026-05-08 |

## Success metrics

- Customer evaluator can go from `curl install.sh` to passing `bnkctl test` on a fresh laptop in < 35 minutes (most of which is waiting for ROKS to provision).
- F5 SE can spin up a demo, run throughput, tear down, and have all artefacts gone in 3 commands.
- Number of GitHub issues citing busybox / docker / WSL trends to zero (compared with bnk's recent v0.6.x release cadence).
- One binary download serves Linux, macOS, and Windows users with no platform-specific docs.

## Appendix: relationship to `bnk`

`bnk` (the current tool) and `bnkctl` (this PRD) coexist during a transition window. `bnk` continues to receive bug fixes; `bnkctl` is the recommended path for new users from v1.0 onward. The TF repo is unchanged by either: it's the shared source of truth.

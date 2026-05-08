# bnkctl

A single-binary CLI to deploy F5 BIG-IP Next for Kubernetes (BNK) onto IBM Cloud ROKS, manage its IBM Cloud Object Storage supply chain, and validate the deployment with built-in connectivity / DNS / throughput tests.

> **Status:** Pre-release. Source compiles, unit tests pass, every PRD verb is implemented. Real-cluster shake-out items are tracked in [docs/SHAKEOUT.md](docs/SHAKEOUT.md). No tagged release yet — install via build-from-source.

`bnkctl` is the cross-platform Go successor to `bnk` (bash + Docker). It drives the existing Terraform in [`ibmcloud_terraform_bigip_next_for_kubernetes_2_3`](https://github.com/jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3) — that repo stays the source of truth for the deployment; `bnkctl` is the orchestrator + test harness on top.

## Highlights

- **3-command happy path** — `bnkctl init` → `bnkctl up` → `bnkctl test`. Customer evaluators go from "I have an API key" to "deployed BNK with a passing throughput test" without touching the IBM Cloud web console.
- **Full lifecycle** — `up` / `plan` / `apply` / `down` with auto-resolved Terraform source, automatic post-apply admin-kubeconfig fetch, and idempotent re-runs.
- **Built-in test suite** — DNS, HTTP/HTTPS connectivity (no external `curl` / `dig` deps), iperf3 throughput against an in-cluster fixture deployed and torn down automatically. Versioned JSON output (`bnkctl.v1`) for CI.
- **First-class COS supply chain** — `cos instance/bucket/object` CRUD via official IBM Go SDKs, multipart upload / streaming download for large objects.
- **Workspaces** — kubectl-style per-environment config + state bundles under `~/.bnkctl/<name>/`. Switch with `bnkctl ws use`, override one-off with `-w`.
- **Cross-platform single binary** — Linux, macOS, Windows. No Docker dependency. ~25 MB statically linked.
- **No `ibmcloud` CLI dependency** — IBM Go SDKs (platform-services / container-services / cos) cover everything internally.

---

## Quick start (build from source today; pre-built binaries soon)

```bash
git clone https://github.com/jgruberf5/bnkctl.git
cd bnkctl
make build
export PATH="$PWD/bin:$PATH"

bnkctl doctor      # check prereqs (terraform, iperf3, kubeconfig, IBM creds)
bnkctl init        # interactive — region, RG, cluster, OpenShift version
bnkctl up          # plan + confirm + apply + auto-fetch admin kubeconfig
bnkctl test        # DNS + connectivity + throughput
```

---

## Features

### Lifecycle (deploy + manage BNK)

| Command | Description |
|---|---|
| `bnkctl init [--upgrade-tf] [--tf-source PATH]` | Interactive setup. Verifies IBM Cloud credentials, resolves the resource group, pins the latest Terraform release, writes `~/.bnkctl/<workspace>/config.yaml`. |
| `bnkctl up [--auto] [--no-kubeconfig]` | The everyday deploy: `terraform plan` → confirm (unless `--auto`) → `terraform apply` → fetch admin kubeconfig to `~/.kube/config`. |
| `bnkctl plan` | Read-only diff. Never prompts. |
| `bnkctl apply [--auto] [--no-kubeconfig]` | Direct apply for CI / scripted flows. Skips the plan-and-confirm gate. |
| `bnkctl down [--auto]` | `terraform destroy` with confirmation gate. |

The Terraform source is pinned at `init` time to the latest release tag of [`ibmcloud_terraform_bigip_next_for_kubernetes_2_3`](https://github.com/jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3). Bump it later with `bnkctl init --upgrade-tf`. Use `--tf-source ./path-to-local-checkout` to develop against a local TF working tree.

### Workspaces (kubectl-style per-environment isolation)

| Command | Description |
|---|---|
| `bnkctl ws list` | Table of workspaces; `*` marks current. Shows region / cluster / TF source. |
| `bnkctl ws current` | Print current workspace name. |
| `bnkctl ws use <name>` | Set the persistent current-workspace pointer. |
| `bnkctl ws new <name>` | Create an empty workspace skeleton. |
| `bnkctl ws delete <name> [--force]` | Remove. Refuses if Terraform state lists resources unless `--force`. Cleans the keychain entry. |
| `-w/--workspace <name>` | Per-command override. Doesn't touch the persistent pointer. |

### COS supply chain

| Command | Description |
|---|---|
| `bnkctl cos instance list` | List COS service instances in the account. |
| `bnkctl cos instance create <name> [--plan standard\|lite] [--plan-id UUID]` | Create a COS instance under the workspace's resource group. |
| `bnkctl cos instance delete <name> [--auto] [--no-recursive]` | Delete an instance and its bound resources. |
| `bnkctl cos bucket create <bucket> --instance <name> [--class standard]` | Create a bucket on the named instance. Storage class configurable. |
| `bnkctl cos bucket delete <bucket> --instance <name>` | Delete a (must-be-empty) bucket. |
| `bnkctl cos bucket list --instance <name>` | List buckets on the instance. |
| `bnkctl cos object put <bucket>/<key> <local-file> --instance <name>` | Upload — multipart for large files, streaming. |
| `bnkctl cos object get <bucket>/<key> <local-file> --instance <name>` | Streaming download. Removes partial files on failure. |
| `bnkctl cos object delete <bucket>/<key> --instance <name>` | Delete an object. |
| `bnkctl cos object list <bucket>[/<prefix>] --instance <name>` | List objects (optionally under a prefix). |

`--instance` accepts either a friendly name or a CRN.

### Cluster ops (post-deploy)

| Command | Description |
|---|---|
| `bnkctl status` | Workspace + region + cluster + TF source + last-apply timestamp + cluster reachability (node ready count). |
| `bnkctl logs <component> [-f]` | Tail logs for `flo` / `cis` / `cert-manager` / `cneinstance`. Component → namespace + label selector mapping is hardcoded against the upstream chart's defaults. |
| `bnkctl kubeconfig` | Print kubeconfig path. |
| `bnkctl kubeconfig --download [--cluster X]` | Fetch admin kubeconfig from IBM Cloud and write to `$KUBECONFIG` / `~/.kube/config` at mode 0600. |
| `bnkctl kubeconfig --export` | Print kubeconfig contents to stdout. |
| `bnkctl shell` | Interactive `$SHELL` subshell with `KUBECONFIG`, `IBMCLOUD_API_KEY`, `IC_API_KEY`, `IBMCLOUD_REGION` exported. |
| `bnkctl exec <command...>` | One-shot run with the same env loaded. |
| `bnkctl kubectl <args...>` | Passthrough to local `kubectl` with workspace credentials loaded. |
| `bnkctl oc <args...>` | Passthrough to local `oc`. |
| `bnkctl ibmcloud <args...>` | Passthrough to local `ibmcloud`. |

### Built-in deployment validation

| Command | Description |
|---|---|
| `bnkctl test [suite]` | Run `connectivity` / `dns` / `throughput`. Bare `test` runs `all` (DNS + connectivity in v1). |
| `bnkctl test connectivity [--insecure]` | HTTP/HTTPS reachability of hosts in `test.connectivity.extra_hosts`. Built-in `net/http` — no external `curl`. `--insecure` skips TLS validation. |
| `bnkctl test dns` | DNS resolution via Go's `net.Resolver` — no external `dig`. |
| `bnkctl test throughput [--mode north-south\|east-west] [--keep]` | Deploys an `iperf3 -s` pod (image configurable) into the `bnkctl-test` namespace, exposes via LoadBalancer (north-south) or ClusterIP (east-west), runs `iperf3 -c` from the host, parses `-J` JSON output. Tears down on exit unless `--keep`. |
| `bnkctl test list` | List available suites. |
| `bnkctl test -o json` | Versioned JSON output (`{"schema":"bnkctl.v1", ...}`) for CI consumers. Exit 0 on all-pass, 1 on any-fail. |

### Operations + meta

| Command | Description |
|---|---|
| `bnkctl doctor` | Eight-check prereq + credentials report: `terraform` / `iperf3` / `kubectl` / `oc` / `ibmcloud` on PATH, kubeconfig present, workspace initialised, API key resolves, IBM Cloud auth works. Exits non-zero on failures (warnings don't block). |
| `bnkctl version` | Version + commit + build date (populated via `-ldflags`). |
| `bnkctl self update` | Pull the latest GitHub release tarball, verify SHA256 against `checksums.txt`, atomic-replace the running binary. Linux/macOS only. |
| `bnkctl completion {bash\|zsh\|fish\|powershell}` | Print shell completion script (cobra built-in). |
| `-o json`, `--no-color`, `-v/--verbose`, `-q/--quiet` | Global output flags. |

### Configuration model

- **Per-workspace:** `~/.bnkctl/<workspace>/config.yaml` — region, resource group, cluster details, BNK options, TF source pin, test settings.
- **Global:** `~/.bnkctl/config.yaml` — `current_workspace` pointer + UI defaults.
- **State:** `~/.bnkctl/<workspace>/state/` — `terraform.tfstate`, kubeconfig, scratch downloads.
- **Override base dir:** `BNKCTL_HOME=/path/to/state` env var.
- **Secrets:** `IBMCLOUD_API_KEY` env var or OS keychain (macOS Keychain / libsecret / Windows Credential Manager via `zalando/go-keyring`). Plaintext API keys in `config.yaml` are rejected at load time.

---

## Build from source

### Requirements

- **Go 1.23+** — `go-version-file: go.mod` is the source of truth; check `go.mod` for the current minimum.
- **terraform** on `PATH` (>= 1.5) — required at runtime for `up` / `plan` / `apply` / `down`.
- **iperf3** on `PATH` — required for `bnkctl test throughput`.
- (Optional) **kubectl / oc / ibmcloud** — required only for the corresponding passthrough commands and `bnkctl shell`.

### Native build

```bash
git clone https://github.com/jgruberf5/bnkctl.git
cd bnkctl

# Fetch deps + populate go.sum (first time only).
go mod tidy

# Build the binary into ./bin/bnkctl
make build

# Or directly:
go build -o bin/bnkctl ./cmd/bnkctl

# Run from anywhere:
sudo install -m 0755 bin/bnkctl /usr/local/bin/bnkctl
# OR add ./bin to PATH:
export PATH="$PWD/bin:$PATH"

bnkctl --help
```

### Make targets

```
make build      # go build -ldflags ... -o bin/bnkctl ./cmd/bnkctl
make test       # go test ./...
make vet        # go vet ./...
make tidy       # go mod tidy
make run        # build + ./bin/bnkctl --help
make clean      # rm -rf bin/
```

`VERSION` / `COMMIT` / `DATE` are passed via `-ldflags`. Override:

```bash
make build VERSION=v0.1.0
./bin/bnkctl version
# bnkctl v0.1.0 (commit abc1234, built 2026-05-08T...)
```

### Build inside Docker (no host Go required)

```bash
docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  golang:1.23-alpine sh -c 'go mod tidy && go build -o bin/bnkctl ./cmd/bnkctl'
```

Produces a Linux binary in `bin/bnkctl`. For other targets (macOS, Windows) cross-compile inside the same container with `GOOS=darwin GOARCH=arm64 go build ...` or use `goreleaser release --snapshot --clean` (which the release workflow does for tagged releases).

### Cross-compilation

```bash
# macOS arm64
GOOS=darwin GOARCH=arm64 go build -o bin/bnkctl-darwin-arm64 ./cmd/bnkctl

# Windows amd64
GOOS=windows GOARCH=amd64 go build -o bin/bnkctl.exe ./cmd/bnkctl

# Full sweep (mirror of what goreleaser produces on tag push):
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    ext=""
    [ "$os" = "windows" ] && ext=".exe"
    GOOS=$os GOARCH=$arch go build -o "bin/bnkctl_${os}_${arch}${ext}" ./cmd/bnkctl
  done
done
```

### Tests

```bash
make test           # all packages
go test -race ./internal/config/...    # one package
go test -v -run TestNew ./internal/config/...   # one test
```

The `internal/ibm` package has integration tests that skip unless `IBMCLOUD_API_KEY` is set:

```bash
IBMCLOUD_API_KEY=... go test ./internal/ibm/...
```

---

## Layout

```
bnkctl/
├── cmd/bnkctl/                # main package — calls cli.Execute()
├── internal/
│   ├── cli/                   # cobra command tree (15 files, every verb wired)
│   ├── config/                # workspace + global YAML, secrets via go-keyring
│   ├── tf/                    # terraform-exec wrapper, GitHub source fetch, tfvars render
│   ├── ibm/                   # IAM, Resource Manager, Resource Controller, container-service
│   ├── cos/                   # IBM/ibm-cos-sdk-go bucket + object I/O
│   ├── k8s/                   # client-go + iperf3 fixture lifecycle
│   ├── test/                  # dns + connectivity + throughput probes, bnkctl.v1 JSON
│   ├── doctor/                # prereq + creds checks
│   └── ui/                    # (placeholder)
├── docs/
│   ├── PRD.md                 # product spec, 16 design decisions captured
│   └── SHAKEOUT.md            # first-build verification checklist
├── .github/workflows/
│   ├── ci.yml                 # vet + test + build + goreleaser check on PR/push
│   └── release.yml            # goreleaser on tag push → GitHub Release with binaries
├── .goreleaser.yml            # cross-compile sweep config
├── Makefile
├── go.mod
└── LICENSE
```

---

## Key dependencies

| Module | Purpose |
|---|---|
| [`github.com/spf13/cobra`](https://github.com/spf13/cobra) | CLI framework |
| [`github.com/hashicorp/terraform-exec`](https://github.com/hashicorp/terraform-exec) | Drive `terraform init/plan/apply/destroy` |
| [`github.com/IBM/go-sdk-core/v5`](https://github.com/IBM/go-sdk-core) | IAM authenticator (shared base) |
| [`github.com/IBM/platform-services-go-sdk`](https://github.com/IBM/platform-services-go-sdk) | IAM Identity, Resource Manager, Resource Controller |
| [`github.com/IBM/ibm-cos-sdk-go`](https://github.com/IBM/ibm-cos-sdk-go) | S3-compatible bucket + object I/O |
| [`k8s.io/client-go`](https://github.com/kubernetes/client-go) | Kubernetes API for iperf3 fixture lifecycle + log streaming |
| [`github.com/zalando/go-keyring`](https://github.com/zalando/go-keyring) | Cross-platform OS keychain (macOS / libsecret / Windows Credential Manager) |
| [`gopkg.in/yaml.v3`](https://gopkg.in/yaml.v3) | Workspace + global config YAML |

---

## Documentation

- [`docs/PRD.md`](docs/PRD.md) — product requirements, full UX spec, command surface, configuration schema, every design decision with rationale.
- [`docs/SHAKEOUT.md`](docs/SHAKEOUT.md) — first-build verification checklist: SDK method-name confidence ratings, hardcoded values to verify (COS plan UUIDs, BNK component label selectors), real-cluster verification items, smoke-test order.

---

## Project status

- ✅ Every PRD verb has real implementation (no stubs in production code paths).
- ✅ `go vet`, `go build`, `go test ./...` all pass on CI (Linux ubuntu-latest).
- ✅ Cross-compiles for `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}` via goreleaser.
- ⏳ No tagged release yet — install via build-from-source.
- ⏳ Hardcoded values (BNK component labels, COS plan UUIDs, container-service kubeconfig endpoint shape) need real-cluster verification — see [`docs/SHAKEOUT.md`](docs/SHAKEOUT.md).
- ⏳ Pre-built binaries, brew tap, scoop bucket, install.sh — land with the first tagged release.

---

## What this is *not*

- Not a Terraform authoring tool. Terraform lives in its own repo and is the source of truth for the deployment shape.
- Not a general-purpose IBM Cloud CLI. `ibmcloud` covers that. `bnkctl`'s scope on IBM Cloud is the BNK supply chain — ROKS for the cluster, COS for prerequisite artefacts (FAR pull keys, JWT licenses), IAM for what BNK consumes.
- Not a general-purpose Kubernetes CLI. `kubectl` and `oc` cover that. `bnkctl shell` and the `bnkctl kubectl` / `bnkctl oc` passthroughs make their context easy to load.
- Not an arbitrary workload deployer. BNK is the workload; the iperf3 / nginx test fixtures exist only to validate it.

---

## Contributing

Follows standard Go conventions. PRs run CI (vet + test -race + build + goreleaser check) automatically. Read [`docs/PRD.md`](docs/PRD.md) before proposing changes to the command surface or configuration schema — there's a "Decided" table at the bottom that's the binding contract for v1.

---

## License

[MIT](LICENSE).

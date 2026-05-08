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

> **Build requires Go 1.23 or newer.** If you don't have a recent Go on PATH, use the [Docker-based build](#build-with-docker-no-go-installation-required) — same result, no host Go needed.

```bash
git clone https://github.com/jgruberf5/bnkctl.git
cd bnkctl

# Path A — native build (requires Go 1.23+):
go version       # confirm: go version go1.23.x or newer
make build

# Path B — Docker build (no host Go installation required):
docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  golang:1.23-alpine sh -c 'go mod tidy && go build -o bin/bnkctl ./cmd/bnkctl'

export PATH="$PWD/bin:$PATH"

bnkctl doctor      # check prereqs (terraform, iperf3, kubeconfig, IBM creds)
bnkctl init        # interactive — region, RG, cluster, OpenShift version
bnkctl up          # plan + confirm + apply + auto-fetch admin kubeconfig
bnkctl test        # DNS + connectivity + throughput
```

---

## Full deployments — for people who already know Terraform

If you live in `terraform plan` / `apply` / `destroy` and want to know *exactly* what bnkctl is doing for you and where the escape hatches are, this section is the long-form walkthrough.

### What bnkctl does on top of Terraform

| Concern | Raw Terraform | bnkctl |
|---|---|---|
| **Source code** | `git clone`, `git checkout <tag>` | `bnkctl init` pins to a release tag; `bnkctl init --upgrade-tf` bumps |
| **Working directory** | wherever you `cd`'d | `~/.bnkctl/<workspace>/state/tf-source/<repo>-<tag>/` (per-workspace, cached) |
| **State storage** | `terraform.tfstate` next to the .tf files (or a backend block) | bnkctl writes a `bnkctl_backend_override.tf` configuring `backend "local" { path = ~/.bnkctl/<ws>/state/terraform.tfstate }` |
| **`.terraform/`** | next to the .tf files | `TF_DATA_DIR=~/.bnkctl/<ws>/state/terraform/` (out of the source tree) |
| **API key** | `ibmcloud_api_key = "..."` in tfvars (plaintext on disk) | `IBMCLOUD_API_KEY` env / OS keychain / base64 in `config.yaml` / interactive prompt — **never written to disk in plaintext.** Passed to terraform via `TF_VAR_ibmcloud_api_key` env so it doesn't land in argv either. |
| **`terraform.tfvars`** | hand-write or copy from `*.example` | `bnkctl tfvars` copies `terraform.tfvars.example` from the pinned source. `--var-file` then layers your edits on top of bnkctl's auto-rendered tfvars (config.yaml-derived). |
| **Pre-flight dirs** | upstream module assumes `/work/.bnk/scratch/...` exists (the bnk runner image's bind-mount) | bnkctl pre-creates `<state>/kubeconfig/{cert_manager,cne_instance,flo,license}` and `<state>/scratch/f5-manifest`. Renders `kubeconfig_dir` and `scratch_dir` overrides into the auto-tfvars to point the upstream there. |
| **`terraform init`** | run manually | run automatically as part of every `bnkctl up` / `plan` / `apply` / `destroy`, with `-reconfigure` so backend transitions land cleanly |
| **`terraform plan` summary** | full resource diff | full resource diff (bnkctl streams terraform's stdout/stderr verbatim) |
| **Confirmation gate** | `-auto-approve` flag or interactive prompt | `bnkctl up` prompts; `--auto` skips. `bnkctl apply` is direct (CI-style). |
| **Transient apply failures** | re-run `terraform apply` manually | bnkctl auto-retries on `exit status 7`, `Connection refused`, `i/o timeout`, etc. — terraform's idempotence skips already-created resources |
| **Post-apply admin kubeconfig** | `ibmcloud ks cluster config --admin -c <name>` | bnkctl reads `roks_cluster_id` from `terraform output`, fetches the admin kubeconfig directly from IBM's container service, writes to `~/.kube/config` (mode 0600). Retries on 404 (cluster propagation lag). |

You can drop down to plain terraform at any point — see [§ Dropping to raw terraform](#dropping-to-raw-terraform) below.

### Day 0 — first deployment, end-to-end

```bash
# 1. Install the bnkctl binary (one-time)
git clone https://github.com/jgruberf5/bnkctl.git
cd bnkctl
docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  golang:1.23-alpine sh -c 'go build -o bin/bnkctl ./cmd/bnkctl'
./bin/bnkctl install                 # → ~/.local/bin/bnkctl

# 2. Sanity-check prereqs (terraform / iperf3 / IBM creds / kubeconfig)
bnkctl doctor

# 3. Initialise a workspace.
#    - Verifies the IBM Cloud API key against IAM
#    - Resolves the resource group ID
#    - Pins the TF source to the latest release tag
#    - Persists the API key (keychain → config.yaml b64 fallback)
#    - Writes ~/.bnkctl/default/config.yaml
bnkctl init

# 4. Bootstrap a starter terraform.tfvars from the pinned upstream source.
mkdir -p ~/my-bnk-deploy
cd ~/my-bnk-deploy
bnkctl tfvars
#   ✓ Wrote ./terraform.tfvars (1187 bytes)

# 5. Edit. Set whatever the upstream's example doesn't default. The
#    api_key line can stay as YOUR_IBMCLOUD_API_KEY — bnkctl supplies
#    it via TF_VAR_ibmcloud_api_key from env/keychain.
$EDITOR ./terraform.tfvars

# 6. Plan — read-only, never prompts.
bnkctl plan --var-file ./terraform.tfvars

# 7. Apply.
#    - Pre-creates the kubeconfig + scratch dirs the upstream assumes
#    - Layers bnkctl's auto-tfvars (kubeconfig_dir + scratch_dir overrides)
#      under your --var-file
#    - Streams terraform output verbatim
#    - Auto-retries on transient failures
#    - Post-apply: fetches admin kubeconfig from IBM
bnkctl up --var-file ./terraform.tfvars
#   ... 25–40 min for fresh ROKS + BNK ...
#   → Fetching admin kubeconfig for "<cluster-id-from-tf-output>"
#   ✓ Wrote /home/<user>/.kube/config (...)

# 8. Verify.
bnkctl status                        # workspace + cluster reachability
bnkctl logs flo                      # tail F5 Lifecycle Operator logs
bnkctl test                          # connectivity + DNS + throughput
```

### Day N — iteration

```bash
# Change a variable in your tfvars:
$EDITOR ./terraform.tfvars

# Re-apply (terraform's natural idempotence handles the diff):
bnkctl up --var-file ./terraform.tfvars

# Bump the TF source to a newer release:
bnkctl init --upgrade-tf
bnkctl up --var-file ./terraform.tfvars

# Inspect a specific terraform output without leaving bnkctl:
bnkctl exec terraform output -raw roks_cluster_id

# Or drop into a workspace-credentialed shell for ad-hoc kubectl/oc/ibmcloud:
bnkctl shell
(bnkctl-default) $ kubectl get pods -A
(bnkctl-default) $ ibmcloud ks cluster ls
(bnkctl-default) $ exit
```

### Tear-down

```bash
# tfvars still required at destroy time — terraform parses it the same way as apply.
bnkctl down --var-file ./terraform.tfvars

# Optionally remove the workspace's local state dir:
bnkctl ws delete default
```

### Layering order — what wins

When `bnkctl up`/`plan`/`apply`/`destroy` runs, terraform sees variables from these layers, in order (later wins):

1. **Auto-rendered `terraform.tfvars`** at `~/.bnkctl/<ws>/state/terraform.tfvars`. Generated from `config.yaml`. Includes `kubeconfig_dir`, `scratch_dir`, region, RG, cluster, BNK basics.
2. **Workspace override** at `~/.bnkctl/<ws>/terraform.tfvars.user` — optional, persistent across runs of this workspace.
3. **`--var-file <path>`** (repeatable). Each `--var-file` flag adds a file in the order given.
4. **`TF_VAR_*` env vars** — `IBMCLOUD_API_KEY` becomes `TF_VAR_ibmcloud_api_key` automatically. Useful for one-off overrides without editing files.

Standard terraform precedence applies: a `--var-file` value wins over an env-var, which wins over an earlier file's value, which wins over the auto-tfvars.

### Dropping to raw terraform

bnkctl owns *no* state that terraform doesn't already track. Everything bnkctl writes is in the workspace dir; terraform owns the .tf files in the resolved source dir. You can drop into either:

```bash
# All workspace state:
ls ~/.bnkctl/default/
#   config.yaml                terraform.tfvars.user   (optional)
#   state/
#     terraform.tfstate        # terraform's state file
#     terraform.tfvars         # bnkctl's auto-rendered tfvars
#     tf-source/<repo>-<tag>/  # the resolved upstream source
#       main.tf
#       variables.tf
#       modules/...
#       bnkctl_backend_override.tf   # bnkctl-managed; configures local backend
#     terraform/               # TF_DATA_DIR (.terraform internals)
#     kubeconfig/<modulename>/ # IBM provider kubeconfig downloads
#     scratch/                 # FLO FAR + manifest extraction
#     scratch/f5-manifest/
#     kubeconfig (file)        # bnkctl's downloaded admin kubeconfig

# To use plain terraform, cd into the source and set TF_DATA_DIR:
cd ~/.bnkctl/default/state/tf-source/ibmcloud_terraform_bigip_next_for_kubernetes_2_3-v0.6.9/
export TF_DATA_DIR=~/.bnkctl/default/state/terraform
export TF_VAR_ibmcloud_api_key="$(security find-generic-password -s bnkctl -a default/ibmcloud_api_key -w)"  # macOS
terraform plan -var-file ~/my-bnk-deploy/terraform.tfvars
terraform apply -var-file ~/my-bnk-deploy/terraform.tfvars
```

The `bnkctl_backend_override.tf` file ensures `terraform plan` writes state to the same `~/.bnkctl/<ws>/state/terraform.tfstate` whether you invoke via bnkctl or directly. Subsequent `bnkctl up` reads the same state seamlessly.

### Common questions

- **Does `bnkctl up` modify `terraform.tfstate` from a previous bnk-runner deployment?** Yes — same state file, terraform's normal `apply` semantics. Existing resources stay; bnkctl just runs the plan against the current TF source.
- **Can I run multiple workspaces against different clusters?** Yes — each `bnkctl -w <name> up` is fully isolated under `~/.bnkctl/<name>/`. State, kubeconfig, scratch, even pinned TF version are per-workspace.
- **What if I want to run terraform from CI without bnkctl?** Use the upstream repo's `terraform.tfvars.example` directly and run `terraform` against the upstream module. bnkctl's contributions (kubeconfig pre-creation, scratch_dir override, backend.tf) only matter if you want to skip the bnk-runner's `/work` mount layout. Most CI setups can either run inside the bnk runner or replicate bnkctl's layout via env vars.
- **What if the upstream source moves to v0.7.0 with breaking changes?** `bnkctl init --upgrade-tf` re-pins; you'll see new variables in `bnkctl tfvars` output. bnkctl's tfvars rendering is forward-compatible — undeclared variables warn but don't break.

---

## Features

### Lifecycle (deploy + manage BNK)

| Command | Description |
|---|---|
| `bnkctl init [--upgrade-tf] [--tf-source PATH]` | Interactive setup. Verifies IBM Cloud credentials, resolves the resource group, pins the latest Terraform release, writes `~/.bnkctl/<workspace>/config.yaml`. |
| `bnkctl up [--auto] [--var-file PATH ...] [--no-kubeconfig]` | The everyday deploy: `terraform plan` → confirm (unless `--auto`) → `terraform apply` → fetch admin kubeconfig to `~/.kube/config`. |
| `bnkctl plan [--var-file PATH ...]` | Read-only diff. Never prompts. |
| `bnkctl apply [--auto] [--var-file PATH ...] [--no-kubeconfig]` | Direct apply for CI / scripted flows. Skips the plan-and-confirm gate. |
| `bnkctl down [--auto] [--var-file PATH ...]` | `terraform destroy` with confirmation gate. |

`--var-file` matches terraform's own flag (repeatable, later-wins). See [Supplying your own `terraform.tfvars`](#supplying-your-own-terraformtfvars) for the full layering story.

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
| `bnkctl install [--dir PATH] [--force]` | Copy the running binary into a directory on `$PATH`. Defaults to `~/.local/bin` (no sudo); overridable. Idempotent — if the running binary is already at the destination, no-op. |
| `bnkctl tfvars [-o PATH] [--force]` | Emit the pinned TF source's `terraform.tfvars.example` to a file (default `./terraform.tfvars`) or stdout (`-o -`). Use as a starter for `bnkctl up --var-file`. |
| `bnkctl self update` | Pull the latest GitHub release tarball, verify SHA256 against `checksums.txt`, atomic-replace the running binary. Linux/macOS only. |
| `bnkctl completion {bash\|zsh\|fish\|powershell}` | Print shell completion script (cobra built-in). |
| `-o json`, `--no-color`, `-v/--verbose`, `-q/--quiet` | Global output flags. |

### Configuration model

- **Per-workspace:** `~/.bnkctl/<workspace>/config.yaml` — region, resource group, cluster details, BNK options, TF source pin, test settings.
- **Global:** `~/.bnkctl/config.yaml` — `current_workspace` pointer + UI defaults.
- **State:** `~/.bnkctl/<workspace>/state/` — `terraform.tfstate`, the auto-generated `terraform.tfvars`, kubeconfig, scratch downloads.
- **User tfvars override** *(optional)*: `~/.bnkctl/<workspace>/terraform.tfvars.user` — see [Importing an existing tfvars](#importing-an-existing-terraformtfvars) below.
- **Override base dir:** `BNKCTL_HOME=/path/to/state` env var.
- **Secrets:** `IBMCLOUD_API_KEY` env var, OS keychain (macOS Keychain / libsecret / Windows Credential Manager via `zalando/go-keyring`), or — opt-in — a base64-encoded `api_key_b64` field in the workspace `config.yaml`. Plaintext `api_key:` is still rejected. The keychain/env path is the recommended secure default; see [API key resolution](#api-key-resolution) below.
- **`.env` file in cwd:** bnkctl loads `./.env` at startup (if present) so project-scoped credentials don't have to live in your shell profile. Existing environment variables take precedence — `.env` only fills in unset ones.

### API key resolution

When bnkctl needs the IBM Cloud API key — at `init`, before any cluster operation, before terraform runs — it walks this chain:

1. **Environment variables** (in order): `IBMCLOUD_API_KEY`, `IC_API_KEY`, `TF_VAR_ibmcloud_api_key`, `TF_VAR_IBMCLOUD_API_KEY`, `TF_VAR_IC_API_KEY`.
2. **OS keychain** — `bnkctl` service, user `<workspace>/ibmcloud_api_key`. Saved via `bnkctl init`'s post-prompt offer.
3. **Workspace config** — `ibmcloud.api_key_b64` (base64-encoded, see warning below).
4. **Interactive prompt** — only on a TTY; offers to save to the keychain after.

To pin a single source, set `ibmcloud.api_key_source: env|keychain|config|prompt` in `config.yaml` — bypasses the chain entirely.

#### Storing the key in `config.yaml` (base64 — opt-in)

If keychain isn't an option (sealed CI workstation, custom VM image, working-from-a-flash-drive scenario) and you don't want to pass `IBMCLOUD_API_KEY` on every invocation, you can paste a base64-encoded copy directly into the workspace config:

```bash
echo -n "$IBMCLOUD_API_KEY" | base64
# 9MfeoOlh...

# Then edit ~/.bnkctl/<workspace>/config.yaml:
#   ibmcloud:
#     region: us-south
#     resource_group: default
#     api_key_b64: 9MfeoOlh...
```

> ⚠️ **base64 is obfuscation, not encryption.** Anyone with the file can `base64 -d` instantly — equivalent to plaintext for security purposes. Use only when:
> - The file lives on a single-user machine, `chmod 600`-ed.
> - The workspace dir is in `.gitignore` (or you're not in a git repo).
> - You'd otherwise be tempted to leave the key in a shell-history-bearing `export IBMCLOUD_API_KEY=…`.
>
> The recommended path stays `IBMCLOUD_API_KEY` env (per-invocation or via `.env`) or the OS keychain (cross-shell persistence with system-level access control).

The plaintext-rejection guard for `config.yaml` only blocks fields *named* `api_key` / `apikey` / `password` / `token` / etc. — it doesn't reject `api_key_b64` because the field name signals user intent ("I know what I'm doing").

### `.env` in the working directory

Any process-level env var bnkctl reads can come from a `.env` file in the directory where you run `bnkctl`. Standard `KEY=VALUE` syntax with `#` comments and quoted values, parsed by [`github.com/joho/godotenv`](https://github.com/joho/godotenv).

```ini
# .env (in your project dir)
IBMCLOUD_API_KEY=oJwJ5M-_***
IBMCLOUD_REGION=us-south
GITHUB_TOKEN=ghp_***            # raises self-update / TF-source rate limits
TF_VAR_ibmcloud_resource_group=my-rg   # any TF_VAR_* feeds straight to terraform
```

Then:

```bash
cd ~/myproject
bnkctl up                       # picks up .env automatically
```

Precedence:

1. Existing env (your shell, CI runner) — wins.
2. `.env` values — fill in anything unset.
3. OS keychain (for `IBMCLOUD_API_KEY` only) — fallback.
4. Interactive prompt — last resort, only on a TTY.

`.env` only loads from cwd, not the workspace dir or `$HOME`. The convention follows tools like `direnv` / `dotenv-cli` / Docker Compose. **Make sure `.env` is in your project's `.gitignore`** — it has secrets.

If `.env` exists but parses badly, bnkctl prints a warning and continues with whatever env vars were already set:

```
bnkctl: warning: parsing .env: line 3: unterminated string
```

### Supplying your own `terraform.tfvars`

Three ways, depending on whether you already have a tfvars or want to start from a template.

#### Bootstrap from the upstream example

```bash
bnkctl init                    # pins a TF source first
bnkctl tfvars                  # writes ./terraform.tfvars from the
                               # pinned source's terraform.tfvars.example
$EDITOR ./terraform.tfvars
bnkctl up --var-file ./terraform.tfvars
```

`bnkctl tfvars` resolves the workspace's pinned TF source (downloading the tarball if not yet cached), reads its `terraform.tfvars.example`, and writes a copy to a path you can edit. Refuses to clobber an existing file unless `--force`. Pass `-o -` to write to stdout instead, or `-o <path>` for a non-default destination.

#### `--var-file` (recommended; matches terraform's flag exactly)

```bash
bnkctl plan --var-file /path/to/terraform.tfvars
bnkctl up   --var-file /path/to/terraform.tfvars
```

Repeatable, in the order given:

```bash
bnkctl up --var-file base.tfvars --var-file overlay.tfvars
```

Available on `up`, `plan`, `apply`, and `down`. Same precedence as terraform: later files override earlier ones.

This is the right primary surface when:

- You have an existing `terraform.tfvars` from a prior bnk workflow.
- You want to set TF variables not exposed in bnkctl's `config.yaml` schema (`testing_*`, `roks_min_worker_*`, `cert_manager_namespace`, `bigip_*`, etc. — the upstream module accepts ~40 variables; `config.yaml` maps the most common subset).
- You're scripting CI runs and want explicit, file-by-file control.

#### `terraform.tfvars.user` (workspace-persistent override)

If you want the same override every time without remembering the flag, drop a file at:

```
~/.bnkctl/<workspace>/terraform.tfvars.user
```

bnkctl picks it up automatically on every up/plan/apply/down. Useful for per-workspace persistence; `--var-file` flags still apply on top.

#### Layering order

bnkctl assembles `-var-file` arguments in this order — terraform's later-wins rule means each layer can override earlier ones:

1. **Auto-rendered** `~/.bnkctl/<workspace>/state/terraform.tfvars` (from `config.yaml`).
2. **`terraform.tfvars.user`** in the workspace dir, if present.
3. **`--var-file`** paths from the command line, in flag order.

You'll see the layering in the run output:

```
→ Layering user tfvars from /home/jgruber/.bnkctl/default/terraform.tfvars.user
→ terraform init
→ terraform plan
```

#### Quick start with an existing tfvars

```bash
bnkctl init                                              # answer minimally — your tfvars will override
bnkctl plan --var-file /home/me/project/terraform.tfvars # confirm merged values
bnkctl up   --var-file /home/me/project/terraform.tfvars
```

#### Note on the API key

If your `terraform.tfvars` contains `ibmcloud_api_key = "..."` it'll be sourced from the file rather than bnkctl's normal env-var/keychain path. That works, but the key ends up in plaintext on disk wherever the file lives. The recommended pattern: remove the `ibmcloud_api_key` line from your tfvars and let bnkctl's keychain/env-var resolution pass it via `TF_VAR_ibmcloud_api_key` instead.

```bash
# Strip the api_key line on the way in:
grep -v '^ibmcloud_api_key' /path/to/terraform.tfvars > /tmp/no-key.tfvars
bnkctl up --var-file /tmp/no-key.tfvars
```

---

## Build from source

### Requirements

- **Go 1.23 or newer** is mandatory. The module declares `go 1.23` in `go.mod`; `go-version-file: go.mod` is what CI reads. Builds fail loudly on older versions — the IBM and k8s SDKs both pull language features added in 1.23. Confirm with `go version`.
  - **No Go installed (or have an older version)?** Skip to [Build with Docker](#build-with-docker-no-go-installation-required) — produces the same binary without touching the host Go install.
  - Need to upgrade? Pre-built Go installers: [go.dev/dl](https://go.dev/dl/). On macOS: `brew install go`. On Linux: distro package or the tarball from go.dev.
- **terraform** on `PATH` (>= 1.5) — required at runtime for `up` / `plan` / `apply` / `down`.
- **iperf3** on `PATH` — required for `bnkctl test throughput`.
- (Optional) **kubectl / oc / ibmcloud** — only for the corresponding passthrough commands and `bnkctl shell`.

`bnkctl doctor` reports each of the above with ✓/⚠/✗ once you have a binary.

### Build with Docker (no Go installation required)

This is the recommended path if your host doesn't have Go 1.23+. Uses the official `golang:1.23-alpine` image; produces a binary in `./bin/`.

```bash
git clone https://github.com/jgruberf5/bnkctl.git
cd bnkctl

docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  golang:1.23-alpine sh -c 'go mod tidy && go build -o bin/bnkctl ./cmd/bnkctl'

./bin/bnkctl --help
```

Anatomy of the docker invocation:

| Flag | Why |
|---|---|
| `-v "$PWD:/work"` | Bind-mount the repo into the container at `/work`. |
| `-w /work` | Container working directory matches the mount. |
| `--user "$(id -u):$(id -g)"` | Output binary is owned by your host user, not root. |
| `-e HOME=/tmp` | Go writes its module cache under `$HOME`; `/tmp` is writable by any user. Without this, `go mod tidy` fails on a writable-`/root` permission error. |
| `golang:1.23-alpine` | Pinned major version; matches `go.mod`'s minimum. |

#### Cross-compile via Docker

Set `GOOS` / `GOARCH` env vars in the same `docker run` to produce binaries for other platforms:

```bash
# macOS arm64 (Apple Silicon)
docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e GOOS=darwin -e GOARCH=arm64 \
  golang:1.23-alpine sh -c 'go mod tidy && go build -o bin/bnkctl-darwin-arm64 ./cmd/bnkctl'

# Windows amd64
docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e GOOS=windows -e GOARCH=amd64 \
  golang:1.23-alpine sh -c 'go mod tidy && go build -o bin/bnkctl.exe ./cmd/bnkctl'

# Full sweep (mirror of what goreleaser produces for tagged releases)
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    ext=""; [ "$os" = "windows" ] && ext=".exe"
    docker run --rm -v "$PWD:/work" -w /work \
      --user "$(id -u):$(id -g)" -e HOME=/tmp \
      -e GOOS=$os -e GOARCH=$arch \
      golang:1.23-alpine sh -c "go build -o bin/bnkctl_${os}_${arch}${ext} ./cmd/bnkctl"
  done
done
```

Each binary is statically linked (Alpine + `CGO_ENABLED=0` is the default for cross-compile) — no extra runtime deps for the binary itself.

### Build natively

If `go version` reports `1.23` or newer:

```bash
git clone https://github.com/jgruberf5/bnkctl.git
cd bnkctl

go mod tidy                          # first time only — populates go.sum
make build                           # → bin/bnkctl

# Or without Make:
go build -o bin/bnkctl ./cmd/bnkctl

# Install via bnkctl itself (recommended — copies into ~/.local/bin):
./bin/bnkctl install

# Or specify a directory:
./bin/bnkctl install --dir ~/bin
sudo ./bin/bnkctl install --dir /usr/local/bin

# Or just add ./bin to PATH for ad-hoc use:
export PATH="$PWD/bin:$PATH"

bnkctl --help
```

Make targets:

```
make build      # go build -ldflags ... -o bin/bnkctl ./cmd/bnkctl
make test       # go test ./...
make vet        # go vet ./...
make tidy       # go mod tidy
make run        # build + ./bin/bnkctl --help
make clean      # rm -rf bin/
```

`VERSION` / `COMMIT` / `DATE` are passed via `-ldflags` and surface in `bnkctl version`:

```bash
make build VERSION=v0.1.0
./bin/bnkctl version
# bnkctl v0.1.0 (commit abc1234, built 2026-05-08T...)
```

### Tests

```bash
make test                                       # all packages
go test -race ./internal/config/...             # one package
go test -v -run TestNew ./internal/config/...   # one test
```

The `internal/ibm` package has integration tests that skip unless `IBMCLOUD_API_KEY` is set:

```bash
IBMCLOUD_API_KEY=... go test ./internal/ibm/...
```

Same Docker pattern works for tests:

```bash
docker run --rm -v "$PWD:/work" -w /work \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  golang:1.23-alpine sh -c 'go test -race ./...'
```

### Troubleshooting `make build`

If `make build` fails, check in this order:

```bash
go version                # need 1.23+; "command not found" → use the Docker path
make --version            # missing on Windows + minimal Linux; install or use the docker `go build` directly
git rev-parse --short HEAD   # the Makefile pulls COMMIT from this; failure is benign (defaults to "none")
go env GOPROXY            # if behind a corporate proxy, set GOPROXY accordingly before `go mod tidy`
```

The most common failure on a fresh clone is **Go too old** — `go: module requires Go 1.23` is unambiguous; install a newer Go or use the Docker path.

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

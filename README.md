# bnkctl

A single-binary CLI to deploy F5 BIG-IP Next for Kubernetes (BNK) onto IBM Cloud ROKS and validate the deployment.

> **Status:** Planning. No code yet — see [docs/PRD.md](docs/PRD.md) for the product specification driving v1.

## What this is

`bnkctl` orchestrates the Terraform that lives in [`ibmcloud_terraform_bigip_next_for_kubernetes_2_3`](https://github.com/jgruberf5/ibmcloud_terraform_bigip_next_for_kubernetes_2_3) and adds a built-in test suite (connectivity / DNS / throughput) so that "did the deployment succeed" has a machine answer.

It is the cross-platform Go successor to `bnk` (bash + docker). Terraform modules continue to be authored and tested in their own repo; `bnkctl` consumes them at a pinned source.

## What this is *not*

- Not a Terraform authoring tool. TF lives in its own repo.
- Not a general-purpose IBM Cloud / Kubernetes CLI.
- Not a workload deployer beyond BNK and the test fixtures it spins up.

## The 3-command UX

```
bnkctl init    # interactive setup, writes workspace config
bnkctl up      # provision + deploy
bnkctl test    # connectivity + DNS + throughput
```

See [docs/PRD.md](docs/PRD.md) for the full command surface, configuration model, and architecture sketch.

## Layout (planned)

```
bnkctl/
├── README.md
├── docs/
│   └── PRD.md
├── cmd/bnkctl/        # cobra root, command entrypoints
└── internal/
    ├── config/        # workspace + global config + secrets
    ├── tf/            # terraform-exec wrapper + tfvars rendering
    ├── ibm/           # IBM Cloud Go SDK calls
    ├── k8s/           # client-go cluster ops
    ├── test/          # connectivity / dns / throughput runners
    ├── ui/            # spinners, tables, JSON output
    └── doctor/        # prerequisite checks
```

The `cmd/` and `internal/` trees do not exist yet — to be scaffolded once the PRD is reviewed.

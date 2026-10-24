# waveform

Plan multi-service deploys from the service graph you **observe** in traces, not the one you declared.

Given a change set (a list of services, or a git diff), waveform:

1. builds the service-to-service graph from OpenTelemetry spans, with traffic (RPS) and a confidence score per edge,
2. computes a **traffic-weighted blast radius**, meaning the share of each service's traffic that reaches a changed service,
3. orders the change into **waves**. Additive changes roll out providers first. Removals roll out consumers first (expand/contract).
4. flags dependencies that were observed but not declared, and declared but never observed,
5. comments the plan on the pull request.

**Status: phase 1, read-only.** waveform plans and comments. It does not deploy, gate, or roll back anything yet. See [Roadmap](#roadmap).

```
<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 0.67, 4 service(s) in radius, window 60s)

### Waves

1. `mobile-bff`, `new-api`
2. `frontend`
3. `legacy-api` (removal)
```

## Install

```sh
go install github.com/ChadiDridi/waveform/cmd/waveform@latest
```

You need Go 1.23 or newer. waveform builds as a single static binary.

## Usage

```sh
# 1. Build the observed graph from OTLP/JSON traces. The collector's
#    `file` exporter output works as-is (JSON lines).
waveform graph --traces traces.json -o graph.json

# 2. Plan a change set.
waveform plan --graph graph.json --change payments --change legacy-api=removal

# 3. Or derive the change set from git, mapping files to services via waveform.yaml.
waveform plan --graph graph.json --git-diff origin/main...HEAD --format json
```

| Flag | Meaning |
|---|---|
| `--graph` / `--traces` | Observed graph JSON, or OTLP/JSON trace files (repeatable) |
| `--window` | Observation window in seconds for RPS. Defaults to the span time range |
| `--change svc[=additive\|removal]` | Changed service (repeatable, comma-separated). The default kind is `additive` |
| `--git-diff REV` | Changed files from `git diff --name-only REV`, mapped through `--config` |
| `--config` | `waveform.yaml`. Defaults to `./waveform.yaml` if that file exists |
| `--format` | `markdown` (default) or `json` |
| `--github-comment [--pr N]` | Create or update the plan comment on a PR |

The exit code is 0 on success, 1 on error or when no valid wave order exists, and 2 on a usage error.

### waveform.yaml

```yaml
services:
  checkout:
    paths: ["services/checkout"]        # plain path = directory prefix
    dependsOn: [payments, inventory]    # optional; enables declared-vs-observed diff
  payments:
    paths: ["services/payments/**/*.go"]
  shared-proto:
    paths: ["proto/**"]
```

A service is identified by the OpenTelemetry `service.name` resource attribute. Service names in `waveform.yaml` must match it. In a monorepo, one file can map to several services.

### GitHub Action

```yaml
# .github/workflows/waveform.yml
on: pull_request
permissions:
  contents: read
  pull-requests: write
jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: ChadiDridi/waveform@main
        with:
          graph: ops/graph.json          # or traces: path/to/otlp.json
```

The action runs `waveform plan --git-diff origin/<base>...HEAD --github-comment`. It updates its own comment in place, so you don't get one new comment per push.

## How it works

Read [docs/design.md](docs/design.md) for the details. In short:

- **Edges.** A span whose parent span belongs to a different service counts as one call from the parent's service to the child's. A client or producer span that has no child but sets `peer.service` falls back to an edge to that peer, at half confidence. That covers uninstrumented dependencies such as databases and SaaS APIs.
- **Confidence** is `calls / (calls + 10)`. It is a sample-size heuristic, not a statistical guarantee.
- **Exposure** of service S is the share of S's outbound RPS that transitively reaches a changed service. The overall score is the throughput-weighted mean exposure. Levels: `low` below 0.15, `medium` below 0.4, `high` otherwise.
- **Waves.** For each changed consumer C that reaches a changed provider P, P goes first if P's change is additive, and C goes first if P's change is a removal. The constraints are layered with Kahn's algorithm, and each layer is one wave.
- **Cycles** are reported. For ordering, waveform ignores the lowest-traffic edge in each cycle, repeating until the graph is acyclic.

## Prior art and honest positioning

To our knowledge, no open-source tool combines observed trace graphs, traffic-weighted blast radius, and wave ordering. This is **not verified**: we only did shallow web searches. If you know a tool that does this, please open an issue. Related work:

- **Argo Rollouts / Flagger** do progressive delivery for one service at a time. You order multiple services by hand.
- **Argo CD** has sync waves inside an Application. It does not resolve dependencies between Applications.
- **Kargo** promotes artifacts across environments (stages). It does not order services against each other by observed traffic.
- **The OpenTelemetry Collector `servicegraph` connector** derives the same kind of edges as metrics. It is a natural future input for waveform.
- **Commercial observability products** show service maps and failure blast radius. They do not plan deploys.

## Roadmap

- **Phase 1 (this release):** graph discovery, blast radius, wave plan, PR comment. Read-only.
- **Phase 2:** gates that ramp each wave on the SLO burn rate of *downstream* services, not only the deployed one, using Prometheus. Argo Rollouts acts as the actuator; waveform does not reimplement it. The plan is stored as a CRD.
- **Phase 3:** ordered rollback of the failing wave and the dependent waves already deployed, in reverse topological order. Irreversible steps (such as migrations) require a human.
- **Later:** API/schema version-window contracts, so the planner rejects orders that would break them. Live OTLP ingest and servicegraph-connector input.

## Development

```sh
go test ./...                              # unit + golden tests
go test ./internal/analyze -update         # regenerate goldens, then review the diff
```

The golden cases are in `testdata/cases/`: chain, diamond, cycle, expand/contract, an unknown service, and raw OTLP traces with config.

## License

[Apache-2.0](LICENSE)

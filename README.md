# waveform

Plan and run multi-service deploys from the service graph you **observe** in traces, not the one you declared.

Given a change set (a list of services, or a git diff), waveform:

1. **Discovers the graph.** It reads OpenTelemetry spans (files, live OTLP/HTTP, or OTel Collector servicegraph metrics) and records traffic (RPS) and confidence for each edge.
2. **Computes the blast radius.** This is traffic-weighted: the share of each service's traffic that reaches a changed service.
3. **Orders the change into waves.** Additive changes roll out providers first. Removals roll out consumers first (expand/contract). Plans that would break declared API version windows are rejected.
4. **Comments the plan on the PR.**
5. **Executes the plan** through Argo Rollouts, from the CLI or as a `WavePlan` custom resource. Each wave is gated on the SLO burn rate of the **downstream callers** in its blast radius, not only the services it deploys.
6. **Rolls back in order.** On failure, the failing wave and the already-deployed waves linked to it roll back in reverse order. Irreversible steps such as migrations halt the rollback for a human.

```
<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 0.67, 4 service(s) in radius, window 60s)

### Waves

1. `mobile-bff`, `new-api`
   - gate watches: frontend, mobile-bff, new-api
2. `frontend`
   - gate watches: frontend
3. `legacy-api` (removal)
   - gate watches: frontend, legacy-api, mobile-bff
```

**Status: v1alpha1, early.** Unit, golden, and kind/Argo Rollouts end-to-end tests pass. Nobody has run it in production yet. Read [Limits](#limits) before pointing it at anything that matters.

## Install

```sh
go install github.com/ChadiDridi/waveform/cmd/waveform@latest   # Go 1.26+
```

Tagged releases also publish binaries and a `ghcr.io/chadidridi/waveform` image.

## 1. Build the graph

```sh
waveform graph --traces traces.json -o graph.json                     # OTLP/JSON files (collector file exporter output works)
waveform graph --servicegraph http://prometheus:9090 --servicegraph-range 1h -o graph.json
waveform collect --listen :4318 -o graph.json                         # live: point an otlphttp exporter here
```

For `collect`, configure the collector like this:

```yaml
exporters:
  otlphttp/waveform:
    endpoint: http://waveform:4318
    encoding: json        # waveform accepts OTLP/JSON only
```

## 2. Plan

```sh
waveform plan --graph graph.json --change payments --change legacy-api=removal
waveform plan --graph graph.json --git-diff origin/main...HEAD --format json
waveform plan --graph graph.json --git-diff origin/main...HEAD --format waveplan --name pr-42 > waveplan.yaml
```

| Flag | Meaning |
|---|---|
| `--graph` / `--traces` / `--servicegraph` | Where the observed graph comes from |
| `--change svc[=additive\|removal]` | Changed service (repeatable). The default kind is `additive` |
| `--git-diff REV` | Changed files from `git diff --name-only REV`, mapped through `waveform.yaml` |
| `--format` | `markdown` (default), `json`, or `waveplan` (an executable resource) |
| `--github-comment [--pr N]` | Create or update the plan comment on a PR |

Exit codes:
- `0`: success.
- `1`: error, no valid order, or a contract violation.
- `2`: usage error.

## 3. Run

Every service's Argo Rollout must **start its canary steps with an indefinite pause**. GitOps (or you) applies all the new revisions at once, each Rollout holds, and waveform releases the holds wave by wave:

```yaml
strategy:
  canary:
    steps:
      - pause: {}          # waveform promotes this in wave order
      - setWeight: 25
      - pause: {duration: 2m}
      # ... your usual steps and analysis
```

Run the plan from the CLI. State is saved next to the plan, so rerunning the command resumes:

```sh
waveform run -f waveplan.yaml
waveform run -f waveplan.yaml --approve orders-db   # start an irreversible step
```

Or run it in-cluster:

```sh
kubectl apply -f deploy/crd.yaml -f deploy/controller.yaml
kubectl apply -f waveplan.yaml
kubectl get waveplans -w
kubectl patch waveplan pr-42 --type=json -p '[{"op":"add","path":"/spec/approvals/-","value":"orders-db"}]'
```

What happens per wave:

1. Promote every step's held Rollout. The Rollout's own canary steps and analysis then run.
2. Every `gate.interval`, check the burn rate of the wave's **watch set**: the wave's services plus every caller with at least 5% exposure to them.
3. When all steps are healthy, **bake** for `gate.bake`. The wave is done after a gate check passes once the bake is over.

On a failed step, a timeout, or a breached gate, the run goes to `RollingBack`:

- The failing wave and every deployed step linked to it through ordering constraints roll back, latest wave first.
- In-flight Rollouts are aborted. Finished ones are undone to their previous revision.
- Unrelated waves are kept.
- Irreversible steps are never rolled back automatically. The run ends `Halted` and the message gives the remaining order.

The final phase is `Succeeded`, `RolledBack`, or `Halted` (needs a human).

## waveform.yaml

```yaml
namespace: shop                       # default namespace for Rollouts
services:
  checkout:
    paths: ["services/checkout"]      # plain path = directory prefix; "**" globs work
    dependsOn: [payments, inventory]  # optional; enables declared-vs-observed diff
  payments:
    paths: ["services/payments/**/*.go"]
    rollout: {name: payments-v2, namespace: pay}   # default: Rollout named after the service
  orders-db:
    paths: ["migrations/orders"]
    irreversible: true                # needs approval; never auto-rolled back

gate:
  prometheusURL: http://prometheus.monitoring:9090   # unset = no gate, bake only
  objective: 0.999
  maxBurnRate: 2
  window: 5m
  bake: 5m
  interval: 30s
  onNoData: pass                      # or fail
  minExposure: 0.05
  # query: Go template returning a burn rate; .Service .Window .Objective

contracts:                            # optional API version windows
  orders-api:
    provider: orders
    serves: {before: [v1], after: [v1, v2]}
    consumers:
      checkout: {before: v1, after: v2}
      reporting: {before: v1}
```

The default gate query assumes OTel HTTP server metrics in Prometheus, with the service name in the `job` label:

```
(sum(rate(http_server_request_duration_seconds_count{job="SVC",http_response_status_code=~"5.."}[5m]))
  / sum(rate(http_server_request_duration_seconds_count{job="SVC"}[5m]))) / (1 - 0.999)
```

If your labels differ, set `gate.query`. Set `WAVEFORM_PROMETHEUS_TOKEN` to send a bearer token.

## GitHub Action

```yaml
on: pull_request
permissions: {contents: read, pull-requests: write}
jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: {fetch-depth: 0}
      - uses: ChadiDridi/waveform@v0.1.0
        with:
          graph: ops/graph.json
```

## How it works

Read [docs/design.md](docs/design.md) for the details. In short:

- **Edges.** A span whose parent span belongs to a different service counts as one call. A client span that never gets a child falls back to its `peer.service` attribute, at half confidence. Confidence is `calls/(calls+10)`.
- **Exposure** of service S is the share of S's outbound RPS that transitively reaches a changed service. The score is the throughput-weighted mean exposure.
- **Waves.** For a changed consumer C that reaches a changed provider P, P goes first if P's change is additive, and C goes first if it is a removal. The constraints are layered with Kahn's algorithm. Cycles are reported, and the lowest-traffic edge is ignored for ordering.
- **Contracts.** An in-flight service runs old and new pods at the same time. Every version a consumer may send must therefore be served by every provider pod set it may reach, in every wave.

## Limits

- **Argo Rollouts only:** the canary strategy, with an inline pod template. Blue-green and `workloadRef` are not supported yet.
- **Git is not reverted.** A cluster rollback does not revert Git, so GitOps self-heal may re-apply the bad revision. Revert the PR too, or disable self-heal for the rollout window.
- **Single replica controller:** there is no leader election, and it polls instead of watching.
- **Live ingest is OTLP/JSON only,** with no protobuf.
- **The blast radius is a traffic-share heuristic,** not per-request causality.

## Prior art

To our knowledge, no open-source tool combines observed trace graphs, traffic-weighted blast radius, wave ordering, downstream SLO gates, and ordered rollback. This is **not verified**: we only did shallow web searches. If you know one, open an issue.

- **Argo Rollouts / Flagger** do progressive delivery for one service at a time. waveform uses Argo Rollouts as its actuator.
- **Argo CD** has sync waves inside an Application. It does not resolve dependencies between Applications.
- **Kargo** promotes artifacts across environments. It does not order services against each other by observed traffic.
- **The OTel Collector `servicegraph` connector** produces the edges that `--servicegraph` reads.

## Development

```sh
go test ./...                          # unit + golden tests
go test ./internal/analyze -update     # regenerate goldens, then review the diff
./hack/e2e.sh                          # kind + Argo Rollouts end-to-end (needs docker, kind, kubectl)
```

## License

[Apache-2.0](LICENSE)

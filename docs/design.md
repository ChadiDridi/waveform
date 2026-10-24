# Design notes

These notes are terse. They record decisions and their reasons.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Name and license | `waveform`, Apache-2.0 | Fits the CNCF ecosystem and includes a patent grant |
| Phase 1 trace source | OTLP/JSON files | Fixtures and collector `file` exporter dumps use the same format. Live ingest can come later |
| Service identity | `service.name` resource attribute | The OTel standard |
| Deploy unit | Path globs in `waveform.yaml` map to services | Works for monorepos, and one file can map to N services |
| Cycles | Report them, and ignore the lowest-RPS edge per SCC until acyclic | Keeps output deterministic. The edge with the least traffic is the least likely to matter for ordering |
| Graph lib | Hand-rolled (Kahn, Tarjan) | Needs about 100 lines. Avoids a dependency |
| Deps | stdlib, `yaml.v3`, client-go (dynamic client only) | No Argo Go dependency: Rollouts are handled as unstructured objects |
| Actuator | Argo Rollouts, canary strategy | Reuse its progressive delivery instead of reimplementing it |
| Plan storage | `WavePlan` CRD, `waveform.chadidridi.github.io/v1alpha1` | Group under a domain the project controls |
| Executor shape | One idempotent `Advance(spec, status)` step | The same code drives the CLI and the controller, and resumes after a crash |
| Live ingest | OTLP/HTTP JSON only | Avoids protobuf/gRPC dependencies. The collector's `otlphttp` exporter supports `encoding: json` |

## Graph

- Edge `A -> B` means A calls B: A is the consumer, B the provider.
- Cross-service parent/child span pairs give `trace` edges.
- An unresolved CLIENT/PRODUCER span with `peer.service` gives a `peer.service` edge at half confidence.
- `rps = calls / window`. The window is either `--window` or the span time range.
- `confidence = calls / (calls + 10)`.
- Spans with no `service.name` map to `unknown_service`.

## Blast radius

```
exposure(C) = 1                                   C changed
exposure(S) = Σ_{S->D} rps(S->D)/rps_out(S) · exposure(D)
score       = Σ exposure(S)·throughput(S) / Σ throughput(S)
throughput  = max(rps_in, rps_out)
```

The computation runs on the cycle-broken DAG. Path confidence is the product of edge confidences along the highest-weight path to a changed service.

Known limits:
- The model assumes each request fans out the way average traffic does. It is not per-request causal.
- Edges with no RPS in a graph file split evenly.

## Waves

- Constraints come from reachability on the DAG, including paths through unchanged services.
- The provider's change kind decides the order: additive means provider first, removal means consumer first.
- A service with both an additive and a removal change is treated as a removal. To split it, make two PRs: expand, then contract.
- Kahn layering produces the waves. If services are left unplaced, waveform reports an error and suggests splitting the change set.
- A changed service missing from the graph gets no constraints and lands in wave 1 with a warning.

## Execution (phase 2)

- Each service's Rollout starts its canary steps with `pause: {}`. GitOps applies every new revision at once, and each Rollout holds at that pause.
- `Advance` promotes the holds of wave N only after wave N-1 is done.
- A step is done when its Rollout reports `Healthy`. The Rollout's own steps and analysis still run.
- A wave is done when all its steps are healthy, the bake time has elapsed, and a gate check passed after the bake ended.
- Promotion is guarded by the hold position (`revision/stepIndex`), so a stale read never promotes twice.
- Later indefinite pauses inside a Rollout are also promoted. Timed pauses are left alone.
- `stepTimeout` (default 30m) runs from the start of the wave. It fails a step that never held (for example, because the new revision was never applied) or never became healthy.

### Gate

- The watch set of a wave is its own services plus every caller whose exposure to the wave is at least `minExposure` (default 0.05). It is computed at plan time and stored in the WavePlan.
- The burn rate comes from a PromQL template rendered per service. The default uses OTel HTTP server metrics.
- The gate breaches when the burn rate exceeds `maxBurnRate` (default 2), or when there is no data and `onNoData: fail` is set.
- A gate error, such as Prometheus being down, never passes a wave: the bake completes only after a successful check.

## Rollback (phase 3)

- Triggered by a failed or degraded step, a step timeout, or a breached gate.
- The rollback set is:
  - the started steps of the failing wave, plus
  - every earlier deployed step linked to them through ordering constraints, in either direction, transitively.
- Unrelated deployed steps are kept and listed in the message.
- Steps that never started are never touched, so waveform does not roll back revisions it did not deploy.
- Groups run in reverse wave order. Each group runs in parallel.
- Reverse order is correct for both kinds of change:
  - Expand: the consumer is reverted before the provider loses the new API.
  - Contract: the provider gets the API back before the consumer calls it again.
- In-flight steps are aborted (`status.abort`). Finished steps are undone: the previous revision's pod template is restored, and its hold is promoted.
- Irreversible steps need `spec.approvals` to start. When rollback reaches one, it marks the step `NeedsManualRollback` and halts with the remaining order spelled out.
- Known gap: if the trigger is a Git change, rolling back the cluster does not revert Git. GitOps self-heal can re-apply the new revision. Revert the PR as well.

## Contracts (later)

- Each provider API lists the versions it serves before and after the change. Each consumer lists the version it calls.
- During a wave, an in-flight service runs both old and new pods. A version a consumer may send must therefore be served by every pod set it may reach.
- The checker evaluates every wave and the final state. It reports the first breaking wave for each consumer/provider pair, and the plan is rejected.

## Live graph (later)

- `waveform collect` attributes spans incrementally. A client span waits `grace` for its server span before falling back to `peer.service`, and spans older than `2*grace` are pruned. Edge counts are cumulative.
- A late server span arriving after the peer fallback is counted twice. This is accepted for a heuristic graph.
- `--servicegraph` reads the OTel Collector servicegraph connector's `traces_service_graph_request_total` counters from Prometheus over a trailing window.

## Not done

- Blue-green strategy and Rollouts using `workloadRef`.
- Leader election for the controller; run one replica.
- Watch-based informers. The controller polls every `--tick`.
- Protobuf OTLP.

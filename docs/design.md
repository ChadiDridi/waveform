# Design notes

These notes are terse. They record phase 1 decisions and their reasons.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Name and license | `waveform`, Apache-2.0 | Fits the CNCF ecosystem and includes a patent grant |
| Phase 1 trace source | OTLP/JSON files | Fixtures and collector `file` exporter dumps use the same format. Live ingest can come later |
| Service identity | `service.name` resource attribute | The OTel standard |
| Deploy unit | Path globs in `waveform.yaml` map to services | Works for monorepos, and one file can map to N services |
| Cycles | Report them, and ignore the lowest-RPS edge per SCC until acyclic | Keeps output deterministic. The edge with the least traffic is the least likely to matter for ordering |
| Graph lib | Hand-rolled (Kahn, Tarjan) | Needs about 100 lines. Avoids a dependency |
| Deps | stdlib + `gopkg.in/yaml.v3` | Single small binary |

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

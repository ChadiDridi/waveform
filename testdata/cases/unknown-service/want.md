<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 0.80, 2 service(s) in radius, window 60s)

### Waves

1. `api`, `billing`
   - gate watches: api, billing, web

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `api` | yes | 100% | 100.00 | 1.00 |  |
| `web` |  | 100% | 100.00 | 1.00 | web → api |

### Warnings

- changed service not in observed graph, ordered without constraints: billing

<sub>Plan from observed traffic (3 services, 2 edges). Nothing is deployed until the plan is run (`waveform run` or a WavePlan resource).</sub>

<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 0.67, 3 service(s) in radius, window 60s)

### Waves

1. `api`
   - gate watches: api, batch, web
2. `web`
   - gate watches: web

<details><summary>Ordering constraints</summary>

- `api` before `web`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `api` | yes | 100% | 101.00 | 1.00 |  |
| `web` | yes | 100% | 100.00 | 1.00 |  |
| `batch` |  | 100% | 1.00 | 0.86 | batch → api |

### API contract violations

- during wave 1 orders-api: batch may call v1 but a provider pod set serves only [v2]
- during wave 1 orders-api: web may call v1 but a provider pod set serves only [v2]

### Warnings

- wave order breaks 2 API contract(s); adjust change kinds or split the change set

<sub>Plan from observed traffic (4 services, 3 edges). Nothing is deployed until the plan is run (`waveform run` or a WavePlan resource).</sub>

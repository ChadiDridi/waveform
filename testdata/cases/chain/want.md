<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 1.00, 3 service(s) in radius, window 60s)

### Waves

1. `db`
   - gate watches: api, db, web
2. `api`
   - gate watches: api, web

<details><summary>Ordering constraints</summary>

- `db` before `api`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `api` | yes | 100% | 100.00 | 1.00 |  |
| `db` | yes | 100% | 50.00 | 1.00 |  |
| `web` |  | 100% | 100.00 | 1.00 | web → api |

<sub>Plan from observed traffic (3 services, 2 edges). Nothing is deployed until the plan is run (`waveform run` or a WavePlan resource).</sub>

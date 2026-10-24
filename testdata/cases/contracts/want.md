<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 1.00, 4 service(s) in radius, window 60s)

### Waves

1. `orders-db` ⚠ irreversible: needs approval, manual rollback
   - gate watches: api, batch, orders-db, web
2. `api`
   - gate watches: api, batch, web
3. `web`
   - gate watches: web

<details><summary>Ordering constraints</summary>

- `api` before `web`: provider first: additive change
- `orders-db` before `api`: provider first: additive change
- `orders-db` before `web`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `api` | yes | 100% | 101.00 | 1.00 |  |
| `orders-db` | yes | 100% | 100.00 | 1.00 |  |
| `web` | yes | 100% | 100.00 | 1.00 |  |
| `batch` |  | 100% | 1.00 | 0.86 | batch → api |

<sub>Plan from observed traffic (4 services, 3 edges). Nothing is deployed until the plan is run (`waveform run` or a WavePlan resource).</sub>

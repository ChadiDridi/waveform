<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 1.00, 4 service(s) in radius, window 60s)

### Waves

1. `c`
   - gate watches: a, b, c, edge
2. `a`
   - gate watches: a, edge

<details><summary>Ordering constraints</summary>

- `c` before `a`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `a` | yes | 100% | 100.00 | 1.00 |  |
| `c` | yes | 100% | 50.00 | 1.00 |  |
| `b` |  | 100% | 100.00 | 1.00 | b → c |
| `edge` |  | 100% | 100.00 | 1.00 | edge → a |

### Warnings

- cycle [a b c]: ignored lowest-traffic edge c -> a for ordering

<sub>Plan from observed traffic (4 services, 4 edges). Nothing is deployed until the plan is run (`waveform run` or a WavePlan resource).</sub>

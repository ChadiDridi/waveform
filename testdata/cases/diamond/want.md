<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 1.00, 5 service(s) in radius, window 60s)

### Waves

1. `auth`, `inventory`
2. `gateway`

<details><summary>Ordering constraints</summary>

- `auth` before `gateway`: provider first: additive change
- `inventory` before `gateway`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `gateway` | yes | 100% | 100.00 | 1.00 |  |
| `auth` | yes | 100% | 30.00 | 1.00 |  |
| `inventory` | yes | 100% | 30.00 | 1.00 |  |
| `orders` |  | 100% | 80.00 | 0.99 | orders → inventory |
| `users` |  | 100% | 20.00 | 0.99 | users → auth |

<sub>Read-only plan from observed traffic (5 services, 5 edges). waveform does not deploy anything.</sub>

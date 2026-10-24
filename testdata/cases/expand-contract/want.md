<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 0.67, 4 service(s) in radius, window 60s)

### Waves

1. `mobile-bff`, `new-api`
2. `frontend`
3. `legacy-api` (removal)

<details><summary>Ordering constraints</summary>

- `frontend` before `legacy-api`: consumer first: provider removal
- `mobile-bff` before `legacy-api`: consumer first: provider removal
- `new-api` before `frontend`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `frontend` | yes | 100% | 50.00 | 1.00 |  |
| `new-api` | yes | 100% | 40.00 | 1.00 |  |
| `legacy-api` | yes | 100% | 12.00 | 1.00 |  |
| `mobile-bff` | yes | 100% | 2.00 | 1.00 |  |

<sub>Read-only plan from observed traffic (5 services, 5 edges). waveform does not deploy anything.</sub>

<!-- waveform:plan -->
## waveform plan

**Blast radius: HIGH** (score 0.62, 3 service(s) in radius, window 9.505s)

### Waves

1. `payments`
2. `checkout`

<details><summary>Ordering constraints</summary>

- `payments` before `checkout`: provider first: additive change

</details>

### Blast radius

| Service | Changed | Exposure | RPS at risk | Confidence | Path |
|---|---|---:|---:|---:|---|
| `checkout` | yes | 100% | 4.21 | 1.00 |  |
| `payments` | yes | 100% | 2.10 | 1.00 |  |
| `frontend` |  | 80% | 2.10 | 0.67 | frontend → checkout |

### Undeclared dependencies

Observed in traces, missing from `dependsOn`:

- `frontend` → `inventory` (0.53 rps, confidence 0.33)

### Declared but not observed

- `checkout` → `shipping`

### Warnings

- changed file not owned by any service: README.md

<sub>Read-only plan from observed traffic (5 services, 5 edges). waveform does not deploy anything.</sub>

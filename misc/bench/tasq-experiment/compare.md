# Solo vs Team-3 agent pool benchmark

Comparison of one pool agent doing the whole 	asq CLI vs three pool agents
dividing the modules among themselves. Both arms ran against the same
SPEC.md, with the same MiniMax-M2.7 provider, on the same harness.

## Summary table

| Metric | Solo | Team-3 |
|---|---|---|
| Elapsed wall-clock (s) | n/a | 1094.9 |
| Source LOC (tasq/) | 1920 | 1446 |
| Test LOC (tests/) | 829 | 1004 |
| pytest passed | 144/146 | 146/147 |
| pytest pass %  | 98.6% | 99.3% |
| Coverage %     | 0% | 0% |
| ruff issues    | -1 | -1 |
| mypy errors    | -1 | -1 |
| Scenario exit  | -1 | -1 |
| Scenario [PASS]s | 0 | 0 |
| **Composite score** | **0** | **0** |

## Raw artifacts

- Solo output:  `D:\claude-code\coai2\bench\tasq-experiment\solo-output`
- Team output:  `D:\claude-code\coai2\bench\tasq-experiment\team-output`
- Run the harness yourself: `.\bench.ps1 -OutputDir <path> -Label <name>`
- Replay the scenario: `python scenarios\smoke.py` (from inside the output dir)

## Score formula

    score = coverage% + pytest_pass% + (20 if scenario_ok else 0)
          - min(20, ruff_issues * 0.5)
          - min(10, mypy_errors * 0.2)
          - loc_penalty   (if <1500 or >12000 source LOC)

Generated at 2026-04-24T08:21:26.0202472+08:00

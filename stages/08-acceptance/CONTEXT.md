# Stage 08 — acceptance

> Layer 2 · "What do I do?"

**Purpose:** Prove the whole build against the goal's success criteria and
write the acceptance report — every check, command, and exit code.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Goal spec | ../../.goals/goal-mm3-icm-scaffold.md | success criteria | the checklist |
| Deployment notes | ../07-containerization/output/deployment.md | full | what's running |
| All stage outputs | ../01..07-*/output/ | full | evidence trail |

## Process
1. Re-run every mechanical check: `icm audit`, baseline sha256, secret-leak
   grep, `go build/vet/gofmt`, coverage greps from the goal spec.
2. Live checks: container on `shared_net` with zero host ports; NPM serves
   `https://mm3.gemneye.xyz`; generate one real song end-to-end (assistant
   → form → submit → play); history persists across a restart.
3. Record each check in `output/acceptance-report.md`: command, exit code,
   evidence snippet — or a skip with an owner and the next command to run.
4. List residual gaps and follow-ups explicitly; no silent omissions.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Acceptance report | output/acceptance-report.md | markdown |

## Checkpoints
- [ ] Human reviews the report before the project is called done.

## Audits
- [ ] Every goal success criterion has a row: pass, or skip-with-owner.
- [ ] No check claims pass without a command + exit code shown.

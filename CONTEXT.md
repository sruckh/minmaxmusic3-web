# Context — minmaxmusic3

> Layer 1 · "Where do I go?"

## Stages
| # | Stage | Purpose |
|---|-------|---------|
| 01 | project-blueprint | Define every user-facing feature of the MiniMax Music 3 front-end |
| 02 | api-contracts | Pin the exact request/response contracts for the two upstream |
| 03 | design-system | Translate `DESIGN.md` and the two theme examples into Tailwind |
| 04 | go-foundation | Stand up the Go server skeleton — layout, configuration, |
| 05 | generation-and-assistant | Build the core flow — generate a song from the form, watch it |
| 06 | song-history | Persist every generated song and make the library browsable and |
| 07 | containerization | Containerize the app for the host's deployment shape and wire |
| 08 | acceptance | Prove the whole build against the goal's success criteria and |

## Notes
- Add stages with `icm stage /opt/docker/minmaxmusic3-web <name>`.
- Regenerate this table with `icm sync /opt/docker/minmaxmusic3-web`.

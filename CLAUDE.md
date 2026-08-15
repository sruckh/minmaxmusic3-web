# Claude Code adapter — minmaxmusic3-web

This workspace follows ICM. When working here:
- Read `IDENTITY.md` (Layer 0), then `CONTEXT.md` (Layer 1) for routing.
- Execute one stage at a time per its `stages/NN-*/CONTEXT.md` contract.
- Load only the Layer 3/4 files the stage's **Inputs** table names.
- Write stage output to `stages/NN-*/output/`; a human may edit before the next stage.
- Run `icm audit` before finishing and `icm sync` when stages change.

<!-- outline:global-rules (managed by the outline skill) -->
## Global Agent Rules

The shared Global Agent Rules for this brain are imported below. They are
refreshed from Outline into `.outline/global-rules.md` at session start — edit
them in the Outline "Global Agent Rules" page, not here.

@.outline/global-rules.md
<!-- /outline:global-rules -->

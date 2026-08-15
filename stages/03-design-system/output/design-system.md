# Design system — stage 03 output

MM3 — A text-to-music console design system inspired by studio hardware, mixing consoles, and channel strips.

## 1. Palette Tokens (10 Source Hexes from `DESIGN.md`)

| Token | Hex | Role / Console Meaning |
|---|---|---|
| `--ink` | `#000000` | Power-off black — dark mode ground |
| `--paper` | `#FFFFFF` | Light mode ground / dark-mode primary text |
| `--teal` | `#004D71` | Console Teal — chassis color, structural chrome, headers |
| `--crimson` | `#E0004C` | Transport Crimson — primary action / Generate (dark mode) |
| `--crimson-deep` | `#C50043` | Deep Crimson — primary action (light mode) / pressed/hover (dark) |
| `--green` | `#44A400` | Level Green — active signal, success, PWR LED, live meter |
| `--violet` | `#95009C` | Aux Violet — AI layer, generative parameters, mood tags |
| `--orange` | `#E02800` | Clip Orange — warnings, clipping, destructive actions |
| `--dust` | `#CABAC0` | Dust — secondary text (dark) / hairline borders (light) |
| `--mist` | `#E5DCDF` | Mist — light-mode panel tint / idle meter segment |

## 2. Derived Surfaces & Theme Contract

All surface and text variants are mathematically derived via CSS `color-mix()` against black, white, or palette bases:

```css
:root {
  /* Dark mode (default) */
  --bg: var(--ink);
  --surface: color-mix(in srgb, var(--teal) 14%, black 86%);
  --surface-raised: color-mix(in srgb, var(--teal) 22%, black 78%);
  --surface-sunken: color-mix(in srgb, var(--teal) 8%, black 92%);
  --border: color-mix(in srgb, var(--dust) 22%, black 78%);
  --border-strong: color-mix(in srgb, var(--dust) 40%, black 60%);

  --text-primary: var(--paper);
  --text-secondary: var(--dust);
  --text-tertiary: color-mix(in srgb, var(--paper) 45%, transparent);

  --action: var(--crimson);
  --action-hover: var(--crimson-deep);
  --action-text: var(--paper);

  --success: var(--green);
  --ai: var(--violet);
  --warn: var(--orange);
  --idle: color-mix(in srgb, var(--dust) 35%, black 65%);
  --focus-ring: var(--crimson);
}

html[data-theme="light"] {
  --bg: var(--paper);
  --surface: color-mix(in srgb, var(--mist) 55%, white 45%);
  --surface-raised: color-mix(in srgb, var(--mist) 78%, white 22%);
  --surface-sunken: color-mix(in srgb, var(--dust) 20%, white 80%);
  --border: var(--dust);
  --border-strong: color-mix(in srgb, var(--dust) 70%, black 30%);

  --text-primary: var(--ink);
  --text-secondary: var(--teal);
  --text-tertiary: color-mix(in srgb, black 45%, transparent);

  --action: var(--crimson-deep);
  --action-hover: color-mix(in srgb, var(--crimson-deep) 80%, black 20%);
  --action-text: var(--paper);

  --success: var(--green);
  --ai: var(--violet);
  --warn: var(--orange);
  --idle: color-mix(in srgb, var(--dust) 55%, black 25%);
  --focus-ring: var(--crimson-deep);
}
```

## 3. Typography & Branding

- **App Branding**: **MM3** / MiniMax Music 3.
- **Display**: `Fraunces` (variable optical axis, italic/500) — wordmark (`MM3`) and panel titles only.
- **UI / Body**: `Hanken Grotesk` (400, 500, 600, 700) — UI labels, buttons, cards, body text.
- **Data / Mono**: `IBM Plex Mono` (400, 500, 600) — prompt textarea, BPM slider value, duration controls (15s–300s / 5m max), seed, timestamps, and channel eyebrows.

### Scale
- `--text-xs` (11px / 0.6875rem): Eyebrows, meter labels (mono, uppercase, letter-spacing 0.08–0.09em)
- `--text-sm` (13px / 0.8125rem): Secondary UI text, chip labels
- `--text-base` (15px / 0.9375rem): Body text, form values
- `--text-md` (18px / 1.05rem): Panel titles (Fraunces)
- `--text-lg` (24px / 1.5rem): Status readouts
- `--text-xl` (30px / 1.875rem): Wordmark (`MM3`)

## 4. Layout Architecture (Single Header + Vertical Stack Accordion)

- **Single Topbar Header**: Clean top bar featuring MM3 icon logo, brand wordmark `MM3`, subhead `MiniMax Music 3 Console`, PWR indicator LED, navigation links, and theme toggle button. Eliminates duplicate hero banners.
- **Vertical Stack Accordion Architecture** (`.vertical-stack`, max-width 900px centered):
  - **CH.01 — LYRICS & STYLE**: Enlarged prompt/lyrics textarea (`min-height: 280px` / 12 rows), character counter, section tag inserter buttons, style caption textarea (`min-height: 180px` / 6 rows), and AI assistant trigger.
  - **CH.02 — CONFIGURATION**: BPM slider (`input[type="range"]` with `.bpm-value`), 15s to 300s (5 minutes) length control with range slider + centered numeric step input (`-` [ Input ] `+`), Key selector, Model selector, Seed input & shuffle icon button, and Vocals rocker `.toggle` switch.
  - **MASTER OUT**: Peak meter (`.meter` with 5 vertical color lanes: Mist/idle, Level Green, Aux Violet, Transport Crimson, Clip Orange), prominent `.generate-btn` with transport play icon, and `.status-line` (`READY` / `RENDERING...` / `COMPLETE`).
  - **SESSION LOG**: `#job` fragment area & song history table.
- **Mobile Responsive Design**: Full-width vertical stacking, touch-friendly tap targets (minimum 44px for buttons/toggles), and responsive textareas.

## 5. Component Specifications

- **Duration Control**: Supports 15 seconds up to 300 seconds (5 minutes). Driven by range slider + centered step number input with `-` and `+` step buttons, displaying formatted minutes and seconds (e.g. `3m 35s` / `215s`). No preset bar.
- **Buttons**: `.generate-btn` (flat Transport Crimson fill, 1px border, 4px radius, 1px inset on `:active`). Secondary buttons `.btn--secondary`, ghost buttons `.btn--ghost`, icon buttons `.icon-btn`.
- **Chips**: `.chip` patch-bay buttons (outlined rectangles, solid fill on select). Genre chips use Teal background when active; Mood chips use Aux Violet background when active.
- **Sliders**: `input[type="range"]` styled with custom webkit/moz thumb in Transport Crimson and a track background with repeating linear gradient tick marks.
- **Toggle Switch**: `.toggle` rocker switch with `.knob` element, active state green border and green knob offset.
- **Peak Meter**: `.meter` containing vertical bar elements. Idle state features low-amplitude, non-zero bar heights; rendering state animates bar heights across all 5 lanes.

## 6. Contrast & Accessibility Compliance

- Primary action button (`--action` / `--crimson` on dark, `--crimson-deep` on light) maintains ≥ 4.5:1 text contrast with `--paper`.
- Visible `:focus-visible` ring (2px solid `--focus-ring`, 2px offset) on all interactive controls.
- `prefers-reduced-motion` suppresses meter animation and toast transitions (`transition-duration: 0.001ms !important`).
- Form elements use explicit `<label>` and `aria-label` / `aria-pressed` / `role="status"` attributes.

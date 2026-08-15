# OUTBOARD — Design System
### A text‑to‑music console

---

## 0. Concept

The source palette (`colors.xml`) isn't a "brand palette" in the usual sense — it's ten
hard, fully saturated values plus black, white, and two dusty neutrals. Nothing in it is
muted or blended. That's not a coincidence you smooth over; it's the whole idea.

Those exact colors — a hot crimson, an acid green, a violet, a burnt orange, a deep
teal — are the color language of studio hardware: the LEDs on a mixing console, the
paint on a rack unit, the red ring around a record button. So **OUTBOARD** is designed
as a piece of outboard gear, not a SaaS dashboard: a channel strip for typing a
description of a sound and printing it to tape.

**Signature element:** the peak meter. Every accent color in the system is first and
foremost a *signal state* on that meter (idle / recording / rendering / AI layer /
clipping) before it's ever "just a brand color." The meter's five colors are literally
five of the ten source hexes, used with their real console meaning intact. Buttons,
tags, and status text borrow the meter's vocabulary instead of inventing a second one.

**The page's one job:** describe a sound, press Generate, audition takes. Everything
else — BPM, key, model, seed — is a rack of secondary controls around that single
action, the way real interfaces are built around one transport.

---

## 1. Color

All hex values below trace back to `colors.xml`. Where a lighter/darker step was
needed for surfaces, it's produced with CSS `color-mix()` against black or white so it
stays mathematically tied to the source swatch — never a hand-picked new hue.

| Token | Hex | Source role on the console |
|---|---|---|
| **Ink** | `#000000` | Power‑off black — dark mode ground |
| **Paper** | `#FFFFFF` | Light mode ground / dark‑mode primary text |
| **Console Teal** | `#004D71` | Chassis color — structural chrome, headers, the "monitor" meter segment |
| **Transport Crimson** | `#E0004C` | The one hot accent — Generate / record / primary action (dark mode value) |
| **Deep Crimson** | `#C50043` | Same signal, printed value — primary action on light mode, and the pressed/hover state on dark mode |
| **Level Green** | `#44A400` | Active signal — "recording," success, live meter |
| **Aux Violet** | `#95009C` | The AI layer — model badges, generative parameters |
| **Clip Orange** | `#E02800` | Warnings, clipping, destructive actions |
| **Dust** | `#CABAC0` | Secondary text (dark) / hairline borders (light) |
| **Mist** | `#E5DCDF` | Light‑mode panel tint / idle meter segment |

**Why two crimsons instead of one:** `#E0004C` is bright enough to sit on black;
`#C50043` is the same hue pulled dark enough to hold contrast on white. Rather than
picking one and letting the other rot in the file, each theme uses both — one as the
resting color, one as the pressed/hover step. The "primary action" is the same signal
in both themes, just printed at the value the ground requires.

**Discipline:** violet and orange are used sparingly and only for what they mean (AI
parameters, warnings). Green is a state, not a decoration. This isn't a five‑accent
rainbow UI — it's one hot accent (crimson) with four narrow, purpose‑built signal
colors around it, exactly like a real meter.

### Derived surfaces
```
--surface-dark   = color-mix(in srgb, #004D71 14%, #000000 86%)
--surface-raised = color-mix(in srgb, #004D71 20%, #000000 80%)
--border-dark    = color-mix(in srgb, #CABAC0 22%, #000000 78%)
--surface-light  = color-mix(in srgb, #E5DCDF 45%, #FFFFFF 55%)
--text-dim-light = color-mix(in srgb, #004D71 100%, #000000 0%)   /* used at full value, see above */
```

---

## 2. Typography

Three faces, three jobs. No Inter, no default system stack.

| Role | Face | Why |
|---|---|---|
| **Display** | **Fraunces** (variable, soft/wonky optical axis) | Used only for the wordmark and panel titles. A warm, slightly imperfect serif keeps the tool from reading as clinical/corporate even inside a hard-edged console UI — the one human note in an otherwise mechanical layout. Used with restraint: three places, max. |
| **UI / body** | **Hanken Grotesk** | A grotesque with real character in its terminals and single‑story letterforms — carries labels, buttons, chip text, paragraphs without falling back to Inter/Helvetica neutrality. |
| **Data / mono** | **IBM Plex Mono** | Every number on the page — BPM, duration, seed, timestamps — and the prompt textarea itself. Typing a prompt into a monospaced field reads like entering a command into a machine, which is exactly what text‑to‑music is. |

### Scale
```
--text-xs   0.6875rem / 11px   — eyebrows, meter labels (mono, uppercase, tracked +0.08em)
--text-sm   0.8125rem / 13px   — secondary UI text, chip labels
--text-base 0.9375rem / 15px   — body, form values
--text-md   1.125rem  / 18px   — panel titles (Fraunces)
--text-lg   1.5rem    / 24px   — status readouts
--text-xl   1.875rem  / 30px   — wordmark
```

Channel labels use the mono face, uppercase, letter‑spaced — `CH.01 — INPUT`,
`CH.02 — RACK`, `MASTER OUT`. This is the one place numbering appears, and it's
earned: a real channel strip numbers its inputs. It isn't used anywhere else on the
page.

---

## 3. Layout

Desktop: a three‑zone rack, because the interface *is* a piece of gear, not a page
with a hero image above the fold.

```
┌────────────────────────────────────────────────────────────────┐
│  OUTBOARD  text-to-music console          ● PWR   [ theme ]     │  ← header strip
├───────────────────┬──────────────────────────┬─────────────────┤
│ CH.01 — INPUT      │        MASTER OUT         │  CH.02 — RACK  │
│ [ prompt, mono ]   │   [ peak meter / wave ]    │  BPM  ▬▬▬●▬▬   │
│ GENRE  [chips]     │                            │  LEN  15 30 1m │
│ MOOD   [chips]     │      ▶ GENERATE            │  KEY  [ v ]    │
│                     │      status: READY         │  MODEL[ v ]   │
│                     │                            │  SEED # 🔀    │
│                     │                            │  VOX  ⏻ on    │
├───────────────────┴──────────────────────────┴─────────────────┤
│  SESSION LOG                                                    │
│  T01  Untitled Take   ▁▂▅▇▃▂▁   0:24   ▶  ✕                     │
│  T02  Untitled Take   ▂▄▇▅▂▁▂   0:15   ▶  ✕                     │
├───────────────────────────────────────────────────────────────┤
│  nothing leaves your browser — local session only               │
└───────────────────────────────────────────────────────────────┘
```

Mobile (< 860px): stacks in the order the task actually happens — **Input → Master
Out (the action) → Rack → Session Log**. The rack's controls collapse to full‑width
rows; the meter shortens but stays the visual center.

Grid gap and panel padding run on an 8px unit. Panel corners are 4px — enough to not
be a knife edge, not enough to read as a soft rounded SaaS card. No drop shadows;
depth comes from a 1px border and a slightly stepped surface color, the way a real
panel is separated from its chassis by a seam, not a shadow.

---

## 4. Components

- **Buttons** — flat fill, 1px border, 4px radius. Primary = Transport/Deep Crimson
  fill, white text. Secondary = transparent with border, fills on hover. Pressed state
  drops to the darker crimson step and insets 1px — a physical "pushed in" cue instead
  of a shadow.
- **Chips (genre/mood)** — outlined rectangles, mono‑ish label, fill solid on select.
  Behave like patch‑bay buttons: binary, no ambiguity about state.
- **Sliders (BPM)** — custom‑styled range input with visible tick marks under the
  track, mono numeric readout to its right that updates live. Reads like a fader, not
  a Material Design slider.
- **Segmented control (Length)** — four buttons sharing one border, single‑select.
- **Toggle (Vocals on/off)** — a rocker switch, not an iOS pill — rectangular, moves a
  hard-edged block, not a circle.
- **Peak meter** — five vertical color lanes (Mist/idle, Level Green, Aux Violet,
  Transport Crimson, Clip Orange) whose bar heights animate only while a take is
  rendering. Idle state is a low, mostly-still bed of Mist/Dust bars — never
  perfectly flat, real meters breathe.
- **Session log rows** — take number, editable name, a small static "printed"
  waveform, duration, play/stop, remove. Play scrubs a highlight across the mini
  waveform and counts the mono duration up; no real audio backend, and the footer
  says so plainly rather than pretending otherwise.

---

## 5. Motion

- Idle meter: a slow, low‑amplitude drift — always breathing, never dead, never busy.
- On Generate: one orchestrated sequence — REC dot starts pulsing, meter bars jump to
  full animated range across all five lanes, mono elapsed timer counts up, status text
  changes state (`READY` → `RENDERING…` → `COMPLETE`). One moment, not five scattered
  micro-animations.
- Hover/focus: 120ms border and fill transitions only. No scale, no bounce.
- `prefers-reduced-motion` disables the idle meter drift and the render sequence's
  looping animation; state changes still happen instantly, just without the motion.

---

## 6. Accessibility

- Body text never sits in Level Green or bright Transport Crimson on a light ground —
  those are reserved for large fills, icons, and the meter, where WCAG's non‑text
  contrast rules apply rather than the 4.5:1 text minimum.
- Deep Crimson (`#C50043`) is used for the light theme's action color specifically
  because it holds ≥ 4.5:1 on white; Transport Crimson (`#E0004C`) is reserved for
  dark backgrounds where it clears the same bar.
- All interactive elements have visible `:focus-visible` outlines in the accent color,
  2px, offset — never removed, never relying on color alone.
- Chips and toggles use `aria-pressed`; the meter and waveforms are decorative and
  hidden from assistive tech; status changes are announced via a live region.

---

## 7. Anti‑slop notes

Things deliberately avoided:
- No cream‑and‑terracotta warm-serif landing page look.
- No near‑black‑plus‑single‑neon‑accent template.
- No gradients — not on buttons, not on text, not as a background wash. Every fill on
  this page is flat. Depth comes from a faint brushed‑texture panel fill (a repeating
  linear gradient at ~3% opacity, same hue as the panel, not a decorative color wash)
  and from borders, not blur or shadow.
- No rounded‑pill everything, no glassmorphism, no sparkle/magic‑wand iconography for
  "Generate" — it's a plain filled play‑style button, because that's what it is.
- No Inter/Space Grotesk default pairing.
- Copy is plain and literal: "Generate," "Rendering…," "Untitled Take," not "Unleash
  your sound" or "AI‑powered magic."

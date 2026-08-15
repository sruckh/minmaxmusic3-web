# Timbre — Design System

A design system for a text-to-speech studio: paste a script, pick a voice, render audio on a rack of models.  Every value in this document is one of ten supplied palette colors or a `color-mix()` of two of them.  Nothing else is allowed in.

**Version 1.0** · Base theme: dark (Ink canvas) · Contrast floor: WCAG AA

---

## 1. Color

### 1.1 The palette

| # | Hex | RGB | Name | Role |
|---|-----|-----|------|------|
| 01 | `#0000FF` | 0, 0, 255 | Blue | **Decorative only** |
| 02 | `#83008C` | 131, 0, 140 | Purple | Cloned & custom voices, markup |
| 03 | `#C95D00` | 201, 93, 0 | Burnt | Refusal, limits, failure |
| 04 | `#006795` | 0, 103, 149 | Teal | Signal, waveform, information |
| 05 | `#9EB600` | 158, 182, 0 | Olive | Live speech, playhead, focus |
| 06 | `#C98500` | 201, 133, 0 | Amber | Work in progress |
| 07 | `#009044` | 0, 144, 68 | Green | Permission, completion |
| 08 | `#D5D5F4` | 213, 213, 244 | Paper | Text, light base |
| 09 | `#4A4AC8` | 74, 74, 200 | Indigo | Primary action |
| 10 | `#00003E` | 0, 0, 62 | Ink | Canvas |

Two swatches were deliberately not given semantic jobs beyond what is listed: **Blue** is restricted (see 1.4), and **Ink**/**Paper** are structural rather than expressive.

### 1.2 Why these roles

The palette is unusually well suited to speech synthesis, and the assignments follow from that rather than from convention:

- **Olive** is the brightest, highest-contrast color against Ink (8.6:1).  Speech is the product, so the brightest color marks the word being spoken right now — and the same value doubles as the focus ring, which is the keyboard user's equivalent of "you are here."
- **Teal** sits mid-range and reads as instrumentation.  It carries the waveform.
- **Amber → Green** is the render lifecycle: something is happening, then it's done.  Serverless GPUs sleep, so "warming" gets the same Amber as "rendering" — both are states that resolve without the user acting.
- **Burnt** is refusal, not danger.  It covers both "past the character limit" and "this model's license forbids commercial output," which are the two ways this product tells someone no.
- **Purple** marks provenance: a voice that came from an uploaded sample, and markup tags inside a script that will be obeyed rather than spoken.
- **Indigo** is the one action color.  It appears on the render button and nowhere else that competes.

### 1.3 Semantic tokens

```css
--canvas:    #00003E;                                    /* Ink            */
--surface-1: color-mix(in srgb, #00003E 92%, #D5D5F4);   /* ≈ #11114D      */
--surface-2: color-mix(in srgb, #00003E 85%, #D5D5F4);   /* ≈ #202059      */
--surface-3: color-mix(in srgb, #00003E 78%, #D5D5F4);   /* ≈ #2F2F65      */
--line:      color-mix(in srgb, #00003E 75%, #D5D5F4);   /* ≈ #35356C      */
--line-loud: color-mix(in srgb, #00003E 58%, #D5D5F4);   /* ≈ #59598F      */

--text:       #D5D5F4;                                   /* 13.7:1         */
--text-dim:   color-mix(in srgb, #D5D5F4 65%, #00003E);  /* ≈ #8B8BB4, 6.0:1 */
--text-faint: color-mix(in srgb, #D5D5F4 42%, #00003E);  /* ≈ #5E5E92, 3.4:1 — non-text only */

--primary:       #4A4AC8;
--primary-hover: color-mix(in srgb, #4A4AC8 85%, #D5D5F4);
--primary-press: color-mix(in srgb, #4A4AC8 82%, #00003E);
--on-primary:    #D5D5F4;

--speaking: #9EB600;   /* live word, playhead, focus ring */
--ready:    #009044;   /* rendered, commercial-safe       */
--working:  #C98500;   /* queued, rendering, warming      */
--limit:    #C95D00;   /* over limit, failed, restricted  */
--signal:   #006795;   /* waveform, informational         */
--cloned:   #83008C;   /* cloned voice, markup chips      */
--decor:    #0000FF;   /* gradients only — never load-bearing */
```

Tinted backgrounds are the hue at 14–22% over Ink, e.g. `color-mix(in srgb, #009044 16%, #00003E)`.

### 1.4 Contrast, measured

Against the Ink canvas (`#00003E`):

| Color | Ratio | Verdict |
|---|---|---|
| Paper `#D5D5F4` | 13.7:1 | Body text ✓ |
| Olive `#9EB600` | 8.6:1 | Body text ✓ |
| Amber `#C98500` | 6.4:1 | Body text ✓ |
| Green `#009044` | 4.8:1 | Body text ✓ |
| Burnt `#C95D00` | 4.7:1 | Body text ✓ |
| Teal `#006795` | 3.2:1 | Large text (≥19px) and UI borders only |
| Indigo `#4A4AC8` | 2.9:1 | Fills only, never text |
| Purple `#83008C` | 2.2:1 | Fills only, never text |
| Blue `#0000FF` | 2.3:1 | **Decorative only** |

Paper text on colored fills:

| Fill | Ratio | Verdict |
|---|---|---|
| Purple `#83008C` | 6.3:1 | ✓ |
| Indigo `#4A4AC8` | 4.7:1 | ✓ |
| Teal `#006795` | 4.3:1 | ≥19px, or ≥15px semibold |

Ink text on bright fills — Olive 8.6:1, Amber 6.4:1, Green 4.8:1, Burnt 4.7:1, Paper 13.7:1 — all pass.

**On Blue.**  Pure `#0000FF` is the most striking swatch in the set and the least usable: 2.3:1 on Ink, 4.6:1 on Paper, and it fringes badly against navy on LCD subpixel rendering.  It appears once, as a bar in the masthead spectrum, where nothing depends on reading it.  Anything else is a bug.

### 1.5 Deriving new values

New colors are produced only by mixing two palette colors.  Never sample from outside the ten, and never reach for a neutral gray — gray is what happens when the mix step gets skipped, and it will read as a foreign body against this navy.

### 1.6 Light theme

Invert the two structural colors: Paper becomes the canvas, Ink becomes the text.  Semantic colors stay identical except **Olive**, which needs an Ink text color on top (8.6:1) instead of being used as text, and **Amber**, which drops to 2.1:1 on Paper and must move to a filled chip.

---

## 2. Typography

| Role | Face | Size / weight | Tracking |
|---|---|---|---|
| Display | Bricolage Grotesque | 44 / 800 | −0.04em |
| Title | Bricolage Grotesque | 26 / 600 | −0.02em |
| Head | Bricolage Grotesque | 16 / 600 | −0.01em |
| Body | Instrument Sans | 15 / 400, 1.6 | 0 |
| Small | Instrument Sans | 13 / 400 | 0 |
| Label | IBM Plex Mono | 11 / 400, uppercase | 0.14em |
| Data | IBM Plex Mono | 13 / 400, tabular | 0 |
| Script | Instrument Sans | 19 / 400, 1.85 | 0 |

Three faces, three jobs.  Bricolage Grotesque names things — it has the slightly irregular, cut-metal quality of studio equipment lettering.  Instrument Sans carries reading, including the script itself, set larger and looser than body copy because it is content being performed, not chrome.  IBM Plex Mono handles anything measurable: durations, sample rates, seeds, character counts, GPU seconds.  If a number changes while you watch it, it is mono and tabular.

Fallbacks: `Archivo → system-ui` for display, `system-ui` for body, `ui-monospace → Menlo` for mono.

---

## 3. Space, shape, motion

**Space** — 4px base: `4, 8, 12, 16, 24, 32, 48, 72`.

**Radius** — `4px` chips and inline marks · `8px` controls · `14px` containers · full round for pills and transport buttons.

**Depth** — no shadows.  On a navy this deep they read as smudges.  Elevation is a surface mix ratio and a 1px `--line` border.

**Motion** — three animations exist in the entire product:
1. the playhead advancing through waveform and script,
2. the masthead spectrum breathing (3.2s, ambient),
3. the render spinner (0.7s).

Everything else transitions at 120–160ms ease on color and border only.  `prefers-reduced-motion: reduce` stops the spectrum, slows the spinner, and steps the playhead at 250ms instead of animating it.

---

## 4. Components

**Buttons** — Primary (Indigo fill) is reserved for synthesis, the action that costs GPU seconds; one per view.  Secondary is an outline on `--line-loud`.  Quiet is text-only.  Danger is a Burnt outline, not a fill — deleting a take is recoverable and shouldn't shout louder than rendering one.

**Focus** — 2px Olive, 2px offset, on every interactive element in both themes.  This is the only place the brightest color in the palette lands on a small element, which is exactly why it registers.

**Badges** — one component, two independent axes.  *Render state*: Ready (Green), Rendering / Warming GPU (Amber), Queued (Teal), Failed (Burnt).  *Voice state*: Commercial-safe (Green), Non-commercial (Burnt), Cloned voice (Purple), Speaking (Olive).  Every badge pairs its color with a word; color is the second signal, never the only one.

**Fields** — labels are mono uppercase, inputs sit on `--surface-2`.  Sliders fill Indigo behind a Paper thumb ringed in Indigo.  Toggles turn Green when on, because "on" is permission.

**Script editor** — the widest, quietest surface in the product.  Inline markup renders as Purple chips so a writer can see at a glance which characters will be spoken and which will be obeyed.  A character meter sits under it in Olive; when the script exceeds the model's limit, the field border and helper text turn Burnt and the message names a model that can handle the length.

**Voice card** — name, model and license, a fingerprint of the sample in Teal, and a license badge.  Selection is an Indigo border plus a 12% Indigo wash; the whole card is the target, not a checkbox.

**The spoken line** *(signature component)* — waveform and script are one instrument.  The played portion of the wave is Teal, the bar under the playhead is Olive, unplayed bars are `--line-loud`.  In the script below, spoken words settle to `--text-dim` and the word being spoken sits in Ink on an Olive block.  This is the only place in the product where color moves, and it is the reason Olive is protected everywhere else.

**Render queue** — a table, because it is data.  Warming a cold endpoint is a named state rather than a disguised spinner, and every row reports what it cost in GPU seconds.

**Messages** — inherit the status colors exactly, with a 3px left border and a 16% tint.  State what happened, then what to do about it.

---

## 5. Voice and copy

Sentence case, plain verbs, no filler.  Name things by what the person controls: *Pace*, not `speed_factor`; *Expressiveness*, not `temperature`.  An action keeps its name through the whole flow — the button says **Render speech**, the toast says **Render finished**.

Errors don't apologize and are never vague: "This voice can't ship — EchoTTS is licensed for non-commercial output.  Pick a commercial-safe voice to use this audio in client work."  Empty states are invitations, not moods.

---

## 6. Quick reference

```
Ink        #00003E   canvas, text on bright fills
Paper      #D5D5F4   all body text
Indigo     #4A4AC8   render button, selection, slider fill
Olive      #9EB600   live word, playhead, focus ring
Teal       #006795   waveform, queued, information
Amber      #C98500   rendering, warming, queued work
Green      #009044   ready, commercial-safe, on
Burnt      #C95D00   over limit, failed, non-commercial, delete
Purple     #83008C   cloned voice, markup chips
Blue       #0000FF   masthead spectrum only
```

`index.html` is the living reference — it is the system rendered in itself, and any change to a token should be visible there first.

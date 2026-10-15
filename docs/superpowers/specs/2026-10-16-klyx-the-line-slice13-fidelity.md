# The Line slice 13: design fidelity

Parent spec: `2026-10-14-klyx-the-line-redesign.md` (§2 visual system, §4 chrome, §5 drawings, §9 every lens).

The owner compared the built app with the mockups on 2026-10-16 and asked for the clean-up and polish they still need. The mockups are the "Klyx redesign" canvas: Fleet, Cluster (Flux diagnosis), Route, Console and Ask. Their sources and the owner's screenshots are in `docs/reviews/fidelity-2026-10-16/` (untracked).

## 1. What is wrong, and why

**One cause covers most of it.** Slice 11a moved the rows to 40px, but the workspace scale in `layout/layout.css` is still the 2026-10-10 layout's, where the parent spec (§2.2, §2.3) says otherwise:

| Element | Today | Spec and mockup |
|---|---|---|
| Lens title | 15px | 22px |
| Object title | 15px / 600 | 19px / 500 mono |
| Inspector verdict | a chip and a paragraph | 19-20px / 600 sentence |
| Body text | 13px | 14px |
| Meta text | 11px | 12px |
| Legend | 9px | 11px |
| Section label | 11px / 600 sans | condensed caps, 12px, tracked 0.16em, ink-3 |
| Controls and buttons | 24-28px | 36px |
| Panel padding | 12-16px | 20-24px |
| Table header | 24px | 36px |

The rest are specific gaps the screenshots show:

1. **Tables have no panel.** Rows sit in a sunk well that runs to the bottom of the pane. The mockup's table is a raised panel (hair border, radius 10) that ends after its last row.
2. **Cells are too loud.**
   - Status words are mono and bold.
   - A fault's message is a whole red paragraph, in rows and in the inspector.
   - Flux kinds are abbreviated ("ks", "hr").
   - Ages read "127h ago".
   - Syncing is a grey chip.
3. **The inspector's order breaks parent §4.** In Flux, Gateway, cert-manager and ESO the drawing is a child section *after* the actions. The order should be verdict, drawing, actions, evidence.
4. **The location bar truncates its crumbs** ("DEV · WESTEURO…", "P…", "OVERVIE…").
   - The object crumb keeps its width while the lens crumb gives way.
   - Regions show raw ("westeurope").
   - The mark is a framed glyph, not the rails and sleeper with the KLYX wordmark.
5. **The lens index repeats lenses.**
   - "Flux CD · Sources" and "Argo CD · ApplicationSets" are top-level rows.
   - A part note is cut to "rout…".
6. **The station page is dense and unweighted.**
   - The verdict sentence is entirely danger-coloured.
   - Needs-you rows carry three-line raw messages.
   - The facts repeat Connection's nested explanations.
   - By area still uses ▲ and ▶.
   - The warning-events columns are spread across the width.
   - Risk kinds are amber.
   - At a 2000px window something overflows at the right edge.
7. **The fleet board's stations print raw connection errors,** and the track label reads "WESTEUROPE".

## 2. What changes

Nothing changes a count, a verdict, a gate or a data path. Each item is a presentation change, unless it says otherwise.

### 13a. The scale and the chrome

1. **Text roles** (`layout.css`), to parent §2.2:
   - `t-workspace-title`: 600 22/28 sans, tracking -0.005em.
   - `t-object-title`: 500 19/24 mono.
   - `t-body`: 14/20.
   - `t-secondary` and `insp-meta`: 12/16.
   - `t-legend`: 11/14.
   - `t-section-label`: Barlow Condensed 500 12/16, uppercase, tracking 0.16em, ink-3.
   - `t-status`: 500 13/18 sans; colour still comes from the caller.
   - `t-number`: 500, not 600.
2. **Geometry:**
   - Header height becomes auto, at least 56px, with 20px top padding.
   - Controls are 36px.
   - Inline padding is 24px.
   - The table header is 36px.
   - The mast is 52px.
   - In the inspector: identity padding 20/24, verdict padding 4/24/20, body padding 0/24/32, and a 22px gap between sections.
3. **Controls** (§2.3: 36px visual, a hit area of at least 40px):
   - `l-btn` is 36px, 14px text, radius 6, with a 1px solid `--line-control` ring: a control boundary is solid (§2.1).
   - `l-btn-primary` is a route fill with route-ink text at 600.
   - `l-input` and the selects are 36px sunk wells with a 1px solid `--line-control` ring and 13px text: a meaningful boundary is never a hairline (review 1).
     - Their box is 40px, with transparent 2px top and bottom borders and the background clipped to the padding box. The ring and fill read 36px; the hit area is 40px.
   - **Filter chips become a segmented control** (`l-seg`, a labelled `role="group"`): a sunk group with a hair ring, padded 3px, with 30px buttons.
     - The pressed button is raised, in ink-1 at 500, *with* a 1px solid `--line-control` ring. A lightness step alone never carries the selection (review 1).
   - Buttons and segment buttons extend their hit area to 40px with a transparent `::after`.
   - This replaces slice 11d's route-wash-and-edge pressed style, because the mockup uses the segmented control. `aria-pressed` and the keyboard are unchanged.
4. **Location bar:**
   - **The mark:** the mockup's rails-and-sleeper mark plus the condensed "KLYX" wordmark.
   - **Crumbs:** condensed 600 14px, tracked 0.12em, with the cluster code in mono 12px ink-3.
   - **Shrink order:**
     1. the object crumb first (the inspector's title names it right below);
     2. then the cluster code;
     3. then the place.
     The rank and the lens never truncate. This reverses slice 4's "lens gives way first".
   - **Regions read as places:** `placeName()` turns Azure ids into "West Europe" and "UK South". AWS and GCP codes, and anything unknown, are shown as given. One helper serves the crumb, the station line, the board's track labels and the console.
5. **Lens index:**
   - A tool lens's lit sub-task sits indented under its lens in Needs you ("Sources 1" under "Flux CD 3"), never as a "Lens · Sub" top-level row.
   - **Grouping rules** (review 1). This is the one exception to the flat ordering:
     - A group is placed by its highest-priority lit member, under the existing comparator: danger before warning, then count, then registry order.
     - Inside a group, the lens comes first, and its lit subs follow in the same order.
     - Every member keeps its own count, tone and Aspect. Nothing is summed.
     - **When only a sub-task is lit,** the lens row is context: its label alone, with no Aspect, no count and never an invented attention count. The lit sub carries the mark and the count.
     - **A sub at danger under a lens at attention** ranks the group as danger, and each row keeps its own tone.
   - Part notes ("routes: 3 with a failing attachment") leave the row and stay in its title. The lens states them.
   - Coverage words ("unverified", "unreadable", "controller not ready", "last good", "checking…") stay on the row, at 11px ink-3: they qualify the count, so they must stay visible.
   - Rows are 34px with 14px labels, as in the mockup.

### 13b. Lens headers and tables

1. **The title is the lens's index label:** "Flux CD", "cert-manager", "Gateways and routes".
   - A tool lens with several tasks shows a part switch in its header, a segmented control with each part's nav count: "Reconciliations | Sources 1".
   - The part switch replaces view-specific titles like "Flux reconciliation" and "Flux sources", and the cert-manager lens's existing Certificates and Issuers tabs.
   - Its counts are navCounts' attention counts, with their markers and qualifiers.
   - Both parts now share one h1, so the title alone can no longer show which part is on screen. The headless walk and the native smoke therefore prove the active part: the pressed part and its own content. A negative case covers a switch that leaves the wrong part showing (review 1).
2. **Count sentence:**
   - Barlow 13 ink-3 with tabular numbers.
   - The attention phrase keeps its Aspect and colour.
   - Lens text and numbers are unchanged.
3. **The table panel** (`LensTable` and `LensLayout`):
   - The panel is raised, as the mockup draws it. Parent §2.1's token table names sunk for "list wells"; sunk stays for inputs, the board, canvases and inset evidence panels.
   - A raised panel with a hair ring and radius 10.
   - It is sized to its rows, and scrolls inside when the rows are taller than the space.
   - The header row is 12px ink-3 with a hair rule, and rows have hair separators.
   - The keyboard hints and the "n of m" line become a 12px caption under the panel.
   - Virtualised lists keep a measured viewport: the panel's height is min(content, available).
4. **Cells:**
   - **Kind:** the full word in ink-3 13px ("Kustomization", not "ks").
   - **Name:** stays "namespace / name". The namespace is mono 12 ink-3 and the name mono 13/500 ink-1.
     - This is a deliberate deviation from the mockup's separate Name and Namespace columns: the responsive column folding (`columns.ts`) depends on the one cell.
   - **Status:** Barlow 13/400.
     - The cell keeps the lens's evaluated label word for word, with its qualifiers ("last observed", "unverified", generations). Only the typography changes.
     - There is no reason-only rule: it could hide a Certificate's "Expired" while its Ready is still True, or a Gateway's "last observed" (review 1).
     - A fault or look-at label is in danger or caution.
     - A healthy row keeps its word ("Ready", "Running") in ink-3, as §2.1 requires.
     - Syncing is ink-3 text, "Syncing… generation 12, observed 11", with no chip.
   - **Ages:** compact everywhere in tables: "40s", "18m", "3h", then days from 48h ("5d"), with no "ago".
     - It is one formatter in `chrome/format.ts`. The absolute time stays on hover.
5. **Faults are coloured by their mark and their status word, never by whole paragraphs.**
   - Row messages become ink-3, one line, clamped.

### 13c. Inspector

1. **Identity:**
   - kind in 12 ink-3;
   - name in mono 19/500;
   - meta in mono 12 ink-3.
2. **Verdict:**
   - The Aspect at 12px, then a 19-20px/600 ink-1 headline, balanced.
   - The headline is the evaluated label the lens already shows, with every qualifier it carries: "Failed · HealthCheckFailed", "CrashLoopBackOff", "No ready endpoints", a Gateway's "last observed". No raw condition replaces it (review 1).
   - The controller's message follows in ink-2 14px, clamped to three lines with "Show full message".
   - The chip with a danger outline is retired from the verdict.
   - No new sentence is synthesised: the mockup's "Held: waiting on infra-controllers" needs reasoning we do not claim (§5.1).
3. **Order (parent §4):** Flux Dependencies, the cert-manager chain, the ESO chain and the Gateway data path move into `InspectorLayout`'s `drawing` slot, so they come before the actions. Every other section stays where it is, after the actions.
4. **Actions:**
   - 36px buttons, with the primary filled.
   - The key hint leaves the button face for its title ("Reconcile (R)"); the footer caption and the ? overlay still list it.
5. **Section heads:** `InspectorBlock` uses the new section label, with the chevron after the label. Collapsing is unchanged.
6. **Drawings (`Rail`):**
   - 12px marks with 2px connectors;
   - each node: kind in 12 ink-3, name in mono 14/500, its line in ink-2 14, detail in ink-3 13.
   - Direction and labels are unchanged:
     - the tree runs from this object to its upstreams;
     - "upstream failure" and "reported chain ends here" stay as they are;
     - no "root cause" (§5.1).
7. **cert-manager's issuer panel:** the ▲ and the danger border go. It becomes a sunk panel (radius 8, hair ring) headed by the Aspect and the issuer's reason, with its message in ink-2.
8. **Key-value evidence** (`insp-kv`):
   - labels in Barlow 13 ink-3;
   - values in Barlow 13 ink-2;
   - identifiers in mono.

### 13d. The station page and the fleet board

1. **Station header:**
   - the station name at 22px;
   - the verdict at 28px/600 ink-1, led by its Aspect (§2.2 screen verdict), with danger only on the Aspect;
   - the line reading follows as today.
2. **Needs-you rows** (station and fleet):
   - Line 1: the kind in ink-3, the name in mono 500, and the status word in danger or caution.
   - Line 2: the message, one line in ink-3, clamped.
   - The right column (area, time) is Barlow 12 ink-3. A Flux row's condition time keeps its "(condition time)" label.
3. **Cluster facts:**
   - Each tool reads short, for example "flux: 3 reconciliations not ready · 1 source not ready · controller unverified".
   - The full explanation stays in Connection, where it already is.
   - This is a wording change only. It drops no fact.
4. **By area:** Aspects replace ▲ and ▶, and the heads use the section label.
5. **Recent warning events:** a fixed grid (count, reason, object, message) with the message taking the remaining width.
6. **Resource risks:** the kind in ink-2 and the name in mono. Nothing is amber unless it is a look-at word.
7. **The 2000px right-edge overflow:** found and fixed, with a walk at 2000×1228.
8. **Board:**
   - Track labels use `placeName` plus the region code.
   - An unreachable station shows "unreachable" and a one-line reason, with the full error in its title. It is never a raw multi-line error.

## 3. Out of scope

- New data or new claims: the mockup's synthesised "Held: …" sentence, "where the fix lives" advice beyond the existing supply facts, and "root cause".
- Separate Name and Namespace columns (§13b.4).
- Any token value: the colours and the contrast test are unchanged.

## 4. Testing

**Review 1** (Codex, on 761858c):
- **Changes requested, folded in above:**
  - the control boundaries and hit areas (13a.3);
  - the grouping rules (13a.5);
  - the evaluated label kept in status cells and verdicts (13b.4, 13c.2).
- **Accepted:** 13a.3, 13a.4, 13a.5, 13b.1 and 13c.3, as amended.
- **Conditions on 13a.4:**
  - the protected cue and the fleet indicator stay at narrow widths;
  - a truncated crumb keeps its full value accessible;
  - long crumbs are tested at 1024px.


1. **Unit tests:**
   - `placeName`;
   - the compact age;
   - the lens index's indented subs and the notes moved to the title;
   - the grouping cases in Needs you: a sub-task lit alone, and a sub at danger under a lens at attention;
   - the active part in the walk and the smoke, including a failed switch;
   - the inspector drawing order for the four drawings;
   - the part switch's counts, which match navCounts.
2. **Existing suites updated** where they assert old words.
3. **Gates:** tsc, eslint, vitest and vite build.
4. **The headless walk (`pw/s12.mjs`):** night and day at 1024, 1440 and 2000, with captures beside the mockups.
5. **The native WKWebView smoke** at 1440 in both themes.
6. **The parity gate:** re-run. It must be unchanged, since no count moves.

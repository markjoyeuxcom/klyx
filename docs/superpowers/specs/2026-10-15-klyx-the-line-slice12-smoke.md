# The Line, slice 12: smoke

Design note for slice 12 of `2026-10-14-klyx-the-line-redesign.md` (the parent).
- **Parent sections:** §11 slice 12 (on the kind lab, in both themes, in the native app's WKWebView as well as headless Chromium, with an eyeball checklist) and §12's visual and parity lines.
- **Out of scope:** the v1.0.0 release gate's other items (the real-cluster smoke of health 1c, the earlier eyeball checklists, a real-LLM smoke, the write paths, the Windows exec check).

## 0. Constraints

- **The kind lab only:** `kind-klyx`, plus the parity gate's own `kind-klyx-parity`. Never a real cluster.
- **No screenshots of the owner's screen.** Headless Chromium captures are files; the native app reports what it rendered as data, never as an image.
- **No model or vendor calls.** The chat drawer is opened, never sent a question. Settings > AI is not touched.
- **No OS notification is shown on the owner's desktop without asking.** The native smoke leaves notifications off. Their click-through is on the eyeball checklist.

## 1. Sub-slices

| | Builds |
|---|---|
| **12a** | The headless smoke: the Go lab tests, the parity gate's Go and frontend stages, and a scripted walk of every screen in both themes at two widths |
| **12b** | The native WKWebView smoke: a `smoke` build tag that walks the same screens inside the real app's WKWebView, in a hidden window, and writes what each rendered to a JSON report |
| **12c** | The eyeball checklist for the owner: what only a person at the native app can judge |

## 2. 12a: the headless smoke

1. **Go lab tests:** `KLYX_LAB_CONTEXT=kind-klyx go test -count=1 -run 'TestLab' ./internal/...`, recorded per package.
2. **The parity gate,** both stages, re-run in full:
   - `scripts/lab/klyx-parity.sh up`, then the Go stage (`TestParityGate`), then `parityGate.test.tsx` against its output, then `down`.
   - The run's id, timings and result are recorded as in the slice 6 note §7. A failing case is fixed as its own reviewed commit before slice 12 continues.
3. **The walk:** `pw/s12.mjs` against server-mode Klyx on `kind-klyx` (`cfg6c`, with two clusters whose contexts are missing, so the line has stations that cannot be read), in night and day, at 1440×900 and 1024×768.
   - **The fleet board:** the verdict, the board and legend, "Needs you" with each filter, and the Ledger and Horizon.
   - **The station page:** the verdict's two lines, its queue opening a row pinned to its UID, and its lower sections.
   - **Every lens, by the name the lens index shows it.**
   - **One inspector per kind that has a drawing:** Flux Dependencies, the Gateway data path, the cert-manager and ESO chains.
   - **The console:** an object search, verbs only, ⌘↵ beside, Escape over a drawer.
   - **Settings:** Notifications, Git and Appearance.
   - **The chat drawer:** opened and closed, never asked anything.
   - **Recorded for each step:** the location bar's words, the page header, row counts, console errors, horizontal overflow and the focused element after each keyboard step.
   - **The narrow width** checks the drawer inspector and the console's beside position.

The walk produces captures and a JSON log. A step that errors, overflows or lands on the wrong page fails the smoke.

## 3. 12b: the native WKWebView smoke

- **The tag.** `cmd/klyx/smoke.go` (`//go:build smoke`) registers a smoke hook. Release builds never carry it, and `main` calls the hook only when it is set. With `KLYX_SMOKE_OUT=<file>`, the hook does three things:
  - starts the main window hidden (WKWebView still lays out and runs script in a hidden window; the report says if a step could not measure);
  - injects `window.__KLYX_SMOKE__ = true` before the page loads;
  - binds a smoke-only `SmokeService` with one method, `Report(json)`, which writes the file and quits the app.
- **The runner.** `src/smoke/run.ts` is a dynamic import, loaded only when the flag is set, so it is its own chunk and never runs in a normal launch. It drives the same walk as 12a through the app's own navigation:
  - the registry's `navigate` and the store's intents, never typed keys or the OS;
  - for each step, the same facts as 12a, plus WKWebView-specific ones:
    - the computed font family of the mono and sans text (the bundled fonts loaded);
    - the resolved values of `--line-ground`, `--line-route` and `--line-danger` in each theme;
    - container-query layout at the inspector breakpoint (the drawer at the narrow window size);
    - that `ResizeObserver` and `structuredClone` exist.
- **The build and run:**
  - `go build -tags smoke -o <scratchpad>/KlyxSmoke.app/...` (the review app bundle), run once per theme against the kind lab's fleet config.
  - The report is compared with 12a's log: the same pages, headers and counts at the same lab state, and no step erroring.
- **What it cannot see,** and why the checklist exists: pixels, motion, focus-ring appearance, OS notifications and native window chrome.

## 4. 12c: the eyeball checklist

`docs/eyeball-checklist-2026-10-15-the-line.md`, in the form of the earlier checklists: each item an action and what to see, grouped by screen, for the owner to run in the native app in night and day.
- **The fleet board:**
  - the line reads at a glance: stations, lit segments, the legend;
  - "Needs you" order;
  - the Ledger with Git history (with a repo configured).
- **The station page:** the two verdict lines and their marks; the queue; the sections' order.
- **Lenses:**
  - 40px rows;
  - the mark column (fault filled, look-at hollow, checking dotted, syncing dashed);
  - healthy rows quiet;
  - count sentences;
  - no ▲ beside a mark.
- **Inspectors:** pinned to the object; Replaced; the drawings (dependencies, data path, chains).
- **The console:** ⌘K; search across the fleet; verbs only; beside; Escape over a drawer.
- **Health notifications:** enable them in Settings, then break an object on the kind lab and see one notification. Clicking it opens the object pinned. A recovery appears only with recoveries on. The connect listing appears in Activity.
- **Themes:**
  - night and day contrast;
  - the bench themes still selectable, without machining;
  - focus rings;
  - reduced motion.
- **Native:** window state restored; the dock badge; trackpad scrolling in long lists; zoom.

## 5. Testing

- **12a** is itself the test. Its scripts live in the scratchpad (`pw/`), as the earlier captures' do, and the note records the run.
- **12b** adds:
  - a Go test that the `smoke` tag's hook is absent from a default build: a `go list -tags ''` check that `smoke.go` is excluded, plus a build-constraint test;
  - a frontend test that the runner chunk is not imported without the flag;
  - a unit test of the runner's step list against the registry, so a new destination is never silently skipped.
- **12c** is a document.

## 6. Open points (for review)

1. **The native smoke runs in a hidden window,** so the owner's desktop is not disturbed. If WKWebView in a hidden window does not lay out (no measurable sizes), the runner reports it, and the smoke falls back to a visible window only with the owner's consent.
2. **The runner navigates through the app's own registry and intents,** not simulated keys: the keyboard paths are covered by 12a's headless walk and the frontend tests.
3. **The smoke build is a separate bundle in the scratchpad,** never the release app, and its `SmokeService` exists only under the tag.
4. **OS notifications stay off in the automated smoke.** Their delivery and click-through are checked by the owner (12c).

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

### 3.1 The smoke build is its own app

- **The tag.** `cmd/klyx/smoke.go` (`//go:build smoke`) sets a package-level smoke hook. Ordinary builds leave it nil, and `main` consults it only when it is set.
- **A separate identity, set before `application.New`:**
  - The Wails single-instance lock is keyed by `UniqueID`, and a collision notifies the running instance and exits with status 0. The smoke build therefore runs as `com.moomora.klyx.smoke`, so it starts and reports while ordinary Klyx is open.
  - Its bundle (`<scratchpad>/KlyxSmoke.app`) has its own `CFBundleIdentifier`.
  - Its config is the lab's fleet config, passed explicitly. Its window state, activity log and every other per-app store live under a smoke-only directory, so the owner's normal app is never read or written.
- **Provenance:** the frontend assets are built fresh from the commit before they are embedded. The report records the commit, a clean-tree flag and the asset bundle's hash.
- **Release builds never carry it:** `SmokeService` and the hook exist only under the tag.

### 3.2 Starting the runner, race-free

This Wails version runs `WebviewWindowOptions.JS` after `WebViewDidFinishNavigation`, so the hook cannot set a flag before the page loads. It uses a post-load handshake instead:
- The hook's window `JS` sets `window.__KLYX_SMOKE__ = true` and dispatches a `klyx-smoke` event.
- The app shell checks the flag when it mounts and listens for the event, so it starts the runner whichever happens first. It loads the runner by dynamic import, as a chunk of its own; an ordinary launch never imports it.
- The runner calls `SmokeService.Report` by name through the Wails runtime, not through generated bindings, which ordinary builds do not have.

### 3.3 Bounded, and failing on anything unmeasured

- **A Go-side watchdog**, independent of the page, writes a failure result (`{"result":"incomplete","why":…}`) and quits:
  - if the runner has not called `SmokeService.Started` within 30 seconds of the window opening;
  - if `Report` has not arrived within 15 minutes (5 until coverage S3b, when the walk outgrew them; the incomplete result names the step the runner last began).
- **Every runner step is bounded:** each wait (readiness, a lens's header, a drawing) has its own timeout, and a timeout fails the step with its name.
- **Every check is an assertion with an expected value,** never a recorded value alone:
  - **Pages:** the location bar names the expected lens; the header is present; no console error; no horizontal overflow.
  - **Focus** after each keyboard-equivalent action is the expected element: the inspector's identity after opening beside, the console input while open.
  - **Layout:** at the narrow window the inspector is a drawer (`.lens-drawer` present); at the wide one it is split.
  - **Tokens:** in each theme `--line-ground`, `--line-route` and `--line-danger` resolve to the spec's values (parent §2.1).
  - **APIs:** `ResizeObserver`, `structuredClone` and `CSS.supports('container-type: inline-size')` are present.
  - **Geometry:** the lens body, rows and inspector have non-zero sizes. Zero or unmeasurable sizes, as a hidden window might give, fail the step as "unmeasurable", never pass.
  - **Fonts:** `document.fonts.load()` is awaited for every face `theme/fonts.css` declares (Barlow 400/500/600, Barlow Condensed 500/600, DM Mono 400/500), then each matching `FontFace` must have `status === "loaded"` and `document.fonts.check()` must be true. A missing or blocked font asset fails the check. The computed families are recorded besides.
- **The result** is `pass` only when every step passed. Any failed, timed-out or unmeasurable step makes it `fail` or `incomplete`, with each reason listed.
- **Hidden first.** If the hidden window proves unmeasurable, the smoke is not passed. A visible run happens only with the owner's consent.

### 3.4 The run

`go build -tags smoke` into the smoke bundle, then run once per theme (the theme passed by environment) at a wide and a narrow window size against the kind lab:
- first while ordinary Klyx is not running;
- once with it running, to prove the separate identity.

The report is compared with 12a's log for the same lab state: the same pages, headers and counts.

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
- **12b:**
  - **Go:**
    - the hook and `SmokeService` are absent from a default build (a build-constraint test);
    - the smoke identity differs from the release one;
    - the watchdog writes an `incomplete` result and quits when the runner never starts, and when `Report` never arrives, with a fake clock.
  - **Frontend:**
    - the runner chunk is not imported without the flag or event;
    - the handshake starts it from either order of flag and mount;
    - the step list covers every registry destination, so a new one is never silently skipped;
    - a deliberately failed assertion, a timed-out step and an unmeasurable geometry each yield a non-pass result;
    - the font check fails when a face does not load.
- **12c** is a document.

## 6. Rulings (design review of 9c72ca5)

1. **The hidden window is accepted,** with §3.3's rule: unmeasurable is never a pass. A visible fallback happens only with the owner's explicit consent.
2. **Navigation through the registry and intents is accepted** for the native smoke. Keyboard coverage stays with the Chromium walk and the frontend tests. The start is race-free through §3.2's post-load handshake, since this Wails version runs the window's `JS` after navigation finishes.
3. **The smoke tag and its separate bundle are accepted,** with §3.1's separate identity, fresh assets and recorded provenance. `SmokeService` is absent from ordinary builds, and a normal launch never imports or runs the runner.
4. **OS notifications stay off in the automation.** Their native delivery and click-through are the owner's checklist work, never an automated pass.

Review findings answered: the single-instance lock (§3.1), bounded progress and explicit failure (§3.3), and font loading (§3.3).

## 7. As built: 12b

- **`internal/smoke`:**
  - `Watchdog`: `Arm`, `Started`, `Progress`, `Report`. A clock seam stands behind the deadlines, `StartWithin` (30s) and `ReportWithin` (15m since coverage S3b).
  - The first result is the only one, written with the settings and provenance, then the app quits.
  - A report that is not JSON or names no result is written as incomplete.
  - `BuildProvenance` takes the commit and modified flag from the build's VCS stamp, and a SHA-256 of the embedded `frontend/dist`, by path and content.
- **`cmd/klyx`:**
  - `smokehook.go` declares a nil `smokeHook`.
  - `smoke.go` (`//go:build smoke`) requires `KLYX_SMOKE_OUT` and `KLYX_CONFIG`, and supplies four things:
    - `UniqueID` `com.moomora.klyx.smoke`;
    - `SmokeService` (`Settings`, `Started`, `Report`);
    - a hidden window at the run's size whose `JS` raises the flag and dispatches `klyx-smoke`;
    - `Start`, which arms the watchdog with `app.Quit`.
  - `main` builds its service list and identity before `application.New`, consults the hook only when set, and leaves window state alone in a smoke run.
- **Frontend:**
  - `smoke/boot.ts` starts the runner once, from the flag at the shell's mount or the event, by dynamic import. A production build puts the runner in its own chunk (`run-*.js`, 4.6 kB), the only one naming `SmokeService`. The main bundle carries only the listener.
  - `smoke/run.ts` walks in this order:
    - the theme and its three tokens;
    - the web platform (`ResizeObserver`, `structuredClone`, container queries);
    - every declared font face, loaded;
    - the lab cluster connecting;
    - the fleet board;
    - every registry destination, by `navigate`: the page and its location bar or kind, with a measurable body. A destination absent on the lab is named so;
    - the inspector at this width: a drawer when narrow, split when wide, with focus inside the drawer.
  - Each step is bounded, fails on a console error or overflow, and is unmeasurable (incomplete) when a box has no size.
- **The bundle:** `<scratchpad>/smoke/KlyxSmoke.app`, with `CFBundleIdentifier` `com.moomora.klyx.smoke` and `LSUIElement` (no Dock icon), and the lab's fleet config in `<scratchpad>/smoke/cfg`. WebKit keeps its stores per bundle identity, apart from the installed app's (`io.moomora.klyx`).
- **Tests:**
  - **Go:** the watchdog's four outcomes (never started, never reported, first result only, no result never passes), the asset hash, and the build constraint (`smoke.go` only under the tag).
  - **Frontend:** the handshake in both orders and never without the signal; the bounded wait; unmeasurable geometry; the tokens; a face that does not load; the verdict; every registry destination stepped; a run with nothing laid out reporting a non-pass.
- **A trial run** found that a row picked up before its page had mounted measured 0×0. The step now waits for the page, then for a row with a measurable box.

### 7.1 Review of 4ef8c0b

Codex requested changes. Each is answered below, and the runs in §8 were repeated afterwards.
- **A destination's page is proven,** never assumed:
  - **Its title:** each task's own title (`TITLES`); a kind shortcut such as Deployments shows its owning lens's title; a raw kind shows the kind.
  - **Settled:** the body leaves loading.
  - **Measurable:** the body has a size.
  - **Recorded:** the title and rows.
  - A wrong header, or a body that never leaves loading, fails (the review's probe is ported).
- **The native walk covers the planned flows,** through DOM events and the store, never OS keys:
  - the queue's three filters, the Ledger and Horizon, and a queue row opening its object pinned (no `data-pinned-gone`);
  - the station page's two verdict lines and queue;
  - the four drawings, each asserted as drawn: the dependency graph, the issuance chain's rail, the ExternalSecret chain's rail and the data path;
  - the console: focus in it on open, a fleet search with hits, verbs only, open beside (aside, the inspector opened, focus kept in the console, not opened as gone), and Escape focusing the inspector's identity;
  - Settings' Appearance, Git and Notifications;
  - the chat drawer, opened and closed without a question.
- **A null or non-object report is incomplete.**
- **Two product defects** were found by the smoke and fixed on their own:
  - **d1f9f31:** `servedGVR` inserted a probed version into a spec that already named one, so Gateway API's Services and EndpointSlices were served as `v1/v1/services` and an indexed Service opened as removed.
  - **bdeb16a:** at a narrow width, opening an object beside from the console opened the inspector as a drawer whose open effect took focus from the still-open console. The drawer now takes focus only when the console is not open.
- **A lab fixture** gives the issuance chain something to draw: `<scratchpad>/smoke/lab-chain.yaml`, one CertificateRequest owned by the Issuing certificate on `kind-klyx` only, labelled `klyx.dev/lab=smoke` for removal.
- **The eyeball checklist** stays on `kind-klyx`, with named lab objects and the right actions (`set image`, not scale).

## 8. The runs (2026-10-15, kind lab)

- **12a, Go lab tests:**
  - `KLYX_LAB_CONTEXT=kind-klyx go test -count=1 -run TestLab ./internal/...` passes.
  - `TestLabIndex`, `TestLabObjectWrites` and `TestLabDependencyGraph` ran and passed.
  - `TestLab` and `TestLabChurn` skip by design: they are the 1c load-budget lab and need the 5,000-pod `klyx-load` fixture.
- **12a, the parity gate:**
  - `klyx-parity.sh up` took 40s.
  - `TestParityGate` passed in 80.7s (run `ddaa4964ff399cd3`, commit `9c72ca5`, clean, capability re-probe 10s).
  - The frontend stage passed 2/2 on that output, then `down`.
- **12a, the walk** (`pw/s12.mjs`, server-mode Klyx at `bdeb16a` on the lab fleet config): night and day at 1440×900 pass 35/35; at 1024×768, 36/36.
  - Each lens is asserted by its own title, settled.
  - The two chains and the data path are asserted as drawn.
  - The console's beside step keeps focus in the console and does not open as gone. It opens the Service `shop/web`, which the GVR defect had opened as removed.
  - The station page waits, within a bound, for a just-connected cluster's summary to arrive.
- **12b, the native smoke:** `KlyxSmoke.app` built at clean commit `bdeb16a` from fresh assets (hash recorded in each report), hidden window. Night and day at 1440×900 and 1024×768 all `pass`, 113/113 steps, with the inspector split at 1440 and a drawer at 1024.

  The installed Klyx (`/Applications/klyx.app`) held the ordinary `com.moomora.klyx` lock throughout, observed read-only with `lsof`, so the separate identity is proven in use. The owner's app and data were not touched.
- **12c:** `docs/eyeball-checklist-2026-10-15-the-line.md`.

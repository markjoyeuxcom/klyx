# The Line slice 16: the inspector widens and expands

Owner request, 2026-10-16, while trying slice 15 (a CRD definition inspector with a horizontally scrolling printer-columns table): "The side bar on the right allow stretching more left or button that you can click to make it pop out".

## 1. What was wrong

- The inspector was already resizable, but the layout spec (2026-10-10, §2) capped the split at 560px whatever the window, so on a wide monitor it stopped far short of the space available.
- The separator's grip was invisible until hovered, so the panel did not read as resizable at all.
- Nothing let the inspector take the workspace. Wide content had only the panel's width: printer columns with descriptions, long condition messages, spec trees and the manifest.

## 2. What changes

**Resize without a fixed ceiling.** This revises the layout spec §2: the split maximum `min(560, innerWidth - bodyMinimum - 8)` becomes `innerWidth - bodyMinimum - 8`.
- The body keeps its minimum (600px; 640px for a canvas), so the list stays usable; the inspector may take the rest.
- The requested width is still stored apart from the effective one: a request wider than the window waits for a wider window, and is never cut down by a narrow one. Stored values up to 4,096px are honoured.
- Keyboard: End drags to the widest the body allows, Home to the minimum, and the arrows step 16px. `aria-valuemax` is the current maximum.
- The drawer (below a 1040px workspace) keeps its 560px ceiling: it overlays a narrow workspace and should not cover it.

**The grip is visible at rest.** It sits at 70% opacity in the border colour and full on hover or focus, under a title: "Drag to resize · double-click to reset".

**Expand.** A button in the inspector's identity row, between copy and close, uses the logs pane's icons (diagonal arrows).
- **Expanded:**
  - the inspector takes the whole workspace;
  - the list stays mounted behind it, hidden, so its scroll, filter and selection survive;
  - in a drawer, the drawer becomes full width.
- **Restore:** the same button, or Escape.
  - Escape runs on a capture listener ahead of the lists' and the drawer's own Escape, and marks the key handled, so restoring never also closes the inspector.
  - The expand owns Escape only while focus is inside the inspector, or nowhere. Focus in the chat drawer, the mast or the nav leaves Escape to them.
  - A field being edited keeps its Escape, and so does any surface above the inspector:
    - a menu, a dialog or the console;
    - an Escape layer: anything marked `data-escape-layer`, such as the expanded logs pane, which collapses first. Before this rule, one Escape with an expanded logs pane over an expanded pod inspector collapsed both, because the inspector's window capture listener ran before the logs pane's document capture listener.
- **Reset:** closing the inspector resets the expand, so the next object opens beside the list. Following a link inside the inspector keeps it expanded, since the inspector stays mounted.
- **Scope:** only an inspector inside a lens layout has the button. The layout owns the geometry and provides the toggle through a context (`layout/inspectorExpand.ts`).
- **A readable measure:** expanded content stops at 1,280px. The identity row (copy, restore, close) keeps the full width.
- **Tree key columns** are a share of a narrow panel and never of a wide one: the fields tree caps them at 320px and the schema tree at 420px.

**Found on the way:**
- The CRD Versions table spilled out of the panel at 400px: its Deprecated cell took the wrapping column's 280px minimum even when it said "no". It now sits in its well like Printer columns, and only a deprecation warning takes the wrapping width.
- `inEditable` moves to a leaf module (`chrome/inEditable.ts`). Importing it from `useListKeys` pulled the store and the generated bindings into the layout, and broke a test that mocks the Wails runtime only partially.

## 3. Not done: a separate window

"Pop out" could also mean its own native window. Logs already do this, but an inspector is not self-contained:
- each of the 18 inspectors reads data its lens loads into the store;
- a second webview has its own store, so every inspector would need its own loading path, pinning and live updates.

The expand covers reading wide content without that cost. A true pop-out window is worth building only if the owner wants an inspector beside another lens or on a second monitor.

## 3a. As built (review 1)

Codex reviewed 77f1b33 and found two P2s; both are fixed in one commit:

1. **Focus across the breakpoint.**
   - The aside was rendered in two different branches (split and drawer), so crossing the breakpoint remounted it. Focus fell to the body, the drawer's cleanup tried to return it to a row in the hidden list, and the next `j` moved a selection nobody could see.
   - Now the separator, scrim and inspector keep fixed places among the layout's children: the inspector is never remounted, and keeps its focus, scroll and tab.
   - The drawer returns focus only when it actually closed. A drawer that becomes a split keeps focus in its inspector, and a split that becomes a drawer keeps both its focused control and the row to return to. The return target is cleared once used.
   - `useListKeys` ignores every key but Escape while its lens's inspector is expanded (`listHidden()`), so a hidden list can never move, whatever has focus.
2. **Escape in a field in a drawer.** The expanded inspector yielded the Escape to the field, but the drawer's own closer then closed the inspector. The drawer's Escape now leaves an editable target alone, which is what the split already did through `useListKeys`. This is a deliberate change for plain drawers too: a field keeps its Escape there, and Escape from any other control still closes the drawer.

Codex's probes are now `layout/LensLayout.expand.test.tsx`, with one more case: a drawer opened from a row, round-tripped through the split, still returns focus to that row when closed.

## 4. Testing

- **Unit** (`layout/LensLayout.test.tsx`):
  - the geometry without the ceiling, and the drawer's ceiling kept;
  - End to the widest, stored;
  - a stored 900px kept;
  - expand hides the list and removes the separator, and the same button restores;
  - Escape restores before the list's Escape closes;
  - Escape in a field, or with a menu above, is not the expand's;
  - closing resets the expand;
  - no button outside a lens layout.
- **Native smoke**, "the inspector expands and restores": a GitRepository's inspector expands, the list is not drawn, the inspector fills the lens, and Escape restores it without closing it, in WKWebView.
- **Live** (`scratchpad/gen16`, kind lab):
  - at 1440: the default 400px, dragged to 600px, expanded to 1,208px;
  - at 2200: dragged to 1,360px, expanded to 1,968px with content at the 1,280px measure;
  - at 1024: the drawer expands to the full 792px workspace;
  - night and day.

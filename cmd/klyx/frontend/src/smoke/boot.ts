// The native smoke's start (The Line slice 12b, note §3.2). The smoke build
// raises window.__KLYX_SMOKE__ and dispatches `klyx-smoke` after the page has
// loaded; the shell checks the flag when it mounts and listens for the event,
// so the runner starts whichever comes first, once. The runner is a dynamic
// import - its own chunk - that an ordinary launch never loads.

type Runner = { runSmoke: () => Promise<void> };
let started = false;

export function startSmokeWhenAsked(load: () => Promise<Runner> = () => import("./run")): void {
  const go = () => {
    if (started) return;
    started = true;
    void load().then((m) => m.runSmoke());
  };
  if ((window as { __KLYX_SMOKE__?: boolean }).__KLYX_SMOKE__) go();
  else window.addEventListener("klyx-smoke", go, { once: true });
}

/** Tests only. */
export function _resetSmokeBootForTest(): void {
  started = false;
}

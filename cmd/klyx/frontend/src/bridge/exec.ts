import { useFleet } from "../store/fleet";
import { ExecService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { Clipboard, type CancellablePromise } from "@wailsio/runtime";

type ExecCommandDTO = { command: string; argv: string[]; error: string };
type ActionResultDTO = { ok: boolean; error: string };
// output is kubectl's stdout exactly; command is the same describe for the
// person's terminal with the cluster's kubeconfig pinned, commandError why
// there is none.
// withheld: Describe was not run (coverage S0b §4) - a kind that can carry
// secrets in templates, patches or values, or a resource Klyx could not
// classify. scrubbed: user info was removed from URLs, so the text is no
// longer kubectl's exact output; linesWithheld counts lines it withheld.
export type DescribeResultDTO = { ok: boolean; output: string; error: string; command: string; commandError: string;
  withheld?: string; scrubbed?: boolean; linesWithheld?: number };

// getExecCommand fetches the kubectl exec argv and display command string.
// Returns null on bridge error (bridge-level throw).
export async function getExecCommand(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
): Promise<ExecCommandDTO | null> {
  try {
    return (await ExecService.GetExecCommand(cluster, ns, pod, container)) as ExecCommandDTO;
  } catch {
    return null;
  }
}

// copyExecCommand calls GetExecCommand then copies the Command string to the
// clipboard. Returns "copied" | "error". A backend refusal (e.g. a path-list
// kubeconfig that cannot travel to a Windows terminal) also raises the action
// toast with its reason - the button's own "copy error" cannot say why.
export async function copyExecCommand(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
): Promise<"copied" | "error"> {
  const dto = await getExecCommand(cluster, ns, pod, container);
  if (!dto) return "error";
  if (dto.error) {
    useFleet.getState().setActionStatus({ kind: "error", message: dto.error });
    return "error";
  }
  try {
    await Clipboard.SetText(dto.command);
    return "copied";
  } catch {
    return "error";
  }
}

// openExecTerminal calls OpenExecTerminal. On success nothing happens visually
// (the OS terminal opens). On error it sets actionStatus so the detail panel
// toast shows the message.
export async function openExecTerminal(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
): Promise<void> {
  let result: ActionResultDTO | null;
  try {
    result = (await ExecService.OpenExecTerminal(cluster, ns, pod, container)) as ActionResultDTO;
  } catch {
    useFleet.getState().setActionStatus({ kind: "error", message: "Failed to launch terminal" });
    return;
  }
  if (!result?.ok) {
    useFleet.getState().setActionStatus({
      kind: "error",
      message: result?.error || "Failed to launch terminal",
    });
  }
  // Success: terminal opened — no toast needed.
}

// describeResource runs `kubectl describe` for one object (The Line slice
// 14). kindArg is "pod"/"node" for the core panels, plural.group for typed
// instances and singular.group from the lens inspectors. The generated call
// is returned unwrapped - never through an async function - so its cancel()
// reaches Go and stops kubectl when the sheet closes or moves to another
// object. A failed run comes back as a value ({ok:false, error}); a rejected
// call (cancelled, or a bridge failure) is the caller's to settle.
export function describeResource(cluster: string, kindArg: string, namespace: string, name: string): CancellablePromise<DescribeResultDTO> {
  return ExecService.Describe(cluster, kindArg, namespace, name) as CancellablePromise<DescribeResultDTO>;
}

// copyDebugCommand copies the kubectl debug command (ephemeral busybox shell
// targeted at the container) to the clipboard. Returns "copied" | "error".
export async function copyDebugCommand(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
  image = "",
  profile = "",
): Promise<"copied" | "error"> {
  try {
    const dto = (await ExecService.GetDebugCommand(cluster, ns, pod, container, image, profile)) as ExecCommandDTO;
    if (!dto) return "error";
    if (dto.error) {
      useFleet.getState().setActionStatus({ kind: "error", message: dto.error });
      return "error";
    }
    await Clipboard.SetText(dto.command);
    return "copied";
  } catch {
    return "error";
  }
}

// openDebugTerminal opens the OS terminal running kubectl debug against the
// pod. Errors surface via the action toast, same as openExecTerminal.
// openDebugTerminal attaches an ephemeral debug container to the pod OBJECT
// the verb was armed against (uid, design §5b): the backend confirms it
// before kubectl debug starts, and a replaced pod launches nothing.
export async function openDebugTerminal(
  cluster: string,
  ns: string,
  pod: string,
  uid: string,
  container: string,
  image: string,
  profile: string,
): Promise<void> {
  try {
    const r = (await ExecService.OpenDebugTerminal(cluster, ns, pod, uid, container, image, profile)) as ActionResultDTO;
    if (!r?.ok) {
      useFleet.getState().setActionStatus({ kind: "error", message: r?.error || "could not open debug terminal" });
    }
  } catch (e) {
    useFleet.getState().setActionStatus({ kind: "error", message: String(e) });
  }
}

// Coverage S6 §6: the debug dialog's preflight - the profiles and what each
// adds, the defaults, and the kubectl the terminal runs (whether it may
// launch, and any qualification). Nothing touches the cluster.
export type DebugProfileDTO = { name: string; adds: string };
export type DebugPreflightDTO = { profiles: DebugProfileDTO[]; defaultImage: string; defaultProfile: string; kubectl?: string; ok: boolean; reason?: string; qualified?: string; error?: string };

export async function debugPreflight(cluster: string): Promise<DebugPreflightDTO> {
  try {
    return (await ExecService.DebugPreflight(cluster)) as unknown as DebugPreflightDTO;
  } catch (e) {
    return { profiles: [], defaultImage: "busybox:1.36", defaultProfile: "general", ok: false, error: e instanceof Error ? e.message : "the preflight failed" };
  }
}

import { useFleet } from "../store/fleet";
import { ExecService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { Clipboard } from "@wailsio/runtime";

type ExecCommandDTO = { command: string; argv: string[]; error: string };
type ActionResultDTO = { ok: boolean; error: string };
export type DescribeResultDTO = { ok: boolean; output: string; error: string };

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
// clipboard. Returns "copied" | "error".
export async function copyExecCommand(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
): Promise<"copied" | "error"> {
  const dto = await getExecCommand(cluster, ns, pod, container);
  if (!dto || dto.error) return "error";
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

// describeResource runs `kubectl describe` for one object and returns the raw
// output. kindArg is "pod"/"node" for the core panels and plural.group
// (plural alone when group is "") for typed instances. Read-only; failures
// come back as a value ({ok:false, error}), never a throw - the pane renders
// them honestly.
export async function describeResource(
  cluster: string,
  kindArg: string,
  namespace: string,
  name: string,
): Promise<DescribeResultDTO> {
  try {
    const r = (await ExecService.Describe(cluster, kindArg, namespace, name)) as DescribeResultDTO;
    return r ?? { ok: false, output: "", error: "describe returned nothing" };
  } catch (e) {
    return { ok: false, output: "", error: e instanceof Error ? e.message : String(e) };
  }
}

// copyDebugCommand copies the kubectl debug command (ephemeral busybox shell
// targeted at the container) to the clipboard. Returns "copied" | "error".
export async function copyDebugCommand(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
): Promise<"copied" | "error"> {
  try {
    const dto = (await ExecService.GetDebugCommand(cluster, ns, pod, container)) as ExecCommandDTO;
    if (!dto || dto.error) return "error";
    await Clipboard.SetText(dto.command);
    return "copied";
  } catch {
    return "error";
  }
}

// openDebugTerminal opens the OS terminal running kubectl debug against the
// pod. Errors surface via the action toast, same as openExecTerminal.
export async function openDebugTerminal(
  cluster: string,
  ns: string,
  pod: string,
  container: string,
): Promise<void> {
  try {
    const r = (await ExecService.OpenDebugTerminal(cluster, ns, pod, container)) as ActionResultDTO;
    if (!r?.ok) {
      useFleet.getState().setActionStatus({ kind: "error", message: r?.error || "could not open debug terminal" });
    }
  } catch (e) {
    useFleet.getState().setActionStatus({ kind: "error", message: String(e) });
  }
}

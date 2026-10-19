import type { AMAlertDTO } from "../cluster/alertsModel";

/** withClasses: a test alert with the backend's classes and key filled from
 * its state and severity, as the backend derives them from the raw alert. */
export function withClasses(a: Omit<AMAlertDTO, "key" | "stateClass" | "severityClass"> & Partial<AMAlertDTO>): AMAlertDTO {
  const sev = (a.severity ?? "").toLowerCase();
  return {
    ...a,
    key: a.key ?? `k-${a.fingerprint}`,
    stateClass: a.stateClass ?? (a.state === "active" || a.state === "suppressed" || a.state === "unprocessed" ? a.state : "other"),
    severityClass: a.severityClass ?? (sev === "critical" || sev === "warning" ? sev : "other"),
  };
}

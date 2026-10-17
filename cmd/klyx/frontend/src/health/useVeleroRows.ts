import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { VeleroBackupDTO, VeleroLocationDTO, VeleroScheduleDTO } from "../bridge/velero";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Velero lens's rows (coverage S2a §3) from the Observer's shared rows:
// the velero task's schedules, locations and backups parts.

export const VELERO_KIND_NAMES = { schedules: "Schedule", backupstoragelocations: "BackupStorageLocation", backups: "Backup" };

export type VeleroPartRows<T> = {
  rows: T[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
};

function usePart<T>(cluster: string | null, part: string, kinds: string[]): VeleroPartRows<T> {
  const r = useRows<T>(cluster, "velero", part);
  return useMemo(() => {
    const coverage = partCoverage(r, kinds, VELERO_KIND_NAMES);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r) };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

export const useVeleroSchedules = (cluster: string | null) => usePart<VeleroScheduleDTO>(cluster, "schedules", ["Schedule", "Backup", "BackupStorageLocation"]);
export const useVeleroLocations = (cluster: string | null) => usePart<VeleroLocationDTO>(cluster, "locations", ["BackupStorageLocation", "Schedule", "Backup"]);
export const useVeleroBackups = (cluster: string | null) => usePart<VeleroBackupDTO>(cluster, "backups", ["Backup", "Schedule"]);

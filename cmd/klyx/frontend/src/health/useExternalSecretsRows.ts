import { useEffect } from "react";
import { useFleet, type ExternalSecretDTO, type ExternalSecretStoreDTO } from "../store/fleet";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The External Secrets lens's data from the Observer's shared rows (slice 3
// §5.2): the eso/externalsecrets and eso/stores parts fill the existing
// externalsecrets slice, whose selection stays the lens's own. Coverage as
// for certificates (health/lensRows): each part's evidence is its own.

const KIND_NAMES = { externalsecrets: "ExternalSecret", secretstores: "SecretStore", clustersecretstores: "ClusterSecretStore" };
const PART_KINDS = { secrets: ["ExternalSecret"], stores: ["SecretStore", "ClusterSecretStore"] };

const owner = sliceOwner(
  () => useFleet.getState().externalsecrets.cluster,
  (cluster) => useFleet.getState().setExternalSecretsLoading(cluster),
  () => useFleet.getState().clearExternalSecrets(),
);

export function useExternalSecretsRows(cluster: string | null): void {
  const secrets = useRows<ExternalSecretDTO>(cluster, "eso", "externalsecrets");
  const stores = useRows<ExternalSecretStoreDTO>(cluster, "eso", "stores");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.externalsecrets.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = [...partCoverage(secrets, PART_KINDS.secrets, KIND_NAMES), ...partCoverage(stores, PART_KINDS.stores, KIND_NAMES)];
    st.setExternalSecretsFromRows(cluster, {
      secrets: partRows(secrets),
      stores: partRows(stores),
      coverage,
      watchError: coverageError(coverage),
      loading: !answered(secrets) && !answered(stores),
      rowsState: rowsState([secrets, stores]),
      partCounts: { secrets: partCounts(secrets), stores: partCounts(stores) },
    });
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, secrets.rows, secrets.part, secrets.error, secrets.refreshError, secrets.evaluatedAt, secrets.counts.part, secrets.counts.updating, secrets.counts.refreshing,
    stores.rows, stores.part, stores.error, stores.refreshError, stores.evaluatedAt, stores.counts.part, stores.counts.updating, stores.counts.refreshing]);
}

// test seam
export function _resetExternalSecretsRowsForTest(): void {
  owner.resetForTest();
}

package fleet

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/workloads"
)

// PDBBlocker is a PodDisruptionBudget with zero allowed disruptions - the
// thing that stalls a drain during the real event the simulation models.
type PDBBlocker struct {
	Namespace          string
	Name               string
	DisruptionsAllowed int
}

// HeadroomReport is the survivability answer plus its caveats.
type HeadroomReport struct {
	Result workloads.HeadroomResult
	// PDBBlockers lists PDBs with disruptionsAllowed == 0 cluster-wide: even a
	// FITTING simulation stalls at drain time while these hold.
	PDBBlockers []PDBBlocker
	// PodListCapped: the 5000-row list cap hit - displaced counts may
	// under-count and the verdict is optimistic. Stated, never silent.
	PodListCapped bool
	// SchedAssumption says which scheduler model the survivors' reservations
	// assume (coverage S6 §2): an estimate, never the deployed scheduler.
	SchedAssumption string
}

// NodeHeadroom simulates losing a node, zone, or pool: requests-based
// bin-packing of the displaced pods onto the surviving schedulable nodes
// (workloads.SimulateLoss carries the semantics + caveats). Read-only.
func (c *ClusterConn) NodeHeadroom(ctx context.Context, loseKind, loseName string) (HeadroomReport, error) {
	if loseKind != "node" && loseKind != "zone" && loseKind != "pool" {
		return HeadroomReport{}, fmt.Errorf("unsupported loss target %q: must be node, zone, or pool", loseKind)
	}
	nodes, err := c.ListNodes(ctx)
	if err != nil {
		return HeadroomReport{}, fmt.Errorf("list nodes: %w", err)
	}
	simNodes := make([]workloads.SimNode, 0, len(nodes))
	for _, n := range nodes {
		simNodes = append(simNodes, workloads.SimNode{
			Name: n.Name, Pool: n.Pool, Zone: n.Zone,
			CPUAlloc: n.CPUAllocatable, MemAlloc: n.MemAllocatable,
			Schedulable: n.Ready && !n.Unschedulable,
		})
	}

	pods, capped, err := c.ListPods(ctx, "")
	if err != nil {
		return HeadroomReport{}, fmt.Errorf("list pods: %w", err)
	}
	simPods := make([]workloads.SimPod, 0, len(pods))
	for i := range pods {
		p := &pods[i]
		if p.Node == "" || p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue
		}
		// The scheduler-effective request: init containers, sidecars,
		// pod-level requests and overhead included - what a survivor must
		// actually have free to take the pod.
		simPods = append(simPods, workloads.SimPod{
			Namespace: p.Namespace, Name: p.Name, Node: p.Node,
			CPUReq: p.SchedCPU, MemReq: p.SchedMem, CPUNeed: p.DesiredCPU, MemNeed: p.DesiredMem,
			AltCPU: p.SchedAltCPU, AltMem: p.SchedAltMem,
			DaemonSet: p.OwnerKind == "DaemonSet",
		})
	}

	report := HeadroomReport{
		Result:          workloads.SimulateLoss(simNodes, simPods, workloads.LossTarget{Kind: loseKind, Name: loseName}),
		PodListCapped:   capped,
		SchedAssumption: c.schedModel().Assumption(),
	}

	if pdbs, perr := c.typed.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{}); perr == nil && pdbs != nil {
		for i := range pdbs.Items {
			pdb := &pdbs.Items[i]
			if pdb.Status.DisruptionsAllowed == 0 {
				report.PDBBlockers = append(report.PDBBlockers, PDBBlocker{
					Namespace: pdb.Namespace, Name: pdb.Name, DisruptionsAllowed: int(pdb.Status.DisruptionsAllowed),
				})
			}
		}
	}
	return report, nil
}

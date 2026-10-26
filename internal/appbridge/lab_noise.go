package appbridge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/moomora/klyx/internal/envread"
)

// LabNoiseEvent carries the §6a lab's delivery control: with the lab trace
// on, KLYX_LAB_NOISE_BYTES=n emits one uniquely valued event of about n
// bytes a second that nothing consumes, so a run can tell the webview's
// event-delivery cost from the rows' own (slice 3 addendum §6).
const LabNoiseEvent = "lab:noise"

func startLabNoise(em Emitter) {
	n, _ := strconv.Atoi(envread.Get(envread.KlyxLabNoiseBytes))
	if n <= 0 {
		return
	}
	go func() {
		for seq := 0; ; seq++ {
			time.Sleep(time.Second)
			var rows []PodRowDTO
			for i, size := 0, 0; size < n; i++ {
				uid := fmt.Sprintf("%08d-%04d-noise-%012d", seq, i, time.Now().UnixNano())
				rows = append(rows, PodRowDTO{Namespace: "klyx-load-00", Name: "noise-" + uid[:16], UID: uid, GVR: "v1/pods",
					OwnerKind: "ReplicaSet", OwnerName: "noise", Node: "", IP: fmt.Sprintf("10.9.%d.%d", seq%256, i%256), Restarts: seq,
					Health:     HealthDTO{Kind: "degraded", Label: "Pending", Reason: "PendingTooLong", Rule: "pod.pending-overdue", Native: "phase Pending"},
					Containers: []PodRowContainerDTO{{Name: "pause", State: "waiting:ContainerCreating"}}})
				b, _ := json.Marshal(rows[len(rows)-1])
				size += len(b) + 1
			}
			em.Emit(LabNoiseEvent, map[string]any{"seq": seq, "rows": rows})
		}
	}()
}

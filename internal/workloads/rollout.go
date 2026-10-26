package workloads

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
)

// RolloutRevision is one entry of a workload's rollout history. Images may be
// empty when a ControllerRevision's patch data does not carry the template
// (never fabricated); ChangeCause is the kubernetes.io/change-cause annotation
// when someone recorded one.
type RolloutRevision struct {
	Revision    int64
	AgeSeconds  int
	Images      []string
	Current     bool
	ChangeCause string
	// ObjectUID is the revision object's UID (a ReplicaSet or a
	// ControllerRevision) and TemplateDigest its pod template's canonical
	// identity: an undo writes only the revision it was shown (A3 §4.5).
	// TemplateDigest is empty when the template could not be read.
	ObjectUID      string
	TemplateDigest string
}

// TemplateImages lists a pod template's container images, init containers
// first - the "what actually changed" column of a rollout history row.
func TemplateImages(spec corev1.PodSpec) []string {
	out := make([]string, 0, len(spec.InitContainers)+len(spec.Containers))
	for _, c := range spec.InitContainers {
		out = append(out, c.Image)
	}
	for _, c := range spec.Containers {
		out = append(out, c.Image)
	}
	return out
}

// RevisionDataImages extracts container images from a ControllerRevision's
// raw patch data (StatefulSet/DaemonSet history). The data is a partial
// object holding spec.template; a shape that does not contain it yields nil -
// the row renders without images rather than guessing.
func RevisionDataImages(raw []byte) []string {
	var obj struct {
		Spec struct {
			Template struct {
				Spec struct {
					InitContainers []struct {
						Image string `json:"image"`
					} `json:"initContainers"`
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	var out []string
	for _, c := range obj.Spec.Template.Spec.InitContainers {
		if c.Image != "" {
			out = append(out, c.Image)
		}
	}
	for _, c := range obj.Spec.Template.Spec.Containers {
		if c.Image != "" {
			out = append(out, c.Image)
		}
	}
	return out
}

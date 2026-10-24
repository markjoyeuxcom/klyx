package workloads

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// templateHashLabel is the label a Deployment's controller adds to its
// ReplicaSets' templates (apps/v1 DefaultDeploymentUniqueLabelKey).
const templateHashLabel = "pod-template-hash"

// TemplateDigest is a pod template's canonical identity (A3 §2.2, §4.5): a
// SHA-256 over its JSON with the normalisation A1.1 compares templates by -
// without a ControllerRevision's $patch directive or a null creation time,
// and, for a Deployment and its ReplicaSets only (dropHash), without the
// controller's pod-template-hash label. tmpl is a typed template or a
// decoded map; both pass through one JSON round trip first, so a typed
// object and the same object read as unstructured digest alike. Fields a
// typed decode drops (newer than the client's types) make the digests
// differ, which refuses a write rather than allowing one.
func TemplateDigest(tmpl any, dropHash bool) (string, error) {
	raw, err := json.Marshal(tmpl)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	if m == nil {
		return "", errors.New("no pod template")
	}
	delete(m, "$patch")
	if md, ok := m["metadata"].(map[string]any); ok {
		if l, ok := md["labels"].(map[string]any); ok && dropHash {
			delete(l, templateHashLabel)
			if len(l) == 0 {
				delete(md, "labels")
			}
		}
		if v, ok := md["creationTimestamp"]; ok && v == nil {
			delete(md, "creationTimestamp")
		}
		if len(md) == 0 {
			delete(m, "metadata")
		}
	}
	canon, err := json.Marshal(m) // map keys marshal sorted: canonical
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// RevisionDataTemplate is a ControllerRevision's pod template: its data's
// spec.template, as the revision stores it.
func RevisionDataTemplate(raw []byte) (map[string]any, error) {
	var obj struct {
		Spec struct {
			Template map[string]any `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if obj.Spec.Template == nil {
		return nil, errors.New("the revision's data holds no pod template")
	}
	return obj.Spec.Template, nil
}

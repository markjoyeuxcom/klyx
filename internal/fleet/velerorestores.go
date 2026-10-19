package fleet

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/velero"
)

// VeleroRestoresCap bounds one read of the Restores (coverage S2b §3).
const VeleroRestoresCap = 500

// veleroRestoresTimeout bounds the read.
const veleroRestoresTimeout = 20 * time.Second

// VeleroRestores is one on-demand read of the Restores: never watched,
// nothing retained after the reply.
type VeleroRestores struct {
	Restores []*velero.Restore
	// Served is false when the Restores API is not served; Forbidden when
	// the list is refused.
	Served, Forbidden bool
	// More: the list went past the cap. Remaining is the server's
	// remainingItemCount when it supplied one: a continue token alone is no
	// count.
	More      bool
	Remaining *int64
	ReadAt    time.Time
}

var veleroRestoresGVR = schema.GroupVersionResource{Group: velero.Group, Version: velero.APIVersion, Resource: velero.ResourceRestores}

// ListVeleroRestores reads the cluster's Restores, capped, projected field
// by field (velero.ProjectRestore): Velero's text is classified, hooks and
// selectors counted, never shown.
func (c *ClusterConn) ListVeleroRestores(ctx context.Context) (VeleroRestores, error) {
	if c.dyn == nil {
		return VeleroRestores{}, errors.New("cluster not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, veleroRestoresTimeout)
	defer cancel()
	list, err := c.dyn.Resource(veleroRestoresGVR).List(ctx, metav1.ListOptions{Limit: VeleroRestoresCap})
	now := c.clk.Now()
	switch {
	case apierrors.IsNotFound(err):
		return VeleroRestores{ReadAt: now}, nil
	case apierrors.IsForbidden(err):
		return VeleroRestores{Served: true, Forbidden: true, ReadAt: now}, nil
	case err != nil:
		return VeleroRestores{}, err
	}
	out := VeleroRestores{Served: true, Restores: velero.ProjectRestores(list.Items), ReadAt: now}
	if list.GetContinue() != "" {
		out.More = true
		out.Remaining = list.GetRemainingItemCount()
	}
	return out, nil
}

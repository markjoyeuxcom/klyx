package dra

import (
	"bytes"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// MaxObject bounds one retained projection of a resource (S11b §2.0, §7);
// a larger one is never retained, and the kind reads capped. Each is set
// above the worst object the API admits, projected (TestWorstObjectsFit
// MaxObject), so no valid object is left out for its size alone.
func MaxObject(resource string) int {
	switch resource {
	case "resourceslices":
		return maxSliceObject
	case "resourceclaims":
		return maxClaimObject
	}
	return maxClassObject
}

const (
	// measured: 323,351 B (64 devices, each with 16 taints and 2 x 32
	// counters, the 1.35+ shape) and 179,602 B (1.34's 32 x 32 counters)
	maxSliceObject = 384 << 10
	// measured: 325,227 B (32 requests of 8 subrequests, 32 results and
	// device statuses of 8 conditions, every message 256 escaped bytes)
	maxClaimObject = 384 << 10
	// measured: 2,798 B
	maxClassObject = 16 << 10
	// unsized is past every kind's bound
	unsized = maxSliceObject + 1
)

// Sized wraps a projection with its encoded size, for the kind's budget.
type Sized[T any] struct {
	V    T
	Size int
}

// sized measures the projection as JSON without HTML escaping: the only
// free text it keeps (a condition's message) is made printable first, so
// escaping grows a byte to at most two.
func sized[T any](v T) *Sized[T] {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	n := unsized
	if enc.Encode(v) == nil {
		n = b.Len()
	}
	return &Sized[T]{V: v, Size: n}
}

// Project returns the projection for one resource: resourceslices,
// resourceclaims or deviceclasses.
func Project(resource string) func(any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("dra: want *Unstructured, got %T", obj)
		}
		switch resource {
		case "resourceslices":
			return sized(ProjectSlice(u)), nil
		case "resourceclaims":
			return sized(ProjectClaim(u)), nil
		case "deviceclasses":
			return sized(ProjectClass(u)), nil
		}
		return nil, fmt.Errorf("dra: unknown resource %q", resource)
	}
}

// SizeOf is a projection's encoded size.
func SizeOf(v any) int {
	switch o := v.(type) {
	case *Sized[Slice]:
		return o.Size
	case *Sized[Claim]:
		return o.Size
	case *Sized[Class]:
		return o.Size
	}
	return unsized
}

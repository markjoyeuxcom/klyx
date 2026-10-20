package dra

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// MaxObject bounds one retained projection (S11b §2.0); a larger one is
// never retained, and the kind reads capped.
const MaxObject = 64 << 10

// Sized wraps a projection with its encoded size, for the kind's budget.
type Sized[T any] struct {
	V    T
	Size int
}

func sized[T any](v T) *Sized[T] {
	b, err := json.Marshal(v)
	n := len(b)
	if err != nil {
		n = MaxObject + 1
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
	return MaxObject + 1
}

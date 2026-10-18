package gwapi

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// GatewayClass is one GatewayClass as read (S7 §8): the controller that
// implements it, its Accepted condition and the features it declares.
type GatewayClass struct {
	Name, ControllerName string
	Generation           int64
	AcceptedCond         Cond
	// SupportedFeatures are status.supportedFeatures' names (S7 §1);
	// FeaturesReported is whether the class lists any.
	SupportedFeatures []string
	FeaturesReported  bool
}

// ParseGatewayClass maps a GatewayClass. Features are read in either shape:
// {name: X} (Gateway API 1.2 and later) or a bare string.
func ParseGatewayClass(u *unstructured.Unstructured) GatewayClass {
	gc := GatewayClass{Name: u.GetName(), Generation: u.GetGeneration(), AcceptedCond: condOf(statusConds(u.Object), "Accepted")}
	gc.ControllerName, _, _ = unstructured.NestedString(u.Object, "spec", "controllerName")
	fs, _, _ := unstructured.NestedSlice(u.Object, "status", "supportedFeatures")
	for _, f := range fs {
		switch x := f.(type) {
		case string:
			gc.SupportedFeatures = append(gc.SupportedFeatures, x)
		case map[string]any:
			if n, ok := x["name"].(string); ok {
				gc.SupportedFeatures = append(gc.SupportedFeatures, n)
			}
		}
	}
	gc.FeaturesReported = len(gc.SupportedFeatures) > 0
	return gc
}

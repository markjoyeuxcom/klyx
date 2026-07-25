package appbridge

import "github.com/moomora/klyx/internal/fleet"

// Compile-time assertions: fleet.Conn must satisfy every per-service conn
// interface cmd/klyx/main.go adapts it to. main.go itself only compiles where
// the Wails GTK headers exist (CI, a real desktop), so without these lines a
// method added to a service interface but not to fleet.Conn surfaces only in
// CI - with them, a plain `go test ./internal/...` catches it anywhere.
var (
	_ AIConn              = fleet.Conn(nil)
	_ ArgoConn            = fleet.Conn(nil)
	_ CertManagerConn     = fleet.Conn(nil)
	_ ConnectivityConn    = fleet.Conn(nil)
	_ CRDConn             = fleet.Conn(nil)
	_ EventsConn          = fleet.Conn(nil)
	_ ExecConn            = fleet.Conn(nil)
	_ ExpiryConn          = fleet.Conn(nil)
	_ ExternalSecretsConn = fleet.Conn(nil)
	_ ForwardsConn        = fleet.Conn(nil)
	_ GatewayConn         = fleet.Conn(nil)
	_ GitOpsConn          = fleet.Conn(nil)
	_ HelmConn            = fleet.Conn(nil)
	_ KarpenterConn       = fleet.Conn(nil)
	_ LogsConn            = fleet.Conn(nil)
	_ MetricsConn         = fleet.Conn(nil)
	_ NodeOpsConn         = fleet.Conn(nil)
	_ NodesConn           = fleet.Conn(nil)
	_ PodsConn            = fleet.Conn(nil)
	_ PromotionConn       = fleet.Conn(nil)
	_ RBACConn            = fleet.Conn(nil)
	_ UpgradeConn         = fleet.Conn(nil)
	_ WorkloadsConn       = fleet.Conn(nil)
)

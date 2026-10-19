package capability

// AlertingCapability is whether the cluster has an Alertmanager to read
// alerts from (coverage S8a §4). It is not probed: it is the alerts
// snapshot's own evidence - an Alertmanager configured, or the last read
// that resolved one (configured, discovered, or found but ambiguous) - so
// the Alerts lens's gate moves on the snapshot's cadence, and it is absent
// only after a read that completed discovery and found none.
type AlertingCapability struct {
	Present bool
}

package capability

// LogHistoryCapability is whether the cluster has a Loki to read pod log
// history from (coverage S8b §2): a configured loki endpoint. It is not
// probed - S8b has no discovery - so it is the configuration's evidence.
type LogHistoryCapability struct {
	Present bool
}

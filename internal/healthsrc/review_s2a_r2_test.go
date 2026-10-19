package healthsrc

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/velero"
)

func TestReviewS2aR2EmptyLiteralTZIsUTC(t *testing.T) {
	servers := VeleroServers(capability.VeleroCapability{ServersRead: true, Servers: []capability.VeleroServer{
		{Namespace: "velero", Name: "velero", Ready: true, RolledOut: true, Image: "velero/velero:v1.18.4", TZSet: true, TZ: ""},
	}})
	now := time.Date(2026, 10, 18, 3, 30, 0, 0, time.UTC)
	s := &velero.Schedule{Meta: velero.Meta{Namespace: "velero", Name: "hourly", Created: now.Add(-24 * time.Hour)}, Phase: "Enabled", Cron: "0 * * * *", LastBackup: now.Add(-2 * time.Hour)}
	v := velero.ScheduleRule(velero.ScheduleInput{S: s, Servers: servers, History: velero.History{Readable: true, Complete: true}, Now: now})
	if v.Eval.Role != health.Fault || v.Eval.Label != "Missed" {
		t.Fatalf("an explicitly empty TZ is UTC in Go; overdue schedule must remain classifiable: facts=%+v verdict=%+v", servers.ByNamespace["velero"], v.Eval)
	}
}

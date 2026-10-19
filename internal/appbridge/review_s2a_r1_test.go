package appbridge

import (
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/velero"
	"strings"
	"testing"
	"time"
)

func TestReviewS2aR1UnknownZoneCannotEchoUserInfo(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	s := &velero.Schedule{Meta: velero.Meta{Namespace: "velero", Name: "daily", UID: "uid-s", Created: now.Add(-time.Hour)}, Phase: "Enabled", Cron: "0 * * * *"}
	servers := velero.Servers{Read: true, ByNamespace: map[string]velero.ServerFacts{"velero": {Server: velero.Server{Version: velero.Version{Major: 1, Minor: 18, Patch: 4}, VersionResolved: true, Zone: "https://user:S2aR1ZoneNeedle@zone/path", ZoneResolved: true}}}}
	v := velero.ScheduleRule(velero.ScheduleInput{S: s, Servers: servers, History: velero.History{Readable: true, Complete: true}, Now: now})
	dto := veleroScheduleDTO(healthsrc.Row[healthsrc.VeleroScheduleRow]{Eval: v.Eval, Data: healthsrc.VeleroScheduleRow{Schedule: s, Verdict: v}})
	raw := rawJSONOf(t, dto)
	if strings.Contains(raw, "S2aR1ZoneNeedle") {
		t.Fatalf("invalid literal TZ leaked URL user info through schedule DTO: %s", raw)
	}
}

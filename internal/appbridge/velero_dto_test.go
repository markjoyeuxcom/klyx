package appbridge

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
)

const s2aNeedle = "S2aLABneedle"

func s2aObj(kind, name string, created time.Time, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	md := map[string]any{"name": name, "namespace": "velero", "uid": "uid-" + name, "creationTimestamp": created.UTC().Format(time.RFC3339)}
	if labels != nil {
		md["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": kind, "metadata": md, "spec": spec, "status": status}}
}

func evaluateVelero(t *testing.T, part string, objs map[string][]*unstructured.Unstructured) any {
	t.Helper()
	kinds, domains := healthsrc.Velero(healthsrc.NativeDeps{Cluster: "c"})
	capture := observe.Capture{Now: time.Now(), Rows: true, RoleCap: 10, Objects: map[string][]any{}, Complete: map[string]bool{},
		Facts: map[string]any{healthsrc.FactVelero: velero.Servers{Read: true, ByNamespace: map[string]velero.ServerFacts{
			"velero": {Ready: true, Server: velero.Server{Version: velero.Version{Major: 1, Minor: 18, Patch: 4}, VersionResolved: true, Zone: "UTC", ZoneResolved: true}}}}}}
	for _, k := range kinds {
		capture.Complete[k.Name] = true
		for _, u := range objs[k.Name] {
			v, err := k.Project(u)
			if err != nil {
				t.Fatal(err)
			}
			capture.Objects[k.Name] = append(capture.Objects[k.Name], v)
		}
	}
	for _, p := range domains[0].Parts {
		if p.ID == part {
			out, err := rowsConverters[healthsrc.TaskVelero+"/"+part](p.Evaluate(capture).Rows, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("no part %s", part)
	return nil
}

// Coverage S2a §6: the Velero rows cross with Klyx's classes only, and the
// AI evidence admits no config value, Secret name, hook or namespace list.
func TestVeleroRowsCrossUnderTheRules(t *testing.T) {
	now := time.Now()
	lb := now.Add(-time.Hour).Truncate(time.Second)
	objs := map[string][]*unstructured.Unstructured{
		healthsrc.KindVeleroSchedules: {s2aObj("Schedule", "daily", now.Add(-48*time.Hour), nil,
			map[string]any{"schedule": "@every 2h", "template": map[string]any{"hooks": map[string]any{"resources": []any{
				map[string]any{"name": "db", "pre": []any{map[string]any{"exec": map[string]any{"command": []any{"pg_dump", s2aNeedle}}}}}}}}},
			map[string]any{"phase": "Enabled", "lastBackup": lb.UTC().Format(time.RFC3339)})},
		healthsrc.KindVeleroLocations: {s2aObj("BackupStorageLocation", "default", now.Add(-48*time.Hour), nil,
			map[string]any{"provider": "aws", "default": true, "credential": map[string]any{"name": "cloud-credentials", "key": "cloud"},
				"objectStorage": map[string]any{"bucket": "b", "caCert": "LS0t" + s2aNeedle},
				"config":        map[string]any{"region": "eu-west-1", "s3Url": "https://u:" + s2aNeedle + "@minio:9000/x", "kmsKeyId": s2aNeedle}},
			map[string]any{"phase": "Unavailable", "message": `BackupStorageLocation "default" is unavailable: invalid configuration value: ` + s2aNeedle + " AccessDenied"})},
		healthsrc.KindVeleroBackups: {
			s2aObj("Backup", "daily-"+lb.UTC().Format("20060102150405"), lb, map[string]any{velero.LabelSchedule: "daily"}, map[string]any{"storageLocation": "default"},
				map[string]any{"phase": "Failed", "startTimestamp": lb.Add(time.Second).UTC().Format(time.RFC3339), "failureReason": "backup execution failed: Authorization: Bearer " + s2aNeedle}),
			s2aObj("Backup", "manual", now.Add(-2*time.Hour), nil, map[string]any{"storageLocation": "default"},
				map[string]any{"phase": "FailedValidation", "validationErrors": []any{"backup can't be created because BackupStorageLocation " + s2aNeedle + " is in Unavailable status."}}),
		},
	}
	sr := evaluateVelero(t, "schedules", objs).([]VeleroScheduleDTO)
	if b := rawJSONOf(t, sr); strings.Contains(b, s2aNeedle) || len(sr) != 1 || sr[0].Rule.Label != "Last run failed" || sr[0].Template.Hooks != 1 {
		t.Fatalf("schedule rows: %s", b)
	}
	if sr[0].Deciding == nil || !strings.Contains(sr[0].Deciding.Class, "the backup failed while running") || sr[0].Location.Label != "Unavailable" {
		t.Fatalf("deciding run and location block: %+v %+v", sr[0].Deciding, sr[0].Location)
	}
	lr := evaluateVelero(t, "locations", objs).([]VeleroLocationDTO)
	if b := rawJSONOf(t, lr); strings.Contains(b, s2aNeedle) || len(lr) != 1 || !lr[0].MessageWithheld || !strings.Contains(lr[0].Message, "AccessDenied") ||
		lr[0].S3URL != "https://minio:9000" || len(lr[0].Schedules) != 1 {
		t.Fatalf("location rows: %s", b)
	}
	br := evaluateVelero(t, "backups", objs).([]VeleroBackupDTO)
	if b := rawJSONOf(t, br); strings.Contains(b, s2aNeedle) || len(br) != 1 || br[0].Name != "manual" || br[0].Validation[0] != "the storage location is Unavailable" {
		t.Fatalf("backup rows: %s", b)
	}
	for _, part := range []string{"schedules", "locations", "backups"} {
		a := rawJSONOf(t, aiEvidenceFields[healthsrc.TaskVelero+"/"+part])
		for _, k := range []string{`"credential"`, `"credentialKey"`, `"caCertRef"`, `"configKeys"`, `"region"`, `"s3Url"`, `"publicUrl"`, `"includedNamespaces"`, `"excludedNamespaces"`} {
			if strings.Contains(a, k) {
				t.Errorf("%s evidence admits %s", part, k)
			}
		}
	}
}

package crd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const veleroNeedle = "S2aLABneedle"

func veleroDoc(kind string, spec, status map[string]any) map[string]any {
	return map[string]any{"apiVersion": "velero.io/v1", "kind": kind, "metadata": map[string]any{"name": "x", "namespace": "velero"},
		"spec": spec, "status": status}
}

// Coverage S2a §6: Velero's hooks, provider config and free text never
// reach the generic paths; each message is its class's fixed placeholder.
func TestSanitizeVelero(t *testing.T) {
	sas := "https://acct.blob.core.windows.net/c?sv=2021&sig=" + veleroNeedle
	docs := []map[string]any{
		veleroDoc("Schedule", map[string]any{"schedule": "0 2 * * *", "template": map[string]any{
			"hooks": map[string]any{"resources": []any{map[string]any{"pre": []any{map[string]any{"exec": map[string]any{"command": []any{"sh", "-c", "pg_dump -p" + veleroNeedle}}}}}}}}},
			map[string]any{"phase": "FailedValidation", "validationErrors": []any{"invalid schedule: " + veleroNeedle}}),
		veleroDoc("Backup", map[string]any{"hooks": map[string]any{"resources": []any{map[string]any{"name": veleroNeedle}}}},
			map[string]any{"phase": "Failed", "failureReason": "backup execution failed: Authorization: Bearer " + veleroNeedle,
				"validationErrors": []any{"backup can't be created because BackupStorageLocation " + veleroNeedle + " is in Unavailable status.", veleroNeedle + " then " + sas}}),
		veleroDoc("Restore", map[string]any{"hooks": map[string]any{"resources": []any{map[string]any{"postHooks": []any{map[string]any{"init": map[string]any{
			"initContainers": []any{map[string]any{"name": "c", "env": []any{map[string]any{"name": "PASS", "value": veleroNeedle}}}}}}}}}}},
			map[string]any{"phase": "Failed", "failureReason": "restore execution failed: invalid configuration value: " + veleroNeedle}),
		veleroDoc("BackupStorageLocation", map[string]any{"provider": "aws", "config": map[string]any{"s3Url": "https://u:" + veleroNeedle + "@minio:9000", "region": "eu"},
			"objectStorage": map[string]any{"bucket": "b", "caCert": "LS0t" + veleroNeedle}},
			map[string]any{"phase": "Unavailable", "message": `BackupStorageLocation "x" is unavailable: unable to locate ObjectStore plugin named ` + veleroNeedle}),
		veleroDoc("VolumeSnapshotLocation", map[string]any{"provider": "aws", "config": map[string]any{"profile": veleroNeedle}}, nil),
	}
	for _, d := range docs {
		kind := d["kind"].(string)
		s := SanitizeEmbedded("velero.io", kind, d)
		b, _ := json.Marshal(s.Object)
		if strings.Contains(string(b), veleroNeedle) || strings.Contains(string(b), "sig=") {
			t.Errorf("%s: a needle crossed: %s", kind, b)
		}
		if !s.Payload || s.EditEligible || s.DescribeMode != DescribeWithheld {
			t.Errorf("%s: payload %v edit %v describe %s", kind, s.Payload, s.EditEligible, s.DescribeMode)
		}
		// a second run leaves every placeholder as it is
		again := SanitizeEmbedded("velero.io", kind, s.Object)
		if !reflect.DeepEqual(again.Object, s.Object) {
			t.Errorf("%s: not idempotent:\n%v\n%v", kind, again.Object, s.Object)
		}
	}
	bsl := SanitizeEmbedded("velero.io", "BackupStorageLocation", docs[3])
	if got := bsl.Object["status"].(map[string]any)["message"]; got != "<velero: the provider's plugin is not installed>" {
		t.Fatalf("message placeholder: %v", got)
	}
}

// A payload-free storage location whose only text is its message is still
// held from Describe (S2a §6).
func TestSanitizeVeleroMessageOnlyHoldsDescribe(t *testing.T) {
	d := veleroDoc("BackupStorageLocation", map[string]any{"provider": "aws", "objectStorage": map[string]any{"bucket": "b"}},
		map[string]any{"phase": "Unavailable", "message": `BackupStorageLocation "x" is unavailable: AccessDenied`})
	if s := SanitizeEmbedded("velero.io", "BackupStorageLocation", d); s.DescribeMode != DescribeWithheld {
		t.Fatalf("%+v", s)
	}
	clean := veleroDoc("BackupStorageLocation", map[string]any{"provider": "aws", "objectStorage": map[string]any{"bucket": "b"}},
		map[string]any{"phase": "Available"})
	if s := SanitizeEmbedded("velero.io", "BackupStorageLocation", clean); s.DescribeMode == DescribeWithheld {
		t.Fatalf("no message, no payload: %+v", s)
	}
	// a raw message that equals a placeholder carries only Klyx's own words
	forged := veleroDoc("BackupStorageLocation", nil, map[string]any{"message": "<velero: the provider's plugin is not installed>"})
	if s := SanitizeEmbedded("velero.io", "BackupStorageLocation", forged); s.Object["status"].(map[string]any)["message"] != "<velero: the provider's plugin is not installed>" {
		t.Fatalf("%+v", s.Object)
	}
}

// An Event about a Velero object is withheld whole; another group's kind
// of the same name keeps its own rule (S2a §6).
func TestEventAboutVelero(t *testing.T) {
	for _, s := range []EventSubject{SubjectOf("Backup", "velero.io/v1"), SubjectOf("Schedule", ""), SubjectOf("DataUpload", "velero.io/v2alpha1")} {
		if out, ok := EventMessage("Failed", "AccessDenied "+veleroNeedle, []EventSubject{s}); !ok || out != VeleroEventWithheld {
			t.Errorf("%+v: %q", s, out)
		}
	}
	if out, ok := EventMessage("Completed", "backup done", []EventSubject{SubjectOf("Backup", "postgresql.cnpg.io/v1")}); ok || out != "backup done" {
		t.Fatalf("another group's Backup: %q", out)
	}
	ev := map[string]any{"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "e"}, "reason": "Failed", "message": veleroNeedle,
		"involvedObject": map[string]any{"kind": "BackupStorageLocation", "apiVersion": "velero.io/v1", "name": "x"}}
	s := SanitizeEmbedded("", "Event", ev)
	if s.Object["message"] != VeleroEventWithheld {
		t.Fatalf("event: %v", s.Object["message"])
	}
}

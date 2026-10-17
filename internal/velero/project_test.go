package velero

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func obj(kind, name string, spec, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": "velero", "uid": "11111111-2222-3333-4444-555555555555"}}}
	if spec != nil {
		u.Object["spec"] = spec
	}
	if status != nil {
		u.Object["status"] = status
	}
	u.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)))
	return u
}

func TestProjectSchedule(t *testing.T) {
	u := obj(KindSchedule, "daily", map[string]any{"schedule": "CRON_TZ=Europe/Amsterdam 0 2 * * *", "paused": false,
		"useOwnerReferencesInBackup": true,
		"template": map[string]any{"storageLocation": "default", "ttl": "720h0m0s", "snapshotVolumes": true,
			"includedNamespaces": []any{"apps", "*"},
			"hooks":              map[string]any{"resources": []any{map[string]any{"name": "db", "pre": []any{map[string]any{"exec": map[string]any{"command": []any{"sh", "-c", "pg_dump -p " + needle}}}}}}}}},
		map[string]any{"phase": "Enabled", "lastBackup": "2026-10-17T02:00:05Z", "validationErrors": []any{"invalid schedule: " + needle}})
	s := ProjectSchedule(u)
	if s.Cron != "CRON_TZ=Europe/Amsterdam 0 2 * * *" || s.Phase != "Enabled" || !s.UseOwnerReferences || s.Paused {
		t.Fatalf("%+v", s)
	}
	if !s.LastBackup.Equal(time.Date(2026, 10, 17, 2, 0, 5, 0, time.UTC)) || s.Template.StorageLocation != "default" || s.Template.Hooks != 1 {
		t.Fatalf("%+v", s.Template)
	}
	if len(s.Validation) != 1 || s.Validation[0].Code != "invalid-cron" {
		t.Fatalf("%+v", s.Validation)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), needle) || strings.Contains(string(b), "pg_dump") {
		t.Fatalf("a hook or Velero's text crossed: %s", b)
	}
	if s.Size != len(b) || s.Size > MaxObject {
		t.Fatalf("size %d vs %d", s.Size, len(b))
	}
}

func TestProjectLocation(t *testing.T) {
	u := obj(KindLocation, "default", map[string]any{"provider": "velero.io/aws", "default": true, "accessMode": "ReadWrite",
		"validationFrequency": "1m0s",
		"objectStorage":       map[string]any{"bucket": "backups", "prefix": "prd", "caCert": "LS0tLS1CRUdJTg=="},
		"credential":          map[string]any{"name": "cloud-credentials", "key": "cloud"},
		"config":              map[string]any{"region": "eu-west-1", "s3Url": "https://user:" + needle + "@minio.example:9000/path?x=" + needle, "kmsKeyId": needle}},
		map[string]any{"phase": "Unavailable", "lastValidationTime": "2026-10-17T02:01:00Z",
			"message": `BackupStorageLocation "default" is unavailable: AccessDenied ` + needle})
	l := ProjectLocation(u)
	if l.Provider != "velero.io/aws" || !l.Default || l.ValidationFrequency != time.Minute || !l.ValidationSet || !l.CACertSet {
		t.Fatalf("%+v", l)
	}
	if l.Region != "eu-west-1" || l.S3URL != "https://minio.example:9000" || strings.Join(l.ConfigKeys, ",") != "kmsKeyId,region,s3Url" {
		t.Fatalf("config: %+v", l)
	}
	if l.Message == nil || !l.Message.Withheld || len(l.Message.Vocabulary) != 1 {
		t.Fatalf("message: %+v", l.Message)
	}
	b, _ := json.Marshal(l)
	if strings.Contains(string(b), needle) || strings.Contains(string(b), "LS0t") {
		t.Fatalf("a secret crossed: %s", b)
	}
}

func TestProjectBackupValidatesJoinInputs(t *testing.T) {
	u := obj(KindBackup, "daily-20261017020005", map[string]any{"storageLocation": strings.Repeat("x", 5000),
		"volumeSnapshotLocations": []any{"a", "b", "c"}},
		map[string]any{"phase": "Failed", "startTimestamp": "2026-10-17T02:00:06Z", "errors": int64(3),
			"failureReason": "backup execution failed: " + needle})
	u.SetLabels(map[string]string{LabelSchedule: "daily", LabelGCFailure: "weird"})
	u.SetOwnerReferences([]metav1.OwnerReference{
		{APIVersion: "velero.io/v1", Kind: KindSchedule, Name: "daily", UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{APIVersion: "velero.io/v1", Kind: KindSchedule, Name: strings.Repeat("n", 300), UID: types.UID("not-a-uuid")},
		{APIVersion: "v1", Kind: "ConfigMap", Name: "x", UID: "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"},
	})
	b := ProjectBackup(u)
	if !b.StorageLocationInvalid || b.StorageLocation != "" {
		t.Fatalf("a 5,000-byte location: %+v", b)
	}
	if len(b.Owners) != 1 || !b.OwnersIncomplete {
		t.Fatalf("owners: %+v %v", b.Owners, b.OwnersIncomplete)
	}
	if b.GCFailure != "other" || b.Errors != 3 || len(b.SnapshotLocations) != maxSnapshotLoc || b.SnapshotLocationCount != 3 {
		t.Fatalf("%+v", b)
	}
	if b.Failure == nil || b.Failure.Code != "execution-failed" {
		t.Fatalf("%+v", b.Failure)
	}
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), needle) || b.Size > MaxObject {
		t.Fatalf("size %d: %s", b.Size, raw)
	}
}

// The worst case a Backup's bounded inputs allow stays under 3 KiB (S2a
// §3, revision 2).
func TestBackupWorstCaseSize(t *testing.T) {
	long := strings.Repeat("a", 253)
	val := []any{}
	for i := 0; i < 10; i++ {
		val = append(val, "backup execution failed: AccessDenied InvalidToken ExpiredToken NoSuchBucket no such host StatusCode: 403")
	}
	u := obj(KindBackup, long, map[string]any{"storageLocation": long, "volumeSnapshotLocations": []any{long, long, long}},
		map[string]any{"phase": "WaitingForPluginOperationsPartiallyFailed", "startTimestamp": "2026-10-17T02:00:06Z",
			"completionTimestamp": "2026-10-17T03:00:06Z", "expiration": "2026-11-16T02:00:06Z",
			"errors": int64(1 << 40), "warnings": int64(1 << 40), "failureReason": "backup execution failed: AccessDenied InvalidToken ExpiredToken NoSuchBucket",
			"validationErrors": val, "progress": map[string]any{"totalItems": int64(1 << 40), "itemsBackedUp": int64(1 << 40)}})
	u.SetNamespace(strings.Repeat("n", 63))
	u.SetLabels(map[string]string{LabelSchedule: strings.Repeat("s", 63), LabelGCFailure: "BSLCannotGet"})
	u.SetOwnerReferences([]metav1.OwnerReference{
		{Kind: KindSchedule, Name: long, UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{Kind: KindSchedule, Name: long, UID: "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"},
	})
	if b := ProjectBackup(u); b.Size >= 3<<10 {
		t.Fatalf("worst case %d bytes", b.Size)
	}
}

func TestRankAndWindow(t *testing.T) {
	older := &Backup{Meta: Meta{Created: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, Phase: "Completed", Start: time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)}
	newer := &Backup{Meta: Meta{Created: time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)}, Phase: "Completed", Start: time.Date(2026, 10, 17, 2, 0, 1, 0, time.UTC)}
	running := &Backup{Meta: Meta{Created: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, Phase: "InProgress"}
	ro, rn, rr := Rank("velero/a", older), Rank("velero/b", newer), Rank("velero/c", running)
	// tier 0 first; then the later run time first, whatever the creation
	if !(rr < ro && ro < rn) {
		t.Fatalf("order: running %s, older-created later-started %s, newer %s", rr, ro, rn)
	}
	w, ok := WindowOf(ro)
	if !ok || w.Tier0 || !w.W.Equal(older.Start) {
		t.Fatalf("window: %+v %v", w, ok)
	}
	if w, ok := WindowOf(rr); !ok || !w.Tier0 {
		t.Fatalf("tier 0 window: %+v", w)
	}
	if _, ok := WindowOf("velero/a"); ok {
		t.Fatal("a key is not a window")
	}
}

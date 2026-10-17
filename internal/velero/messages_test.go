package velero

import (
	"encoding/json"
	"strings"
	"testing"
)

const needle = "S2aLABneedle"

const bslPrefix = `BackupStorageLocation "default" is unavailable: `

// Every pinned form classifies to its fixed class (S2a §6).
func TestClassifyPinnedForms(t *testing.T) {
	cases := []struct{ field, msg, code string }{
		{FieldLocationMessage, bslPrefix + "backup storage location does not use object storage", "no-object-storage"},
		{FieldLocationMessage, bslPrefix + "object storage provider name must not be empty", "no-provider"},
		{FieldLocationMessage, bslPrefix + `backup storage location's bucket name "a/b" must not contain a '/' (if using a prefix, put it in the 'Prefix' field instead)`, "bucket-slash"},
		{FieldLocationMessage, bslPrefix + "cannot specify both caCert and caCertRef in objectStorage", "ca-both"},
		{FieldLocationMessage, bslPrefix + "error getting CA certificate from secret: secrets \"ca\" not found", "ca-secret"},
		{FieldLocationMessage, bslPrefix + "unable to get credentials: unable to get key for secret: x", "credentials"},
		{FieldLocationMessage, bslPrefix + "Backup store contains 4 invalid top-level directories: [a b c ...]", "foreign-dirs"},
		{FieldLocationMessage, bslPrefix + "Backup store contains invalid top-level directories: [a]", "foreign-dirs"},
		{FieldLocationMessage, bslPrefix + "unable to locate ObjectStore plugin named velero.io/aws", "plugin-missing"},
		{FieldBackupValidation, "backup can't be created because backup storage location dr is currently in read-only mode", "location-read-only"},
		{FieldBackupValidation, "backup can't be created because BackupStorageLocation default is in Unavailable status.", "location-unavailable"},
		{FieldBackupValidation, "an existing backup storage location was not specified at backup creation time and the server default default does not exist. Please address this issue", "location-default-missing"},
		{FieldBackupValidation, "an existing backup storage location was not specified at backup creation time and the default x was not found.", "location-default-missing"},
		{FieldBackupValidation, "error getting backup storage location: boom", "location-unreadable"},
		{FieldBackupValidation, "a VolumeSnapshotLocation CRD for the location aws with the name specified in the backup spec needs to be created", "vsl-missing"},
		{FieldBackupValidation, "error getting volume snapshot location named x: boom", "vsl-unreadable"},
		{FieldBackupValidation, "more than one VolumeSnapshotLocation name specified for provider aws: a; unexpected name was b", "vsl-duplicate"},
		{FieldBackupValidation, "invalid resource policies: bad yaml", "resource-policy"},
		{FieldBackupValidation, "include-resources, exclude-resources and include-cluster-resources are old filter parameters.\nPlease use new ones", "old-filters"},
		{FieldBackupFailure, `found a backup with status "InProgress" during the server starting, mark it as "Failed"`, "server-restart"},
		{FieldBackupFailure, "backup execution failed: backup already exists in object storage", "execution-failed"},
		{FieldScheduleValidation, "Schedule must be a non-empty valid Cron expression", "empty-cron"},
		{FieldScheduleValidation, "invalid schedule: expected exactly 5 fields, found 4: [0 2 * *]", "invalid-cron"},
		{FieldRestoreFailure, `found a restore with status "InProgress" during the server starting, mark it as "Failed"`, "server-restart"},
	}
	for _, c := range cases {
		got, ok := Classify(c.field, c.msg)
		if !ok || got.Code != c.code || got.Withheld {
			t.Errorf("%s %q: %+v", c.field, c.msg, got)
		}
	}
	if _, ok := Classify(FieldLocationMessage, ""); ok {
		t.Fatal("an empty message has no class")
	}
}

// A near-miss of a whole-message form is withheld.
func TestClassifyNearMissWithheld(t *testing.T) {
	for _, c := range []struct{ field, msg string }{
		{FieldLocationMessage, bslPrefix + "object storage provider name must not be empty, and more"},
		{FieldLocationMessage, "object storage provider name must not be empty"}, // no prefix
		{FieldBackupValidation, "backup can't be created because BackupStorageLocation default is in Unavailable status. Also " + needle},
		{FieldBackupFailure, `found a backup with status "InProgress" during the server starting, mark it as "Failed" ` + needle},
	} {
		got, _ := Classify(c.field, c.msg)
		if !got.Withheld || got.Text != WithheldText {
			t.Errorf("%q: %+v", c.msg, got)
		}
	}
}

// Nothing from the text crosses but a fixed class and the fixed
// vocabulary: forged whole forms, URL-free secrets, bearer headers and
// secrets beside a token (S2a §6, design revisions 1 and 2).
func TestClassifyNeverDisclosesText(t *testing.T) {
	sas := "https://acct.blob.core.windows.net/c?sv=2021&sig=" + needle + "&se=2026"
	cases := []struct{ field, msg string }{
		{FieldLocationMessage, bslPrefix + "unable to locate ObjectStore plugin named " + needle},
		{FieldLocationMessage, bslPrefix + "rpc error: GET " + sas + ": 403"},
		{FieldLocationMessage, bslPrefix + "invalid configuration value: " + needle},
		{FieldLocationMessage, bslPrefix + "Authorization: Bearer " + needle},
		{FieldLocationMessage, bslPrefix + needle + " then https://s3.example.com/bucket?X-Amz-Signature=abc"},
		{FieldLocationMessage, bslPrefix + "operation error S3: ListObjectsV2, AccessDenied " + needle + " StatusCode: 403"},
		{FieldLocationMessage, bslPrefix + "unable to get credentials: token " + needle + " expired, ExpiredToken"},
		{FieldBackupValidation, "backup can't be created because BackupStorageLocation " + needle + " is in Unavailable status."},
		{FieldBackupFailure, "backup execution failed: put object: AccessDenied: " + needle},
		{FieldScheduleValidation, "invalid schedule: " + needle},
	}
	for _, c := range cases {
		got, _ := Classify(c.field, c.msg)
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), needle) || strings.Contains(string(b), "sig=") {
			t.Errorf("%q disclosed: %s", c.msg, b)
		}
	}
}

func TestVocabulary(t *testing.T) {
	got, _ := Classify(FieldLocationMessage, bslPrefix+"operation error S3: HeadBucket, https response error StatusCode: 403, api error AccessDenied: denied; dial tcp: lookup x: no such host")
	want := []string{"the provider reported AccessDenied", "network: no such host", "HTTP 403"}
	if !got.Withheld || strings.Join(got.Vocabulary, "|") != strings.Join(want, "|") {
		t.Fatalf("%+v", got)
	}
	// a token inside a longer word is not the token
	got, _ = Classify(FieldLocationMessage, bslPrefix+"NotAccessDeniedReally")
	if len(got.Vocabulary) != 0 {
		t.Fatalf("partial token: %+v", got)
	}
	// a tail form keeps its class and adds the vocabulary of its tail
	got, _ = Classify(FieldBackupFailure, "backup execution failed: AccessDenied")
	if got.Code != "execution-failed" || len(got.Vocabulary) != 1 {
		t.Fatalf("tail: %+v", got)
	}
}

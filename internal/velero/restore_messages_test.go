package velero

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every enumerated Restore validation form classifies to its fixed class
// (S2b §1, §5), pinned from v1.18.4's restore_controller.go.
func TestClassifyRestoreValidationForms(t *testing.T) {
	cases := []struct{ msg, code string }{
		{"nodes are non-restorable resources", "restore-non-restorable"},
		{"events.events.k8s.io are non-restorable resources", "restore-non-restorable"},
		{"Invalid included/excluded resource lists: excludes list cannot contain an item in the includes list: pods", "restore-resource-lists"},
		{"Invalid included/excluded namespace lists: includes list must either contain '*' only, or a non-empty list of items", "restore-namespace-lists"},
		{"encountered labelSelector as well as orLabelSelectors in restore spec, only one can be specified", "restore-both-selectors"},
		{"Either a backup or schedule must be specified as a source for the restore, but not both", "restore-source"},
		{"invalid restore hooks: bad", "restore-hooks"},
		{`invalid init container in restore hook "h1": bad`, "restore-init-container"},
		{"Invalid ExistingResourcePolicy: sometimes", "restore-existing-policy"},
		{"Unable to list backups for schedule", "restore-schedule-unlistable"},
		{"No backups found for schedule", "restore-schedule-none"},
		{"No completed backups found for schedule", "restore-schedule-none-completed"},
		{`Error retrieving backup: backups.velero.io "b" not found`, "restore-backup-unreadable"},
		{"The BSL default is unavailable, cannot retrieve the backup", "restore-location-unavailable"},
		{"invalid restore resource policies: bad", "restore-resource-policy"},
		{"failed to get resource modifiers configmap velero/mods", "restore-modifier-unreadable"},
		{"Error in parsing resource modifiers provided in configmap velero/mods: yaml: line 1", "restore-modifier-unparseable"},
		{"Validation error in resource modifiers provided in configmap velero/mods: bad", "restore-modifier-invalid"},
	}
	for _, c := range cases {
		got, ok := Classify(FieldRestoreValidation, c.msg)
		if !ok || got.Code != c.code || got.Withheld {
			t.Errorf("%q: %+v", c.msg, got)
		}
		if p := Placeholder(FieldRestoreValidation, c.msg); !IsPlaceholder(p) || strings.Contains(p, c.msg) {
			t.Errorf("%q: placeholder %q", c.msg, p)
		}
	}
}

// Near-misses of the whole-message forms, v1.17's bare hook errors and
// forged forms are withheld; nothing of the text crosses.
func TestClassifyRestoreValidationWithheld(t *testing.T) {
	for _, msg := range []string{
		"No backups found for schedule " + needle,
		"The BSL default is unavailable, cannot retrieve the backup " + needle,
		"Either a backup or schedule must be specified as a source for the restore, but not both. " + needle,
		"failed to get resource modifiers configmap velero/mods " + needle,
		needle + " are non-restorable resources",
		"error parsing hook " + needle, // v1.17's bare error
	} {
		got, _ := Classify(FieldRestoreValidation, msg)
		if !got.Withheld {
			t.Errorf("%q classified: %+v", msg, got)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), needle) {
			t.Errorf("%q disclosed: %s", msg, b)
		}
	}
	// a tail form's class never carries its tail
	got, _ := Classify(FieldRestoreValidation, "Error retrieving backup: "+needle+" AccessDenied")
	b, _ := json.Marshal(got)
	if got.Code != "restore-backup-unreadable" || strings.Contains(string(b), needle) || len(got.Vocabulary) != 1 {
		t.Fatalf("tail: %s", b)
	}
	for _, msg := range []string{
		"Invalid included/excluded namespace lists: " + needle,
		`invalid init container in restore hook "` + needle + `": x`,
		"Error in parsing resource modifiers provided in configmap " + needle + ": x",
	} {
		got, _ := Classify(FieldRestoreValidation, msg)
		b, _ := json.Marshal(got)
		if got.Withheld || strings.Contains(string(b), needle) {
			t.Errorf("%q: %s", msg, b)
		}
	}
}

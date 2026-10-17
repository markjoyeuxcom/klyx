// Package velero reads Velero's Schedules, BackupStorageLocations and
// Backups (coverage S2a): their projections, Velero's own due time for a
// schedule, the classes of Velero's free text, run identity and the health
// rules. Pure: no client, no clock but the one passed in.
package velero

import (
	"regexp"
	"strings"
)

// The fields that carry Velero's free text (S2a §6). Their text is never
// shown or sent: each message is classified, and only its fixed class
// crosses, with any names it needs taken from object fields Klyx reads.
const (
	FieldLocationMessage    = "BackupStorageLocation status.message"
	FieldBackupFailure      = "Backup status.failureReason"
	FieldBackupValidation   = "Backup status.validationErrors"
	FieldScheduleValidation = "Schedule status.validationErrors"
	FieldRestoreFailure     = "Restore status.failureReason"
	FieldRestoreValidation  = "Restore status.validationErrors"
)

// Class is one classified message: a fixed code and Klyx's fixed wording,
// plus the fixed-vocabulary diagnoses found in it. Withheld is true when
// no form matched and the message was withheld whole.
//
// Text follows from Code, a fixed string of Klyx's, so the projection's
// encoded size (the budget's measure) leaves it out.
type Class struct {
	Code       string   `json:"k"`
	Text       string   `json:"-"`
	Withheld   bool     `json:"w,omitempty"`
	Vocabulary []string `json:"v,omitempty"`
}

// Withheld is the wording for a message no form matches.
const WithheldText = "Velero's message is withheld: provider errors can carry credentials"

type form struct {
	re   *regexp.Regexp
	code string
	text string
	// tail: the text after the match is scanned for the vocabulary
	tail bool
}

func f(pattern, code, text string, tail bool) form {
	return form{regexp.MustCompile(`(?s)^` + pattern), code, text, tail}
}

// The BSL message's fixed prefix (backup_storage_location_controller.go
// 222-229); the forms below are matched against what follows it.
var locationPrefix = regexp.MustCompile(`(?s)^BackupStorageLocation "[^"]*" is unavailable: `)

// Forms pinned from Velero v1.18.4 (S2a §6). Nothing from a match is
// re-emitted: a provider can forge a whole form (design revision 2).
var (
	locationForms = []form{
		f(`backup storage location does not use object storage$`, "no-object-storage", "no object storage configured", false),
		f(`object storage provider name must not be empty$`, "no-provider", "no provider configured", false),
		f(`backup storage location's bucket name ".*" must not contain a '/' \(if using a prefix, put it in the 'Prefix' field instead\)$`, "bucket-slash", "the bucket name contains '/' (use the prefix)", false),
		f(`cannot specify both caCert and caCertRef in objectStorage$`, "ca-both", "both caCert and caCertRef are set", false),
		f(`error getting CA certificate from secret: `, "ca-secret", "the CA certificate's Secret could not be read", true),
		f(`unable to get credentials: `, "credentials", "the credentials could not be read", true),
		f(`Backup store contains (?:\d+ )?invalid top-level directories: `, "foreign-dirs", "the bucket holds top-level directories Velero does not own", false),
		f(`unable to locate ObjectStore plugin named \S+$`, "plugin-missing", "the provider's plugin is not installed", false),
	}
	backupValidationForms = []form{
		f(`backup can't be created because backup storage location \S+ is currently in read-only mode$`, "location-read-only", "the storage location is read-only", false),
		f(`backup can't be created because BackupStorageLocation \S+ is in Unavailable status\.$`, "location-unavailable", "the storage location is Unavailable", false),
		f(`an existing backup storage location was not specified at backup creation time and the (?:server )?default `, "location-default-missing", "no storage location was named and the default does not exist", false),
		f(`error getting backup storage location: `, "location-unreadable", "the storage location could not be read", true),
		f(`a VolumeSnapshotLocation CRD for the location `, "vsl-missing", "a volume snapshot location is missing", false),
		f(`error getting volume snapshot location named `, "vsl-unreadable", "a volume snapshot location could not be read", true),
		f(`more than one VolumeSnapshotLocation name specified for provider `, "vsl-duplicate", "more than one volume snapshot location per provider", false),
		f(`invalid resource policies: `, "resource-policy", "the resource policy is invalid", false),
		f(`include-resources, exclude-resources and include-cluster-resources are old filter parameters\.`, "old-filters", "old and new resource filters are mixed", false),
	}
	backupFailureForms = []form{
		f(`found a backup with status "[A-Za-z]+" during the server starting, mark it as "[A-Za-z]+"$`, "server-restart", "the server restarted while this backup ran", false),
		f(`backup execution failed: `, "execution-failed", "the backup failed while running", true),
	}
	scheduleValidationForms = []form{
		f(`Schedule must be a non-empty valid Cron expression$`, "empty-cron", "the cron is empty", false),
		f(`invalid schedule: `, "invalid-cron", "Velero rejected the cron", false),
	}
	restoreFailureForms = []form{
		f(`found a restore with status "[A-Za-z]+" during the server starting, mark it as "[A-Za-z]+"$`, "restore-server-restart", "the server restarted while this restore ran", false),
		f(`restore execution failed: `, "restore-execution-failed", "the restore failed while running", true),
	}
)

// The fixed vocabulary (S2a §6): only a matched entry ever crosses, never
// the text around it.
var (
	vocabTokens = []string{
		// S3
		"AccessDenied", "NoSuchBucket", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken",
		"RequestTimeTooSkewed", "PermanentRedirect", "AuthorizationHeaderMalformed", "NoCredentialProviders",
		// Azure
		"AuthenticationFailed", "AuthorizationFailure", "AuthorizationPermissionMismatch", "ContainerNotFound",
		"AccountIsDisabled", "InvalidAuthenticationInfo",
	}
	vocabPhrases = []string{
		"storage: bucket doesn't exist",
		"no such host", "connection refused", "i/o timeout", "context deadline exceeded",
		"x509: certificate signed by unknown authority", "x509: certificate has expired",
	}
	vocabToken  = regexp.MustCompile(`\b(` + strings.Join(vocabTokens, "|") + `)\b`)
	vocabStatus = regexp.MustCompile(`(?i)\bstatus ?code:? ?([1-5][0-9]{2})\b`)
)

// vocabulary scans s for the fixed list, in a stable order; it returns the
// list's own strings only.
func vocabulary(s string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, m := range vocabToken.FindAllStringSubmatch(s, -1) {
		for _, t := range vocabTokens {
			if m[1] == t { // re-emit the list's string, never the match
				add("the provider reported " + t)
			}
		}
	}
	for _, p := range vocabPhrases {
		if strings.Contains(s, p) {
			add("network: " + p)
		}
	}
	for _, m := range vocabStatus.FindAllStringSubmatch(s, -1) {
		if code, ok := httpCodes[m[1]]; ok {
			add("HTTP " + code)
		}
	}
	return out
}

// httpCodes are the status codes the vocabulary names, as fixed strings.
var httpCodes = func() map[string]string {
	m := map[string]string{}
	for _, c := range []string{"400", "401", "403", "404", "408", "409", "429", "500", "502", "503", "504"} {
		m[c] = c
	}
	return m
}()

// textOfCode is each code's fixed wording: a projection keeps the code, and
// the wording follows from it.
var textOfCode = func() map[string]string {
	m := map[string]string{"withheld": WithheldText}
	for _, forms := range [][]form{locationForms, backupValidationForms, backupFailureForms, scheduleValidationForms, restoreFailureForms} {
		for _, fm := range forms {
			if old, dup := m[fm.code]; dup && old != fm.text {
				panic("velero: code " + fm.code + " has two wordings")
			}
			m[fm.code] = fm.text
		}
	}
	return m
}()

func classify(forms []form, msg string) Class {
	for _, fm := range forms {
		if loc := fm.re.FindStringIndex(msg); loc != nil {
			c := Class{Code: fm.code, Text: fm.text}
			if fm.tail {
				c.Vocabulary = vocabulary(msg[loc[1]:])
			}
			return c
		}
	}
	return Class{Code: "withheld", Text: WithheldText, Withheld: true, Vocabulary: vocabulary(msg)}
}

// Classify classifies one message of a Velero free-text field. An empty
// message has no class (ok false).
func Classify(field, msg string) (Class, bool) {
	if msg == "" {
		return Class{}, false
	}
	switch field {
	case FieldLocationMessage:
		loc := locationPrefix.FindStringIndex(msg)
		if loc == nil {
			return classify(nil, msg), true
		}
		return classify(locationForms, msg[loc[1]:]), true
	case FieldBackupValidation:
		return classify(backupValidationForms, msg), true
	case FieldBackupFailure:
		return classify(backupFailureForms, msg), true
	case FieldScheduleValidation:
		return classify(scheduleValidationForms, msg), true
	case FieldRestoreFailure:
		return classify(restoreFailureForms, msg), true
	case FieldRestoreValidation:
		return classify(nil, msg), true // S2b enumerates its forms
	}
	return classify(nil, msg), true
}

// Placeholder is the generic paths' stand-in for one Velero message (S2a
// §6): the generic detail, Describe's gate, the editor and cache diffs see
// Klyx's fixed wording for its class, from a finite set, never the text.
// The vocabulary is shown only by the lens, so every placeholder is one of
// a fixed list and a second run leaves it as it is.
func Placeholder(field, msg string) string {
	c, ok := Classify(field, msg)
	if !ok {
		return msg
	}
	return placeholderOf(c.Code)
}

func placeholderOf(code string) string { return "<velero: " + textOfCode[code] + ">" }

var placeholders = func() map[string]bool {
	m := map[string]bool{}
	for code := range textOfCode {
		m[placeholderOf(code)] = true
	}
	return m
}()

// IsPlaceholder reports one of Placeholder's fixed strings.
func IsPlaceholder(s string) bool { return placeholders[s] }

// Kinds is every velero.io kind, for the event rule: an Event about any of
// them is withheld whole (S2a §6), since Velero itself records none for the
// kinds Klyx reads and another tool's text is unbounded.
var Kinds = map[string]bool{
	"Backup": true, "BackupRepository": true, "BackupStorageLocation": true, "DeleteBackupRequest": true,
	"DownloadRequest": true, "PodVolumeBackup": true, "PodVolumeRestore": true, "Restore": true, "Schedule": true,
	"ServerStatusRequest": true, "VolumeSnapshotLocation": true, "DataUpload": true, "DataDownload": true,
}

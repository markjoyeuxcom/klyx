package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Line slice 10: Settings > Notifications writes its block and nothing
// else; absent, notifications are off.
func TestSaveNotifications(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	orig := "# my fleet\nclusters:\n  - name: prd\n    context: prd # the prd one\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Notifications != nil {
		t.Fatalf("absent: %+v %v", c.Notifications, err)
	}
	if err := SaveNotifications(path, NotificationsConfig{Enabled: true, Scope: "nonsense", Recoveries: true}); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil || c.Notifications == nil || *c.Notifications != (NotificationsConfig{Enabled: true, Scope: "protected", Recoveries: true}) {
		t.Fatalf("read back: %+v %v", c.Notifications, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), orig) {
		t.Fatalf("other content changed:\n%s", b)
	}
	if err := SaveNotifications(path, NotificationsConfig{Enabled: false, Scope: "all"}); err != nil {
		t.Fatal(err)
	}
	c, _ = Load(path)
	if *c.Notifications != (NotificationsConfig{Scope: "all"}) || strings.Count(string(mustRead(t, path)), "notifications:") != 1 {
		t.Fatalf("replaced: %+v", c.Notifications)
	}
}

// Settings review slice 0 (Codex's cross-check probe), under slice F: a
// save made from the version read before a hand edit refuses it and writes
// nothing; from the file's own version it goes ahead.
func TestANotificationsSaveNeverOverwritesAHandEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: prd\n    context: prd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := "clusters:\n  - name: prd\n    context: prd\nnotifications:\n  enabled: false\n  scope: all\n  recoveries: true\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveNotificationsAt(path, read, NotificationsConfig{Enabled: true}); !errors.Is(err, ErrFileChanged) || string(mustRead(t, path)) != edited {
		t.Fatalf("%v\n%s", err, mustRead(t, path))
	}
	now, _ := FileVersion(path)
	written, err := SaveNotificationsAt(path, now, NotificationsConfig{Enabled: true, Scope: "all", Recoveries: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := FileVersion(path); v != written {
		t.Fatalf("returned version %s, file %s", written, v)
	}
}

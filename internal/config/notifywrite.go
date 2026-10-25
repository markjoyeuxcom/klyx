package config

import (
	"errors"
	"fmt"
)

// ErrNotificationsEdited: fleet.yaml's notifications block was edited after
// Klyx read it, and the save, made from that earlier read, was refused
// rather than written over the edit (Settings review slice 0).
var ErrNotificationsEdited = errors.New("fleet.yaml's notifications block was edited after Klyx read it, so nothing was written: restart Klyx to use the file's settings, or undo that edit")

// NormalNotifications is a notifications block as it takes effect: absent,
// off for protected clusters without recoveries; any scope but all is
// protected.
func NormalNotifications(n *NotificationsConfig) NotificationsConfig {
	if n == nil {
		return NotificationsConfig{Scope: "protected"}
	}
	c := *n
	if c.Scope != "all" {
		c.Scope = "protected"
	}
	return c
}

// SaveNotifications replaces the fleet file's top-level notifications:
// block - written only by Settings > Notifications (The Line slice 10).
// prev is the block as Klyx last read or wrote it: a file whose block has
// been edited since is refused, never overwritten (Settings review slice 0).
func SaveNotifications(path string, n, prev NotificationsConfig) error {
	n = NormalNotifications(&n)
	block := fmt.Sprintf("notifications:\n  enabled: %t\n  scope: %s\n  recoveries: %t\n", n.Enabled, n.Scope, n.Recoveries)
	pre := func(cur *Config) error {
		if NormalNotifications(cur.Notifications) != NormalNotifications(&prev) {
			return ErrNotificationsEdited
		}
		return nil
	}
	return saveTopLevelBlock(path, "notifications", block, "notification settings", pre, func(got *Config) error {
		if got.Notifications == nil || *got.Notifications != n {
			return errors.New("the written notifications block does not read back as intended (file unchanged)")
		}
		return nil
	})
}

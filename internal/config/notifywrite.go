package config

import (
	"errors"
	"fmt"
)

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
func SaveNotifications(path string, n NotificationsConfig) error {
	_, err := SaveNotificationsAt(path, "", n)
	return err
}

// SaveNotificationsAt is SaveNotifications from a plan: version is the file
// version it was made from (ErrFileChanged for any other; "" checks none),
// and it returns the written file's version (Settings slice F §5). The plan
// refused a block edited by hand since what runs was read, so a write under
// that version never replaces such an edit.
func SaveNotificationsAt(path, version string, n NotificationsConfig) (string, error) {
	n = NormalNotifications(&n)
	block := fmt.Sprintf("notifications:\n  enabled: %t\n  scope: %s\n  recoveries: %t\n", n.Enabled, n.Scope, n.Recoveries)
	return saveTopLevelBlock(path, "notifications", block, "notification settings", version, nil, func(got *Config) error {
		if got.Notifications == nil || *got.Notifications != n {
			return errors.New("the written notifications block does not read back as intended (file unchanged)")
		}
		return nil
	})
}

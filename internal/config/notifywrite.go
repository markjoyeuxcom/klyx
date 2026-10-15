package config

import (
	"errors"
	"fmt"
)

// SaveNotifications replaces the fleet file's top-level notifications:
// block - written only by Settings > Notifications (The Line slice 10).
func SaveNotifications(path string, n NotificationsConfig) error {
	if n.Scope != "all" {
		n.Scope = "protected"
	}
	block := fmt.Sprintf("notifications:\n  enabled: %t\n  scope: %s\n  recoveries: %t\n", n.Enabled, n.Scope, n.Recoveries)
	return saveTopLevelBlock(path, "notifications", block, "notification settings", func(got *Config) error {
		if got.Notifications == nil || *got.Notifications != n {
			return errors.New("the written notifications block does not read back as intended (file unchanged)")
		}
		return nil
	})
}

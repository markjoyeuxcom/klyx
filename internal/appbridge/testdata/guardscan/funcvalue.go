package fixture

import "context"

// Codex's F1 review probe: the ticket's Write used, the constructor taken
// as a function value, and the write sent on what it makes
func scale(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	bare := context.Background
	return write(bare())
}

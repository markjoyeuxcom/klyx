package fixture

import (
	"context"
	stdctx "context"
)

// Codex's F1 re-review probe: the constructor under an import alias
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(stdctx.Background())
}

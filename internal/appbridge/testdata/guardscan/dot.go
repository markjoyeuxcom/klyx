package fixture

import (
	"context"
	. "context"
)

// the constructor through a dot import
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(Background())
}

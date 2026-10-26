package fixture

import "context"

// a package-level reference to a constructor
var bg = context.Background

func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(bg())
}

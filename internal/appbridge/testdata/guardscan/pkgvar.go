package fixture

import "context"

// a package-level root context
var root = context.Background()

func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(root)
}

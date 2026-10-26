package fixture

import "context"

// a package-level context
var root = pick()

func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(root)
}

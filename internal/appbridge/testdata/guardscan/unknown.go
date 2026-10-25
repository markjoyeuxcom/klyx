package fixture

import "context"

// a context-named variable of unknown origin
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	ctx := pick()
	return write(ctx)
}

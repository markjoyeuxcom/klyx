package fixture

import "context"

// a context parameter: its origin is the caller's, unknown here
func restart(c string, given context.Context) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(given)
}

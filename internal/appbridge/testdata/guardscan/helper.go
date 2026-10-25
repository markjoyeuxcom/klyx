package fixture

import "context"

// a helper making a root context outside the guarded method
func bg() context.Context { return context.Background() }

func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(bg())
}

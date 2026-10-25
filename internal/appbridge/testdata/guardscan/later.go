package fixture

import "context"

// a variable copied before its source is reassigned unfenced
func restart(c string, other context.Context) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	b := t.Write(context.Background())
	a := b
	b = other
	_ = b
	return write(a)
}

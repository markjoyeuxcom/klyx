package fixture

import "context"

// a fenced context made, an unfenced one forwarded
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	ctx := t.Write(context.Background())
	_ = ctx
	return write(context.Background())
}

package fixture

import "context"

// a closure's context parameter: the scan does not follow the argument
// into it, so the form is refused rather than assumed
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	run := func(ctx context.Context) error { return write(ctx) }
	return run(t.Write(context.Background()))
}

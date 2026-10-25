package fixture

import "context"

// the first write fenced, a second write outside it
func reconcile(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	if err := write(t.Write(context.Background())); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.TODO(), 0)
	defer cancel()
	return write(ctx)
}

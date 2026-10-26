package fixture

import "context"

type svc struct{ ctx context.Context }

// a service's stored context forwarded beside the ticket's
func (s *svc) restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	return write(s.ctx)
}

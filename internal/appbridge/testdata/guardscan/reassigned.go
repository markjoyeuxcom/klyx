package fixture

import "context"

type svc struct{ stored context.Context }

// fenced on one path only: the variable's provenance is every assignment's
func (s *svc) restart(c string, fence bool) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	w := s.stored
	if fence {
		w = t.Write(w)
	}
	return write(w)
}

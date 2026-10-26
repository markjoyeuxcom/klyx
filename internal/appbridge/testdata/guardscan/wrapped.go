package fixture

import "context"

// a wrapper of a context of unknown origin, under a name that does not say
// context
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	w, cancel := context.WithTimeout(pick(), 0)
	defer cancel()
	return write(w)
}

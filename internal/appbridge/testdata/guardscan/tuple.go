package fixture

import "context"

type logs struct{}

// a reviewed-looking helper's tuple result
func (logs) reserve(string) (*int, context.Context, error) { return nil, context.Background(), nil } // unfenced: a fixture's helper

// Codex's F1 re-review probe: a context from a tuple result, under a name
// that does not say context
func restart(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t.Write(context.Background())
	_, lifetime, _ := logs{}.reserve("probe")
	return write(lifetime)
}

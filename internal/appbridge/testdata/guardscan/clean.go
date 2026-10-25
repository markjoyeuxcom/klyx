package fixture

import (
	"context"
	"os/exec"

	"github.com/moomora/klyx/internal/readfence"
)

// a clean guarded method: its contexts from the ticket, a marked unfenced
// one, and the child's start right after the check
func drain(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	if err := check(t.Write(context.Background())); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background()) // unfenced: the stream's lifetime, no cluster request
	defer cancel()
	_ = ctx
	cmd := exec.Command("kubectl")
	if err := t.StartCLI(); err != nil {
		return err
	}
	return cmd.Start()
}

// the dry-run form: a marked base, fenced on the write path, and a
// composed fence inside the ticket's Write
func apply(c string, dryRun bool, conns func() error) error {
	base := context.Background() // unfenced: a dry run writes nothing
	if !dryRun {
		t, err := guardFor(c)
		if err != nil {
			return err
		}
		base = t.Write(readfence.With(base, conns))
	}
	ctx, cancel := context.WithTimeout(base, 0)
	defer cancel()
	vctx, vcancel, _ := fenced(ctx, conns)
	defer vcancel()
	// a further fence and a tracker on a context already known
	return write(readfence.WithWriteTracker(readfence.With(vctx, conns)))
}

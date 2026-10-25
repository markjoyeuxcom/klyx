package fixture

import (
	"context"
	"os/exec"
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

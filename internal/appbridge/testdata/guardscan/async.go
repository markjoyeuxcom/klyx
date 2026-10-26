package fixture

import "os/exec"

// the check before the goroutine, the child started inside it after
// preparation: the start itself is unchecked
func drain(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	if err := t.StartCLI(); err != nil {
		return err
	}
	cmd := exec.Command("kubectl")
	go func() {
		prepare()
		_ = cmd.Start()
	}()
	return nil
}

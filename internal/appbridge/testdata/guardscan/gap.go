package fixture

import "os/exec"

// the check, then work before the start: the start is not the checked one
func drain(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	cmd := exec.Command("kubectl")
	if err := t.StartCLI(); err != nil {
		return err
	}
	prepare()
	return cmd.Start()
}

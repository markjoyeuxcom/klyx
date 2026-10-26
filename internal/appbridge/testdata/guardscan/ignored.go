package fixture

import "os/exec"

// Codex's F1 review probe: StartCLI called, its refusal ignored
func drain(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	cmd := exec.Command("kubectl")
	_ = t.StartCLI()
	return cmd.Start()
}

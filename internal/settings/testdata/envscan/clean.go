package fixture

import (
	"os"

	"github.com/moomora/klyx/internal/envread"
)

// a clean file: os for other things, the environment through envread
func read() (string, error) {
	_, err := os.Stat("/")
	return envread.Get(envread.KlyxConfig), err
}

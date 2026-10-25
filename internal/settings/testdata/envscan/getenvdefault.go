package fixture

import "os"

type reader struct{ getenv func(string) string }

func newReader() reader { return reader{getenv: os.Getenv} }

package fixture

import fp "path/filepath"

type deps struct{ ConfigPath string }

func dir(d deps) string { return fp.Dir(d.ConfigPath) }

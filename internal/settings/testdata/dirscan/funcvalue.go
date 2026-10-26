package fixture

import fp "path/filepath"

// Dir as a function value, aliased
var parent = fp.Dir

func store(p string) string { return fp.Join(parent(p), "new.json") }

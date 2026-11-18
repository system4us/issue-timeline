package commits

import "os"

func writeRaw(file, s string) error { return os.WriteFile(file, []byte(s), 0o644) }

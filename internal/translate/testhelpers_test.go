package translate

import (
	"runtime"
	"testing"
)

// copyArgvFor returns an argv that copies in → out using the
// platform's native file-copy command. Used by TestExecAdapter so the
// test stays portable: macOS/Linux use cp, Windows uses cmd /c copy.
func copyArgvFor(t *testing.T, in, out string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "copy", "/y", in, out}
	}
	return []string{"cp", in, out}
}

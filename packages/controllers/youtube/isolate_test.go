package youtube

import (
	"testing"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// isolate points the data directory at a temporary one for the duration of a
// test.
//
// Without it these tests read and write the real data/quota.json, because
// paths.Root() finds the project regardless of the working directory a test
// runs in. A test that marks the quota exhausted would then leave YouTube
// switched off on the actual machine until midnight -- a test suite that
// breaks the application it is testing.
func isolate(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	paths.SetRoot(directory)
	if _, err := paths.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	return directory
}

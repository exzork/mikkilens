//go:build !windows

package llm

import "os"

// adopt is a no-op off Windows, where process groups handle this differently.
func adopt(*os.Process) {}

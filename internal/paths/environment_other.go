//go:build !windows

package paths

// RestoreUserEnvironment is only needed on Windows.
func RestoreUserEnvironment() {}

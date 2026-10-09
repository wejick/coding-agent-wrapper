//go:build !unix

package tools

// writable is not checked on platforms without access(2); the package
// manager reports the failure itself.
func writable(string) error { return nil }

//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package main

import "os"

// isTerminal reports false where terminals cannot be detected without
// extra dependencies, so init asks for --yes instead of prompting.
func isTerminal(*os.File) bool { return false }

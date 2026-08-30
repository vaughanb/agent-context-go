//go:build !windows && !unix

package main

// lowerProcessPriority is a no-op on platforms without scheduling-priority
// control.
func lowerProcessPriority() error { return nil }

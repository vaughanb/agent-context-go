//go:build unix

package main

import "golang.org/x/sys/unix"

// lowerProcessPriority raises the process nice value so background indexing
// yields CPU to interactive applications.
func lowerProcessPriority() error {
	return unix.Setpriority(unix.PRIO_PROCESS, 0, 10)
}

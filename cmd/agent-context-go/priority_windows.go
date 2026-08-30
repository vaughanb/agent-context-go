//go:build windows

package main

import "golang.org/x/sys/windows"

// lowerProcessPriority drops the process to below-normal scheduling priority
// so background indexing yields CPU to interactive applications.
func lowerProcessPriority() error {
	return windows.SetPriorityClass(windows.CurrentProcess(), windows.BELOW_NORMAL_PRIORITY_CLASS)
}

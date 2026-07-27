//go:build !windows

package main

// runService is the non-Windows half of the service entry point. Only Windows
// starts a process and then expects it to report in to a service manager;
// systemd and launchd read the process itself, so there is nothing to do here.
// Reporting "not handled" keeps main() on the interactive path.
func runService() (bool, error) { return false, nil }

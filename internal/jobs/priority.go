//go:build !windows

package jobs

import "os/exec"

// LowerPriority asks the OS to run a background child below normal priority.
//
// Off Windows it does nothing, deliberately: the governor's NiceCommand puts
// nice and ionice in front of the command line, which is how the priority is
// lowered there, and doing it twice would only make the two disagree.
func LowerPriority(cmd *exec.Cmd) {}

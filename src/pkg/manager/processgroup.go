package manager

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// processGroupAlive checks the owned group, not just its original leader. A
// leader may exit on SIGTERM while a child ignores it; that child still needs
// escalation. Targeted group queries exclude zombies without scanning the host.
func processGroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		// On an inventory error, conservatively retain a group that exists.
		return !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}
	for _, member := range members {
		if member.Proc.P_stat != 0 && member.Proc.P_stat != procStateZombie {
			return true
		}
	}
	return false
}

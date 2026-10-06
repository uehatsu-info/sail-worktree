package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
)

// portFree checks that p is free on both all interfaces and 127.0.0.1
// (on macOS, SO_REUSEADDR lets binding ":p" succeed even if another process is bound only to 127.0.0.1).
// If binding on all interfaces fails (including a permission error) the port is always considered taken.
// See loopbackBindBlocked for how a failure to bind 127.0.0.1 is treated.
func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
	if err != nil {
		return false
	}
	l.Close()
	l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return !loopbackBindBlocked(err, runtime.GOOS)
	}
	l.Close()
	return true
}

// loopbackBindBlocked reports whether the port is considered taken when binding 127.0.0.1 failed with err.
//   - Permission error: macOS does not let an unprivileged user bind a privileged port (below 1024) on 127.0.0.1,
//     but Docker can, so it is not considered taken. On other OSes it is considered taken (on Windows a permission
//     error can mean another process uses the port exclusively).
//   - EADDRNOTAVAIL: there is no 127.0.0.1. Whether the port is in use is unknown here, so it is not considered taken.
//     Windows error codes (WSA*) do not match syscall.EADDRNOTAVAIL, so on Windows it is considered taken (the safe side).
//   - Anything else (EADDRINUSE etc.): taken.
func loopbackBindBlocked(err error, goos string) bool {
	switch {
	case errors.Is(err, os.ErrPermission):
		return goos != "darwin"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return false
	}
	return true
}

// allocatePorts assigns a non-conflicting port to each port variable.
// An existing assignment (current) is reused first (without checking that it is free, since the worktree's own
// containers may be using it). A new port is searched from default+1 upwards, skipping ports assigned to other
// worktrees, ports assigned in this call and ports in use on the host. The default itself is left to the main worktree.
func allocatePorts(vars []PortVar, current map[string]int, taken map[int]bool, free func(int) bool) (map[string]int, error) {
	out := map[string]int{}
	inThis := map[int]bool{}
	for _, v := range vars {
		if p, ok := current[v.Name]; ok && !taken[p] && !inThis[p] {
			out[v.Name] = p
			inThis[p] = true
		}
	}
	for _, v := range vars {
		if _, ok := out[v.Name]; ok {
			continue
		}
		found := false
		for p := v.Default + 1; p <= 65535; p++ {
			if taken[p] || inThis[p] || !free(p) {
				continue
			}
			out[v.Name] = p
			inThis[p] = true
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("no port available for %s", v.Name)
		}
	}
	return out, nil
}

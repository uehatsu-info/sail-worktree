package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

const usage = `sail-worktree - assign non-conflicting ports to Laravel Sail projects running in git worktrees

Usage:
  sail-worktree init            create .sail-worktree.json for the project
  sail-worktree up [args...]    create or update .env, then run sail up (e.g. up -d)
  sail-worktree stop            run sail stop
  sail-worktree rm [-y]         remove containers, networks, volumes and built images, and release the ports
  sail-worktree version         print the version (the tag for go install ...@vX.Y.Z; (devel) or a pseudo-version for a local build)
`

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "init":
		err = cmdInit()
	case "up":
		err = cmdUp(args)
	case "stop":
		err = cmdStop(args)
	case "rm":
		err = cmdRm(args)
	case "version", "--version":
		fmt.Println(version())
		return
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

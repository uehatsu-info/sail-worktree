package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"unicode/utf8"
)

const usage = `sail-worktree - assign non-conflicting ports to Laravel Sail projects running in git worktrees

Usage:
  sail-worktree init            (optional) write .sail-worktree.json to pin the detected compose file and port variables
  sail-worktree up [args...]    create or update .env, then run sail up (e.g. up -d)
  sail-worktree stop            run sail stop
  sail-worktree rm [-y]         remove containers, networks, volumes and built images, and release the ports
  sail-worktree ps [--all] [--json] [--no-docker]
                                list the worktrees in the port registry with their docker state (read-only;
                                --all: every repository, --no-docker: do not ask docker)
  sail-worktree ports [PORT] [--all] [--json] [--no-docker]
                                list the recorded ports with their holders and whether the host can bind them
                                (read-only; PORT: only that port)
  sail-worktree status [--no-docker]
                                show how this worktree is set up and report problems (read-only; exit 1 if any)
  sail-worktree version         print the version (the tag for go install ...@vX.Y.Z; (devel) or a pseudo-version for a local build)

Run the commands in your Laravel project's directory (the one with artisan and the compose file) or below it.
The project may be in a subdirectory of the repository. ps and ports work anywhere inside the repository.
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
	case "ports":
		err = cmdPorts(args)
	case "status":
		err = cmdStatus(args)
	case "ps":
		err = cmdPs(args)
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
		printErr(os.Stderr, err)
		os.Exit(1)
	}
}

// printErr prints the final error. Messages carry paths and names from the repository (directory names may hold any
// byte), so control and format characters and invalid bytes are escaped before they reach the terminal.
func printErr(w io.Writer, err error) {
	fmt.Fprintln(w, "error:", escapeControl(err.Error()))
}

// escapeControl escapes what strconv.IsPrint rejects (C0/C1 controls, DEL, format characters such as U+202E) and
// invalid UTF-8 bytes, as Go escapes. \n is kept because messages use it for layout, so a newline inside a quoted
// path can still start a line of its own; printable non-ASCII text is kept so that such paths stay readable.
func escapeControl(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || strconv.IsPrint(r):
			b.WriteString(s[i : i+n])
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
		i += n
	}
	return b.String()
}

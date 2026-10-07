package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// portRow is one registry port as `ports` shows it. The JSON field names are a public contract.
type portRow struct {
	Port     int    `json:"port"`
	Variable string `json:"variable"`
	Worktree string `json:"worktree"`
	Subdir   string `json:"subdir"`
	State    string `json:"state"`    // the state of the entry, as in ps
	Host     string `json:"host"`     // one of the host* constants
	Process  string `json:"process"`  // what listens on an unavailable port, when known
	PID      int    `json:"pid"`      // 0 when unknown
	Conflict bool   `json:"conflict"` // the port is recorded more than once
}

// What the host says about a port.
const (
	hostFree        = "free"
	hostUnavailable = "unavailable" // cannot be bound: in use, or not allowed to bind
	hostOwn         = "in use by this project"
)

// lookupOwner finds the process that listens on a port (tests replace it).
var lookupOwner = lsofOwner

// lsofOwner asks lsof, on Unix only and best effort: lsof may be missing, or not allowed to see other users'
// processes. The arguments are fixed and the port is formatted here, never taken from text.
func lsofOwner(port int) (name string, pid int, ok bool) {
	if runtime.GOOS == "windows" {
		return "", 0, false
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	out, err := output(os.TempDir(), env, 5*time.Second, "lsof",
		"-nP", "-w", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fcp")
	if err != nil {
		return "", 0, false
	}
	return parseLsof(string(out))
}

// maxProcessName keeps a long or hostile name from taking over a table row.
const maxProcessName = 64

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// parseLsof reads `lsof -Fcp` output: a "p<pid>" line starts a process and the "c<command>" line after it names it.
// The first process wins; malformed lines are ignored. The name is whatever the process called itself, so callers
// escape it before printing.
func parseLsof(out string) (name string, pid int, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			if ok && name != "" {
				return name, pid, true
			}
			if n, err := strconv.Atoi(line[1:]); err == nil && n > 0 {
				pid, name, ok = n, "", true
			}
		case strings.HasPrefix(line, "c") && ok && name == "":
			name = truncateRunes(strings.TrimRight(line[1:], "\r"), maxProcessName)
		}
	}
	if !ok || name == "" {
		return "", 0, false
	}
	return name, pid, true
}

// portsOf lists the rows of the entries, sorted by port. A port recorded more than once (by two entries, or by two
// variables of one) marks all its rows as a conflict.
func portsOf(entries []entry) []portRow {
	var rows []portRow
	count := map[int]int{}
	for _, e := range entries {
		for name, p := range e.Ports {
			rows = append(rows, portRow{Port: p, Variable: name, Worktree: e.Worktree, Subdir: e.Subdir, State: e.State})
			count[p]++
		}
	}
	for i := range rows {
		rows[i].Conflict = count[rows[i].Port] > 1
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Variable != b.Variable {
			return a.Variable < b.Variable
		}
		return a.Worktree < b.Worktree
	})
	return rows
}

// fillHost probes each distinct port once. A port of a running project is that project's own and is not probed.
// The owner is looked up only for a port that cannot be bound.
func fillHost(rows []portRow) {
	type result struct {
		host string
		name string
		pid  int
	}
	seen := map[int]result{}
	for i := range rows {
		r := &rows[i]
		if r.State == stateRunning {
			r.Host = hostOwn
			continue
		}
		res, done := seen[r.Port]
		if !done {
			res.host = hostFree
			if !probePort(r.Port) {
				res.host = hostUnavailable
				if name, pid, ok := lookupOwner(r.Port); ok {
					res.name, res.pid = name, pid
				}
			}
			seen[r.Port] = res
		}
		r.Host, r.Process, r.PID = res.host, res.name, res.pid
	}
}

func hostCell(r portRow) string {
	if r.Process != "" {
		return fmt.Sprintf("%s (%s pid %d)", r.Host, r.Process, r.PID)
	}
	return r.Host
}

func cmdPorts(args []string) error {
	var all, asJSON, noDocker bool
	filter := 0
	for _, a := range args {
		switch {
		case a == "--all":
			all = true
		case a == "--json":
			asJSON = true
		case a == "--no-docker":
			noDocker = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown argument: %q", a)
		case filter != 0:
			return fmt.Errorf("unexpected argument: %q (give one port at most)", a)
		default:
			p, err := parsePortArg(a)
			if err != nil {
				return err
			}
			filter = p
		}
	}
	entries, hidden, err := collectEntries(all)
	if err != nil {
		return err
	}
	if !noDocker {
		fillDockerStates(entries)
	}
	if hidden > 0 {
		fmt.Fprintf(stderr, "note: registry entries not listed: %d (gone or invalid, they still hold their ports); "+
			"use --all\n", hidden)
	}
	rows := portsOf(entries)
	if filter != 0 {
		var kept []portRow
		for _, r := range rows {
			if r.Port == filter {
				kept = append(kept, r)
			}
		}
		if kept == nil { // nobody records it; the host is still worth a look
			kept = []portRow{{Port: filter}}
		}
		rows = kept
	}
	fillHost(rows)
	if asJSON {
		if rows == nil {
			rows = []portRow{}
		}
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, jsonEscape(string(b)))
		return nil
	}
	renderPorts(rows)
	return nil
}

// parsePortArg accepts digits only (no sign, no spaces) in 1..65535.
func parsePortArg(s string) (int, error) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a port number: %q", s)
		}
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("not a port number: %q", s)
	}
	return p, nil
}

func renderPorts(rows []portRow) {
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no registry entries")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "PORT\tVARIABLE\tWORKTREE\tSUBDIR\tSTATE\tHOST\tCONFLICT")
	for _, r := range rows {
		conflict := ""
		if r.Conflict {
			conflict = "yes"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Port, cell(r.Variable), cell(r.Worktree), cell(r.Subdir),
			cell(r.State), cell(hostCell(r)), cell(conflict))
	}
	tw.Flush()
}

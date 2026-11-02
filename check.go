package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// shellName is a variable name that may be shown as a ready-made VAR=N line. Port variable names come from a committed
// file and may be any key, and Sail sources .env, so any other name is only quoted.
var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// cmdCheck records the main worktree of the current repository, so that a repository used only in its main worktree
// is known too, and reports ports that more than one main worktree or worktree in the registry uses. It writes only
// the registry (under the lock, atomically) and never .env: a conflict is fixed by the user, check only suggests how.
// Like status it exits with an error after the report when it finds a conflict; warnings do not change it.
func cmdCheck(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown argument: %s", args[0])
	}
	r := &report{w: stdout}
	if err := checkRecord(r); err != nil {
		return err
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	checkConflicts(r, reg)
	return r.finish()
}

// checkRecord records the current repository's main worktree. Outside a repository or a project there is nothing to
// record, which is only a warning: the rest of the registry can still be checked.
func checkRecord(r *report) error {
	c, err := loadCtx()
	if err != nil {
		r.warn("no main worktree was recorded: %v", err)
		return nil
	}
	cfg, err := c.config()
	if err != nil {
		r.warn("no main worktree was recorded: %v", err)
		return nil
	}
	ports, defaults, envOK, found := mainPorts(c.main, cfg.PortVars)
	if !found {
		r.warn("no main worktree was recorded: the main worktree has no project directory at %q", c.main)
		return nil
	}
	if !envOK {
		r.warn("%s", mainEnvUnreadable(c.main))
	}
	// Read the main's .env before locking, then read the registry after locking, as up does.
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	wt := ""
	if _, ok := reg.Worktrees[c.root]; ok && !c.isMain() {
		wt = c.root
	}
	if reg.recordMain(c.main, wt, ports, defaults, envOK) {
		if err := reg.save(); err != nil {
			return err
		}
	}
	unlock()
	r.line("recorded main worktree: %q", c.main)
	return nil
}

// holder is one port of one main worktree or worktree.
type holder struct {
	main bool
	dir  string
	name string
	port int
	of   string // the main worktree of a worktree, if the registry knows it
}

func (h holder) String() string {
	kind := "worktree"
	if h.main {
		kind = "main worktree"
	}
	s := fmt.Sprintf("%s %q %s", kind, h.dir, cell(h.name))
	if h.of != "" {
		s += fmt.Sprintf(" (of %q)", h.of)
	}
	return s
}

// checkConflicts reports ports with more than one holder. Records of directories that no longer exist (or keys that
// are not absolute clean paths) are left out with a warning: up still keeps their ports free, but they cannot be in
// use, and a deleted clone must not make check fail for ever.
func checkConflicts(r *report, reg *Registry) {
	reserved := map[int]bool{} // every recorded port, stale records included, as up's used() sees them
	var holders []holder
	mains := sortedKeys(reg.Mains)
	for _, k := range mains {
		rec := reg.Mains[k]
		for _, p := range rec.Ports {
			reserved[p] = true
		}
		if fi, err := os.Stat(k); !validKey(k) || err != nil || !fi.IsDir() {
			r.warn("the record of main worktree %q keeps ports free, but the directory no longer exists: %s", k, cell(portsCell(rec.Ports)))
			continue
		}
		for name, p := range mainPortsNow(k, rec) {
			reserved[p] = true
			holders = append(holders, holder{main: true, dir: k, name: name, port: p})
		}
	}
	ofMain := map[string]string{}
	for _, k := range mains {
		for _, w := range reg.Mains[k].Worktrees {
			ofMain[w] = k
		}
	}
	view := foldRegistry(reg)
	wts := sortedKeys(view)
	for _, k := range wts {
		for _, p := range view[k] {
			reserved[p] = true
		}
		if !validKey(k) || !pathExists(k) {
			r.warn("the record of worktree %q keeps ports free, but the directory no longer exists: %s", k, cell(portsCell(view[k])))
			continue
		}
		for name, p := range view[k] {
			holders = append(holders, holder{dir: k, name: name, port: p, of: ofMain[k]})
		}
	}
	sort.SliceStable(holders, func(i, j int) bool {
		a, b := holders[i], holders[j]
		if a.main != b.main {
			return a.main
		}
		if a.dir != b.dir {
			return a.dir < b.dir
		}
		return a.name < b.name
	})
	byPort := map[int][]holder{}
	var ports []int
	for _, h := range holders {
		if h.port < 1 || h.port > 65535 {
			continue
		}
		if len(byPort[h.port]) == 0 {
			ports = append(ports, h.port)
		}
		byPort[h.port] = append(byPort[h.port], h)
	}
	sort.Ints(ports)
	r.line("registry: %d main worktree(s), %d worktree(s)", len(reg.Mains), len(view))
	conflicts := 0
	for _, p := range ports {
		hs := byPort[p]
		if len(hs) < 2 {
			continue
		}
		if conflicts == 0 {
			r.line("conflicts:")
		}
		conflicts++
		parts := make([]string, len(hs))
		for i, h := range hs {
			parts[i] = h.String()
		}
		r.line("  port %d: %s", p, strings.Join(parts, ", "))
		r.line("    keeps it: %s", hs[0])
		for _, h := range hs[1:] {
			r.line("    fix: %s", suggestion(h, reserved))
		}
		r.problem("port %d is used by %d holders", p, len(hs))
	}
	if conflicts == 0 {
		r.line("no port is used more than once")
	}
}

// mainPortsNow re-reads the ports of a recorded main worktree from its .env with the rule of mainPorts, so a change
// made since it was recorded is seen. A .env that is not a regular file is not read; the record is used then.
func mainPortsNow(dir string, rec MainRecord) map[string]int {
	envPath := filepath.Join(dir, ".env")
	if _, err := os.Lstat(envPath); os.IsNotExist(err) {
		return portsFromEnv(nil, rec.Defaults)
	} else if err == nil {
		if e, ok := readEnvIfRegular(envPath); ok {
			return portsFromEnv(e, rec.Defaults)
		}
	}
	return rec.Ports
}

// suggestion tells how to move h off its port. A worktree is moved by up (the other holder's port counts as used); a
// main worktree's .env is never written by this tool, so a free port is proposed. reserved gains the proposed port.
func suggestion(h holder, reserved map[int]bool) string {
	if !h.main {
		return fmt.Sprintf("run `sail-worktree up` in %q (it assigns another port)", h.dir)
	}
	got, err := allocatePorts([]PortVar{{Name: h.name, Default: h.port}}, nil, reserved, probePort)
	if err != nil {
		return fmt.Sprintf("set %s in the .env of %q to a free port (none found above %d)", cell(h.name), h.dir, h.port)
	}
	p := got[h.name]
	reserved[p] = true
	if shellName.MatchString(h.name) {
		return fmt.Sprintf("set %s=%d in the .env of %q", h.name, p, h.dir)
	}
	return fmt.Sprintf("set %+q to %d in the .env of %q", h.name, p, h.dir)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

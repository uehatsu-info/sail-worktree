package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// shellName is a variable name that may be shown as a ready-made VAR=N line. Port variable names come from a committed
// file and may be any key, and Sail sources .env, so any other name is only quoted.
var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// cmdCheck records the main worktree of the current repository, so that a repository used only in its main worktree
// is known too, updates every recorded main worktree from its .env, and reports ports that more than one main
// worktree or worktree in the registry uses. It writes only the registry (under the lock, atomically) and never .env:
// a conflict is fixed by the user, check only suggests how. Like status it exits with an error after the report when
// it finds a conflict; warnings (a lock that another run holds included) do not change it.
func cmdCheck(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown argument: %s", args[0])
	}
	r := &report{w: stdout}
	reg, err := checkRecord(r)
	if err != nil {
		return err
	}
	checkConflicts(r, reg)
	return r.finish()
}

// checkTarget is the main worktree that check records: the one of the repository it runs in, if any.
type checkTarget struct {
	main, worktree    string
	ports, defaults   map[string]int
	envOK, recordable bool
}

// currentMain finds the main worktree to record. Outside a repository or a project there is none, which is only a
// warning: the rest of the registry can still be checked.
func currentMain(r *report) checkTarget {
	c, err := loadCtx()
	if err != nil {
		r.warn("no main worktree was recorded: %v", err)
		return checkTarget{}
	}
	cfg, err := c.config()
	if err != nil {
		r.warn("no main worktree was recorded: %v", err)
		return checkTarget{}
	}
	ports, defaults, envOK, found := mainPorts(c.main, cfg.PortVars)
	if !found {
		r.warn("no main worktree was recorded: the main worktree has no project directory at %q", c.main)
		return checkTarget{}
	}
	if !envOK {
		r.warn("%s", mainEnvUnreadable(c.main))
	}
	cur := checkTarget{main: c.main, ports: ports, defaults: defaults, envOK: envOK, recordable: true}
	if !c.isMain() {
		cur.worktree = c.root
	}
	return cur
}

// checkLock takes the registry lock for check; tests replace it to act between the reads before it and the lock.
var checkLock = lockRegistry

// checkRecord records the current main worktree and refreshes the others, then returns the registry as saved. up reads
// only the records, so a port changed in a main's .env is avoided only once a record holds it; without the refresh,
// check could tell a worktree to run up and up would keep the port. The mains' .env are read before the lock, so that a
// slow drive does not keep other runs waiting; under the lock the registry is read again and only that copy is saved.
// When another run holds the lock for longer than the wait, the report is still worth having, so nothing is recorded
// and the registry is read without the lock, which is safe because it is replaced atomically; a broken registry stays
// an error. Any other lock failure (no permission, a read-only file system) is an error too: the report would hide a
// setup in which no run can record anything.
func checkRecord(r *report) (*Registry, error) {
	pre, err := loadRegistry() // only for the keys and the records the refresh is based on
	if err != nil {
		return nil, err
	}
	fresh := readMains(pre)
	warnDuplicates(r, fresh)
	cur := currentMain(r) // read last, so the current main's record comes from the newest read of its .env
	unlock, err := checkLock()
	if err != nil {
		if !errors.Is(err, errLockHeld) {
			return nil, err
		}
		r.warn("nothing was recorded or refreshed, so this report may be based on records that refreshing would have "+
			"changed: %v", err)
		return loadRegistry() // again: pre may be as old as the wait for the lock
	}
	defer unlock()
	reg, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	changed := false
	if cur.recordable {
		wt := ""
		if _, ok := reg.Worktrees[cur.worktree]; ok && cur.worktree != "" {
			wt = cur.worktree
		}
		changed = reg.recordMain(cur.main, wt, cur.ports, cur.defaults, cur.envOK)
		delete(fresh, cur.main) // recordMain used a newer read of the same .env
	}
	if refreshMains(reg, fresh) {
		changed = true
	}
	if changed {
		if err := reg.save(); err != nil {
			return nil, err
		}
	}
	unlock()
	if cur.recordable {
		r.line("recorded main worktree: %q", cur.main)
	}
	return reg, nil
}

// mainRefresh is what readMains found in a recorded main worktree's .env: its ports, the record they were based on, and
// the port variables set on more than one line.
type mainRefresh struct {
	ports, basePorts, baseDefaults map[string]int
	duplicates                     []string
}

// readMains reads the ports of every recorded main worktree with the rule of mainPorts and the recorded defaults. A key
// is used as a directory only after validKey, and a main whose .env cannot be read is left out (its record is kept).
// The port variables are the recorded defaults' keys, so a variable added to the configuration counts from the next
// check, as its port does.
func readMains(reg *Registry) map[string]mainRefresh {
	out := map[string]mainRefresh{}
	for k, rec := range reg.Mains {
		if !usableDir(k) {
			continue
		}
		env, ok := readMainEnv(k)
		if !ok {
			continue
		}
		f := mainRefresh{ports: portsFromEnv(env, rec.Defaults), basePorts: rec.Ports, baseDefaults: rec.Defaults}
		if env != nil {
			f.duplicates = env.duplicateKeys(slices.Collect(maps.Keys(rec.Defaults)))
		}
		out[k] = f
	}
	return out
}

// warnDuplicates warns about port variables that a main's .env sets on more than one line: the port is read from the
// last line, as Sail and compose do, but Laravel's Dotenv reads the first, so the two can disagree unseen. It is only a
// warning, and the values are not shown (a port variable name may be any key).
func warnDuplicates(r *report, fresh map[string]mainRefresh) {
	for _, k := range slices.Sorted(maps.Keys(fresh)) {
		for _, name := range fresh[k].duplicates {
			r.warn("the .env of main worktree %q sets %s on more than one line; Sail and compose use the last line, "+
				"Laravel's Dotenv the first, so keep only one", k, cell(name))
		}
	}
}

// refreshMains applies what readMains found, but only to a record whose ports and defaults (all a refresh depends on)
// are still those it was based on: one that another run changed meanwhile is newer, and the next check refreshes it.
func refreshMains(reg *Registry, fresh map[string]mainRefresh) bool {
	changed := false
	for k, f := range fresh {
		rec, ok := reg.Mains[k]
		if !ok || !maps.Equal(rec.Ports, f.basePorts) || !maps.Equal(rec.Defaults, f.baseDefaults) {
			continue
		}
		if !maps.Equal(f.ports, rec.Ports) {
			rec.Ports = f.ports
			reg.Mains[k] = rec
			changed = true
		}
	}
	return changed
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
// are not absolute clean paths) and main records that another record replaced (replacedMain) are left out with a
// warning: up still keeps their ports free, but they cannot be in use, and a deleted clone or a moved project must not
// make check fail for ever.
// Entries of a main record's linked worktrees that prune removes are warned about too, although they hold no ports.
func checkConflicts(r *report, reg *Registry) {
	// A suggestion must avoid every port up treats as taken: alias keys and stale records included. used returns a
	// fresh map, so suggestion can add to it.
	reserved := reg.used("")
	var holders []holder
	mains := slices.Sorted(maps.Keys(reg.Mains))
	for _, k := range mains {
		rec := reg.Mains[k]
		if !usableDir(k) {
			r.warn("the record of main worktree %q keeps ports free, but it is not a usable local directory: %s", k, cell(portsCell(rec.Ports)))
			continue
		}
		if by, why := reg.replacedMain(k); by != "" {
			r.warn("the record of main worktree %q keeps ports free, but %s: %s", k, why, cell(portsCell(rec.Ports)))
			continue
		}
		for name, p := range rec.Ports {
			holders = append(holders, holder{main: true, dir: k, name: name, port: p})
		}
		// prune's test for the list entries, so that prune removes only what check warns about.
		for _, w := range slices.Sorted(slices.Values(rec.Worktrees)) {
			if !usableDir(w) {
				r.warn("the record of main worktree %q lists linked worktree %q, which is not a usable local directory "+
					"(the entry holds no ports; prune removes it)", k, w)
			}
		}
	}
	ofMain := map[string]string{} // keyed by the folded path, as view is, so that an alias still matches
	for _, k := range mains {
		for _, w := range reg.Mains[k].Worktrees {
			ofMain[foldKey(w)] = k
		}
	}
	view := foldRegistry(reg)
	wts := slices.Sorted(maps.Keys(view))
	for _, k := range wts {
		if !usableDir(k) {
			r.warn("the record of worktree %q keeps ports free, but it is not a usable local directory: %s", k, cell(portsCell(view[k])))
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
		if !validPort(h.port) {
			continue
		}
		if len(byPort[h.port]) == 0 {
			ports = append(ports, h.port)
		}
		byPort[h.port] = append(byPort[h.port], h)
	}
	sort.Ints(ports)
	r.line("registry: %d main worktree record(s), %d worktree record(s)", len(reg.Mains), len(view))
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
		r.problem("port %d is used %d times", p, len(hs))
	}
	if conflicts == 0 {
		r.line("no port is used more than once")
	}
}

// isDir reports whether p is a directory itself, not a link to one: a registry key is untrusted.
func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// suggestion tells how to move h off its port. A worktree is moved by up (the other holder's port counts as used); a
// main worktree's .env is never written by this tool, so a free port is proposed. reserved gains the proposed port.
func suggestion(h holder, reserved map[int]bool) string {
	if !h.main {
		return fmt.Sprintf("run `sail-worktree up` in %q (it assigns another port)", h.dir)
	}
	got, err := allocatePorts([]PortVar{{Name: h.name, Default: h.port}}, nil, reserved, probePort)
	if err != nil {
		return fmt.Sprintf("set %+q in the .env of %q to a free port (none found above %d)", h.name, h.dir, h.port)
	}
	p := got[h.name]
	reserved[p] = true
	if shellName.MatchString(h.name) {
		return fmt.Sprintf("set %s=%d in the .env of %q", h.name, p, h.dir)
	}
	return fmt.Sprintf("set %+q to %d in the .env of %q", h.name, p, h.dir)
}

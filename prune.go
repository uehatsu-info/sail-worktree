package main

import (
	"bufio"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// cmdPrune drops the registry records that check warns about: keys that are not usable local directories and, in a
// main worktree's record, linked worktrees whose directory is gone. Such a record keeps its ports reserved for ever
// (up avoids them), and nothing else removes it. It writes only the registry and runs neither docker, sail nor git, so
// the containers, networks and volumes of a removed worktree are left alone.
// The list is shown and confirmed without the lock (a prompt can wait for ever); after locking, the registry is read
// again and only listed records that are still recorded and still unusable are removed.
func cmdPrune(args []string) error {
	yes := false
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			yes = true
		} else {
			return fmt.Errorf("unknown argument: %s", a)
		}
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	r := &report{w: stdout}
	list := findPrunable(reg)
	if list.empty() {
		r.line("nothing to remove: every record in the registry is a usable local directory")
		return nil
	}
	r.line("these registry records are not usable local directories:")
	for _, k := range list.worktrees {
		r.line("  worktree %q (ports: %s)", k, cell(portsCell(reg.Worktrees[k])))
	}
	for _, k := range list.mains {
		r.line("  main worktree %q (ports: %s)", k, cell(portsCell(reg.Mains[k].Ports)))
	}
	for _, l := range list.linked {
		r.line("  linked worktree %q in the record of main worktree %q", l.worktree, l.main)
	}
	r.line("prune removes only these records from the registry; it does not touch containers, networks or volumes " +
		"(find those of a removed worktree with `docker compose ls -a`).")
	if !yes {
		fmt.Fprint(stdout, "Remove these records? [y/N] ")
		ans, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			r.line("aborted")
			return nil
		}
	}
	unlock, err := lockRegistry()
	if err != nil {
		return fmt.Errorf("the registry could not be locked, so nothing was removed (run prune again): %w", err)
	}
	defer unlock()
	// Read again under the lock: another run may have changed the registry while the prompt waited.
	reg, err = loadRegistry()
	if err != nil {
		return fmt.Errorf("nothing was removed: %w", err)
	}
	done, kept := reg.prune(list)
	if !done.empty() {
		if err := reg.save(); err != nil {
			return fmt.Errorf("nothing was removed: %w", err)
		}
	}
	unlock() // before the report, which can block on a slow terminal
	for _, k := range kept {
		r.line("kept (changed since the list): %s", k)
	}
	r.line("removed %d worktree record(s), %d main worktree record(s), %d linked worktree entr(ies)",
		len(done.worktrees), len(done.mains), len(done.linked))
	return nil
}

// pruneList is what prune removes, sorted. linked are entries of a kept main worktree's list (a removed main takes
// its list with it).
type pruneList struct {
	worktrees, mains []string
	linked           []linkedEntry
}

type linkedEntry struct{ main, worktree string }

func (l pruneList) empty() bool {
	return len(l.worktrees) == 0 && len(l.mains) == 0 && len(l.linked) == 0
}

// usableDir reports whether a registry key can be used as a local directory: validKey first, so that an invalid key
// (on Windows a UNC or device path) is never looked at, then isDir (a link as its last element is not followed).
func usableDir(k string) bool { return validKey(k) && isDir(k) }

// staleWorktreeKey reports whether a worktrees key is unusable. An alias that an older version recorded under a
// symlinked path counts as the directory it resolves to, as in foldRegistry and checkConflicts (so prune removes
// what check warns about): it still holds the ports of a live worktree, and the next up there folds it into the real path.
func staleWorktreeKey(k string) bool {
	if !validKey(k) {
		return true
	}
	if rp, err := realPath(k); err == nil {
		return !validKey(rp) || !isDir(rp)
	}
	return !isDir(k)
}

// findPrunable lists the unusable records of reg. Main worktree keys and their lists are recorded as real paths, so
// they are checked without following links.
func findPrunable(reg *Registry) pruneList {
	var l pruneList
	for _, k := range slices.Sorted(maps.Keys(reg.Worktrees)) {
		if staleWorktreeKey(k) {
			l.worktrees = append(l.worktrees, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(reg.Mains)) {
		if !usableDir(k) {
			l.mains = append(l.mains, k)
			continue
		}
		for _, w := range slices.Sorted(slices.Values(reg.Mains[k].Worktrees)) {
			if !usableDir(w) {
				l.linked = append(l.linked, linkedEntry{main: k, worktree: w})
			}
		}
	}
	return l
}

// prune removes the listed records that are still recorded and still unusable, with the same tests as findPrunable,
// and returns what it removed and the listed records it kept (quoted for the report). Records that became unusable
// after the list are left for the next prune: the user did not confirm them.
func (r *Registry) prune(list pruneList) (done pruneList, kept []string) {
	for _, k := range list.worktrees {
		if _, ok := r.Worktrees[k]; !ok {
			continue // already gone
		}
		if !staleWorktreeKey(k) {
			kept = append(kept, fmt.Sprintf("worktree %q", k))
			continue
		}
		delete(r.Worktrees, k)
		done.worktrees = append(done.worktrees, k)
	}
	for _, k := range list.mains {
		if _, ok := r.Mains[k]; !ok {
			continue
		}
		if usableDir(k) {
			kept = append(kept, fmt.Sprintf("main worktree %q", k))
			continue
		}
		delete(r.Mains, k)
		done.mains = append(done.mains, k)
	}
	for _, l := range list.linked {
		rec, ok := r.Mains[l.main]
		if !ok || !slices.Contains(rec.Worktrees, l.worktree) {
			continue
		}
		if usableDir(l.worktree) {
			kept = append(kept, fmt.Sprintf("linked worktree %q in the record of main worktree %q", l.worktree, l.main))
			continue
		}
		r.forgetWorktree(l.main, l.worktree)
		done.linked = append(done.linked, l)
	}
	return done, kept
}

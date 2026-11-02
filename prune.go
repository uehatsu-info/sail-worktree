package main

import (
	"fmt"
	"maps"
	"slices"
)

// cmdPrune drops the registry records that check warns about: keys that are not usable local directories, main
// worktree records that another record replaced (replacedMain) and, in a main worktree's record, linked worktrees whose
// directory is gone. Such a record keeps its ports reserved for ever (up avoids them), and nothing else removes it. It
// writes only the registry and runs neither docker, sail nor git, so the containers, networks and volumes of a removed
// worktree are left alone.
// The list is shown and confirmed without the lock (a prompt can wait for ever). Replaced main records are tested again
// after the prompt, still without the lock, so that a slow disk does not keep other runs waiting; after locking, the
// registry is read again and only listed records that are still recorded and still unusable (or unchanged and still
// replaced) are removed.
func cmdPrune(args []string) error {
	yes, err := parseYes(args)
	if err != nil {
		return err
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	r := &report{w: stdout}
	list := findPrunable(reg)
	if list.empty() {
		r.line("nothing to remove: no record in the registry is stale")
		return nil
	}
	r.line("these registry records are stale:")
	for _, k := range list.worktrees {
		r.line("  worktree %q (ports: %s)", k, cell(portsCell(reg.Worktrees[k])))
	}
	for _, m := range list.mains {
		r.line("  main worktree %q (ports: %s): %s", m.key, cell(portsCell(m.rec.Ports)), m.reason())
	}
	for _, l := range list.linked {
		r.line("  linked worktree %q in the record of main worktree %q", l.worktree, l.main)
	}
	r.line("prune removes only these records from the registry; it does not touch containers, networks or volumes " +
		"(find those of a removed worktree with `docker compose ls -a`).")
	if !yes && !confirm("Remove these records? [y/N] ") {
		return nil
	}
	var kept []string
	list, kept, err = recheckReplaced(list)
	if err != nil {
		return fmt.Errorf("nothing was removed: %w", err)
	}
	unlock, err := pruneLock()
	if err != nil {
		return fmt.Errorf("the registry could not be locked, so nothing was removed (run prune again): %w", err)
	}
	defer unlock()
	// Read again under the lock: another run may have changed the registry while the prompt waited.
	reg, err = loadRegistry()
	if err != nil {
		return fmt.Errorf("nothing was removed: %w", err)
	}
	done, keptNow := reg.prune(list)
	kept = append(kept, keptNow...)
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

// pruneLock takes the registry lock for prune; tests replace it to act between the prompt and the lock.
var pruneLock = lockRegistry

// recheckReplaced tests the listed replaced main records again on a fresh read of the registry, after the prompt and
// before the lock, so that the lock is not held while realPath and the marker look-ups run on a slow disk: the prompt
// can wait for ever, and meanwhile the record may have become a project again. A record no longer replaced by the same
// record is kept; whether the record itself changed is compared under the lock (stillPrunable).
func recheckReplaced(list pruneList) (pruneList, []string, error) {
	reg, err := loadRegistry()
	if err != nil {
		return list, nil, err
	}
	var kept []string
	var mains []prunedMain
	for _, m := range list.mains {
		if m.by != "" {
			if by, _ := reg.replacedMain(m.key); by != m.by {
				kept = append(kept, fmt.Sprintf("main worktree %q", m.key))
				continue
			}
		}
		mains = append(mains, m)
	}
	list.mains = mains
	return list, kept, nil
}

// pruneList is what prune removes, sorted. linked are entries of a kept main worktree's list (a removed main takes
// its list with it).
type pruneList struct {
	worktrees []string
	mains     []prunedMain
	linked    []linkedEntry
}

type linkedEntry struct{ main, worktree string }

// prunedMain is a listed main worktree record: by and why are set when another record replaced it (replacedMain), and
// rec is the record as listed, so that one that up or check recorded again meanwhile is not removed.
type prunedMain struct {
	key, by, why string
	rec          MainRecord
}

func (m prunedMain) reason() string {
	if m.by != "" {
		return m.why
	}
	return "it is not a usable local directory"
}

func (l pruneList) empty() bool {
	return len(l.worktrees) == 0 && len(l.mains) == 0 && len(l.linked) == 0
}

// staleWorktreeKey reports whether a worktrees key, or an entry of a main worktree's list, is unusable. An alias that
// an older version recorded under a symlinked path counts as the directory it resolves to, as in foldRegistry and
// checkConflicts (so prune removes what check warns about): it still names a live worktree (and a key still holds its
// ports), and the next up there folds it into the real path.
func staleWorktreeKey(k string) bool {
	if !validKey(k) {
		return true
	}
	if rp, err := realPath(k); err == nil {
		return !usableDir(rp)
	}
	return !isDir(k)
}

// findPrunable lists the unusable records of reg. Main worktree keys are recorded as real paths and never folded, so
// they are checked without following links; the entries of their lists are folded like worktrees keys (an older
// version recorded aliases there too). A replaced main record takes its list with it.
func findPrunable(reg *Registry) pruneList {
	var l pruneList
	for _, k := range slices.Sorted(maps.Keys(reg.Worktrees)) {
		if staleWorktreeKey(k) {
			l.worktrees = append(l.worktrees, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(reg.Mains)) {
		rec := reg.Mains[k]
		if !usableDir(k) {
			l.mains = append(l.mains, prunedMain{key: k, rec: rec})
			continue
		}
		if by, why := reg.replacedMain(k); by != "" {
			l.mains = append(l.mains, prunedMain{key: k, by: by, why: why, rec: rec})
			continue
		}
		for _, w := range slices.Sorted(slices.Values(reg.Mains[k].Worktrees)) {
			if staleWorktreeKey(w) {
				l.linked = append(l.linked, linkedEntry{main: k, worktree: w})
			}
		}
	}
	return l
}

// prune removes the listed records that are still recorded and still unusable, with the same tests as findPrunable,
// and returns what it removed and the listed records it kept (quoted for the report). Records that became unusable
// after the list are left for the next prune: the user did not confirm them. A replaced main record is not tested
// again here, under the lock (recheckReplaced did it just before): it is removed only when it is unchanged since the
// list and the record that replaces it is still there.
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
	for _, m := range list.mains {
		rec, ok := r.Mains[m.key]
		if !ok {
			continue
		}
		if !m.stillPrunable(r, rec) {
			kept = append(kept, fmt.Sprintf("main worktree %q", m.key))
			continue
		}
		delete(r.Mains, m.key)
		done.mains = append(done.mains, m)
	}
	for _, l := range list.linked {
		rec, ok := r.Mains[l.main]
		if !ok || !slices.Contains(rec.Worktrees, l.worktree) {
			continue
		}
		if !staleWorktreeKey(l.worktree) {
			kept = append(kept, fmt.Sprintf("linked worktree %q in the record of main worktree %q", l.worktree, l.main))
			continue
		}
		r.forgetWorktree(l.main, l.worktree)
		done.linked = append(done.linked, l)
	}
	return done, kept
}

// stillPrunable tests a listed main record under the lock: an unusable one with usableDir again, a replaced one only in
// the registry (rec, the record now, is the one listed, and the record that replaces it is still there).
func (m prunedMain) stillPrunable(r *Registry, rec MainRecord) bool {
	if m.by == "" {
		return !usableDir(m.key)
	}
	_, ok := r.Mains[m.by]
	return ok && maps.Equal(rec.Ports, m.rec.Ports) && maps.Equal(rec.Defaults, m.rec.Defaults) &&
		slices.Equal(rec.Worktrees, m.rec.Worktrees)
}

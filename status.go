package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// probePort reports whether a port can be bound on this host (tests replace it). It binds for an instant and writes
// nothing to disk.
var probePort = portFree

// report collects what status prints. Every line goes through escapeControl, because paths and .env values are
// untrusted; only an allow-list of .env keys is ever shown (never the whole file).
type report struct {
	w        io.Writer
	problems []string
	warnings []string
}

func (r *report) line(format string, args ...any) {
	fmt.Fprintln(r.w, escapeControl(fmt.Sprintf(format, args...)))
}

func (r *report) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

func (r *report) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// list prints items under a heading, or nothing when there are none. The continuation lines of an item are indented
// under its text, so that they cannot pass for a heading or an item of their own.
func (r *report) list(heading string, items []string) {
	if len(items) == 0 {
		return
	}
	r.line("%s", heading)
	for _, m := range items {
		r.line("  - %s", strings.ReplaceAll(m, "\n", "\n    "))
	}
}

// printWarnings prints the warnings alone, for a command that stops with an error before its report is complete.
func (r *report) printWarnings() { r.list("warnings:", r.warnings) }

func (r *report) finish() error {
	r.printWarnings()
	if len(r.problems) == 0 {
		return nil
	}
	r.list("problems:", r.problems)
	return fmt.Errorf("found %d problem(s)", len(r.problems))
}

// cmdStatus shows how this worktree is set up and what is wrong with it. It reads only: .env is parsed, never
// sourced, and neither it nor the registry is written. It exits with an error when it finds a problem (after the
// report), so a script can rely on the exit status; warnings do not change it.
func cmdStatus(args []string) error {
	noDocker := false
	for _, a := range args {
		if a != "--no-docker" {
			return fmt.Errorf("unknown argument: %s", a)
		}
		noDocker = true
	}
	c, err := loadCtx()
	if err != nil {
		return err
	}
	r := &report{w: stdout}
	r.line("project directory: %q", c.root)
	r.line("worktree:          %s (branch %s)", filepath.Base(c.wtTop), currentBranch(c.wtTop))
	if c.bare { // mainTop is the bare repository there, not a checkout (as check's warning says)
		r.line("main worktree:     none (the repository %q is bare, so there is no main worktree checkout)", c.mainTop)
	} else {
		r.line("main worktree:     %q", c.mainTop)
	}
	if c.isMain() {
		r.line("this is the main worktree: up and rm do not run here; check records its ports (up does too, in a worktree)")
		return nil
	}
	if nativeWindows {
		r.warn("on native Windows this report uses this worktree's Windows path and the Windows registry; WSL2 sees " +
			"the worktree under another path, so another compose project name, and has a registry of its own: run up " +
			"and status there")
	}
	proj := c.projectName()
	r.line("compose project:   %s", proj)
	cfg, cfgErr := c.config()
	if cfgErr != nil {
		r.problem("configuration: %v", cfgErr)
	} else {
		src := configName
		if c.detected {
			src = "detected (no " + configName + ")"
		}
		r.line("configuration:     %s, compose file %q", src, cfg.Compose)
		if c.detected && len(cfg.PortVars) == 0 {
			r.problem("%v", noPortVarError(filepath.Join(c.root, cfg.Compose)))
		}
	}
	state := stateNone
	if !noDocker {
		projects, derr := dockerProjects()
		state = dockerState(projects, derr, proj)
	}
	if noDocker {
		r.line("docker:            not asked (--no-docker)")
	} else {
		r.line("docker:            %s", state)
	}

	env := statusEnv(c, r)
	if env != nil {
		checkProjectName(r, env, proj)
	}
	var vars []PortVar
	if cfg != nil {
		vars = cfg.PortVars
	}
	statusPorts(c, r, env, vars, state)
	if env != nil {
		statusDuplicates(r, env, vars)
		statusAppURL(r, env, vars)
	}
	return r.finish()
}

// currentBranch is the branch checked out in dir; best effort, since it is only shown.
func currentBranch(dir string) string {
	if b, err := gitOut(dir, "branch", "--show-current"); err == nil && b != "" {
		return b
	}
	return "(detached)"
}

// statusEnv reports the state of the worktree's .env and returns it when it can be read. A missing file, a link and a
// non-regular file give nil (a link is never read through); a hard-linked file is reported but still read, which is
// harmless.
func statusEnv(c *ctx, r *report) *envFile {
	path := filepath.Join(c.root, ".env")
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			r.line(".env:              missing")
			r.problem(".env is missing; %s", upHint())
		} else {
			r.line(".env:              unreadable")
			r.problem("cannot look at .env: %v", err)
		}
		return nil
	}
	if err := checkOwnEnv(path); err != nil {
		label := "hard link"
		if fi.Mode()&os.ModeSymlink != 0 {
			label = "symbolic link"
		} else if !fi.Mode().IsRegular() {
			label = "not a regular file"
		}
		r.line(".env:              %s", label)
		r.problem("%v", err)
		if !fi.Mode().IsRegular() {
			return nil
		}
	} else {
		r.line(".env:              ok")
	}
	env, err := readEnvIfRegular(path)
	if err != nil {
		r.problem(".env cannot be read: %v", err)
		return nil
	}
	if k, ok := env.overrideKey(upOverrideKeys); ok {
		r.problem(".env has %s, which up refuses (compose would not read the ports and project name up writes)", k)
	}
	return env
}

func checkProjectName(r *report, env *envFile, want string) {
	got, ok := env.Get("COMPOSE_PROJECT_NAME")
	switch {
	case !ok || got == "":
		r.problem("COMPOSE_PROJECT_NAME is missing in .env; %s", upHint())
	case got != want:
		r.problem("COMPOSE_PROJECT_NAME in .env is %+q, but this worktree's name is %+q", got, want)
	}
}

// statusPorts shows each port variable with its .env and registry values and whether the host can bind it, and
// reports the disagreements. state is docker's view of this project: a running project holds its own ports, so they
// are not probed.
func statusPorts(c *ctx, r *report, env *envFile, vars []PortVar, state string) {
	reg, err := loadRegistry()
	if err != nil {
		r.problem("%v", err)
		return
	}
	view := foldRegistry(reg)
	mains := foldMains(reg)
	mine := view[c.root]
	if len(vars) > 0 {
		r.line("ports:")
	}
	for _, v := range vars {
		envPort, haveEnv := 0, false
		if env != nil {
			if s, ok := env.Get(v.Name); !ok {
				r.problem("%s is not set in .env; %s", v.Name, upHint())
			} else if p, ok := parsePort(s); !ok {
				// The value is not shown: a port variable name comes from a committed file and may be any key.
				r.problem("%s in .env is not a port number", v.Name)
			} else {
				envPort, haveEnv = p, true
			}
		}
		regPort, haveReg := mine[v.Name]
		if haveReg && !validPort(regPort) {
			r.problem("%s is recorded as %d in the registry, which is not a port number", v.Name, regPort)
			regPort, haveReg = 0, false
		} else if !haveReg {
			r.problem("%s is not recorded in the registry; %s", v.Name, upHint())
		}
		if haveEnv && haveReg && envPort != regPort {
			r.problem("%s is %d in .env but %d in the registry", v.Name, envPort, regPort)
		}
		port := envPort
		if !haveEnv {
			port = regPort
		}
		for _, other := range otherHolders(view, mains, c.root, port) {
			r.problem("port %d (%s) is also recorded for %s", port, v.Name, other)
		}
		host, blocked := hostState(port, state)
		r.line("  %-20s env=%s registry=%s host=%s", v.Name, portText(envPort, haveEnv), portText(regPort, haveReg), host)
		if blocked {
			r.warn("port %d (%s) cannot be bound although the project is not running; another process may use it",
				port, v.Name)
		}
	}
}

// statusDuplicates warns about port variables that .env sets on more than one line: status shows the first line, but
// Sail and compose use the last, so the port shown may not be the one in use. up collapses them to the first line.
// The values are not shown (a port variable name may be any key).
func statusDuplicates(r *report, env *envFile, vars []PortVar) {
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	for _, name := range env.duplicateKeys(names) {
		fix := "run up, which keeps only the first"
		if nativeWindows {
			fix = "up keeps only the first (" + upHint() + ")"
		}
		r.warn("%s is set on more than one line in .env; status shows the first line, but Sail and compose use the "+
			"last (Laravel's Dotenv the first); %s", cell(name), fix)
	}
}

// upHint is the advice to run up. On native Windows up is refused (nativeWindowsError), and inside WSL2 the worktree
// has another path, so another project name, and another registry: what up fixes there only status there shows.
func upHint() string {
	if nativeWindows {
		return "run up and status inside WSL2"
	}
	return "run up"
}

// parsePort reads a port number from an .env value.
func parsePort(s string) (int, bool) {
	p, err := strconv.Atoi(s)
	return p, err == nil && validPort(p)
}

// otherHolders lists, sorted, the registry entries other than self that record port, and then the main worktree
// records that do, with check's labels (holder.String). Every main record counts, also one of a directory that is gone,
// because up avoids them all and would move this port on its next run; one that another record replaced says so. A
// worktree's main is not named: the line is about this worktree's port, and the holder's own directory already tells
// which record to look at.
func otherHolders(view map[string]map[string]int, mains map[string]mainView, self string, port int) []string {
	// A value that is not a port number holds nothing, as in check and ports. The callers pass a valid port or 0, and
	// an equal recorded value is then valid too.
	if !validPort(port) {
		return nil
	}
	var hs []holder
	for dir, ports := range view {
		if dir == self {
			continue
		}
		for name, p := range ports {
			if p == port {
				hs = append(hs, holder{dir: dir, name: name, port: p})
			}
		}
	}
	for dir, m := range mains {
		for name, p := range m.rec.Ports {
			if p == port {
				hs = append(hs, holder{main: true, dir: dir, name: name, port: p, replacedBy: m.replacedBy})
			}
		}
	}
	sort.Slice(hs, func(i, j int) bool {
		a, b := hs[i], hs[j]
		if a.main != b.main {
			return !a.main // worktrees first
		}
		if a.dir != b.dir {
			return a.dir < b.dir
		}
		return a.name < b.name
	})
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.String()
	}
	return out
}

func portText(p int, ok bool) string {
	if !ok {
		return "-"
	}
	return strconv.Itoa(p)
}

// hostState is what the probe says about a port, and whether that is a finding: only a port that cannot be bound
// while docker says the project is not running. A running project holds its own ports, and without docker's answer
// (unknown, or --no-docker) a bound port may be this project's own, so neither is probed.
func hostState(port int, state string) (text string, blocked bool) {
	switch {
	case port == 0:
		return "-", false
	case state == stateRunning:
		return "in use by this project", false
	case state == stateUnknown || state == stateNone:
		return "not probed", false
	case probePort(port):
		return "free", false
	}
	return "cannot bind", true
}

// statusAppURL checks that APP_URL points at the assigned APP_PORT and that SANCTUM_STATEFUL_DOMAINS has its entry.
// The URL is shown without credentials.
func statusAppURL(r *report, env *envFile, vars []PortVar) {
	raw, ok := env.Get("APP_URL")
	if !ok {
		r.line("APP_URL:           -")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		r.line("APP_URL:           (not a URL with a host)")
		return
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", "" // may hold secrets
	r.line("APP_URL:           %q", u.String())
	// up rewrites APP_URL with the APP_PORT it assigned. An invalid value is reported with the ports and must not be
	// echoed here.
	configured := false
	for _, v := range vars {
		configured = configured || v.Name == "APP_PORT"
	}
	if !configured {
		return
	}
	s, ok := env.Get("APP_PORT")
	if !ok {
		return
	}
	appPort, ok := parsePort(s)
	if !ok {
		return
	}
	if u.Port() != strconv.Itoa(appPort) {
		r.problem("APP_URL %q does not use APP_PORT %d; %s", u.String(), appPort, upHint())
		return
	}
	// addStatefulDomain edits what it is given, so it gets a copy; nothing here is written.
	added, warning := addStatefulDomain(&envFile{lines: append([]string(nil), env.lines...)}, u)
	if added != "" {
		r.problem("%s lacks %q for APP_URL; %s", sanctumKey, added, upHint())
	}
	if warning != "" {
		r.warn("%s", warning)
	}
}

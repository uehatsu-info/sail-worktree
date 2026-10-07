package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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

func (r *report) finish() error {
	if len(r.warnings) > 0 {
		r.line("warnings:")
		for _, m := range r.warnings {
			r.line("  - %s", m)
		}
	}
	if len(r.problems) == 0 {
		return nil
	}
	r.line("problems:")
	for _, m := range r.problems {
		r.line("  - %s", m)
	}
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
	r.line("main worktree:     %q", c.mainTop)
	if c.isMain() {
		r.line("this is the main worktree: up, stop and rm do not run here, and nothing is recorded for it")
		return nil
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
	r.line("docker:            %s", state)

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
			r.problem(".env is missing; run up")
		} else {
			r.line(".env:              unreadable")
			r.problem("cannot look at .env: %v", err)
		}
		return nil
	}
	if err := checkOwnEnv(path); err != nil {
		r.line(".env:              not a plain file")
		r.problem("%v", err)
		if !fi.Mode().IsRegular() {
			return nil
		}
	} else {
		r.line(".env:              ok")
	}
	env, ok := readEnvIfRegular(path)
	if !ok {
		r.problem(".env cannot be read")
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
		r.problem("COMPOSE_PROJECT_NAME is missing in .env; run up")
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
	mine := view[c.root]
	if len(vars) > 0 {
		r.line("ports:")
	}
	for _, v := range vars {
		envPort, haveEnv := 0, false
		if env != nil {
			if s, ok := env.Get(v.Name); ok {
				if p, err := strconv.Atoi(s); err == nil && p >= 1 && p <= 65535 {
					envPort, haveEnv = p, true
				} else {
					r.problem("%s in .env is %+q, not a port number", v.Name, s)
				}
			} else {
				r.problem("%s is not set in .env; run up", v.Name)
			}
		}
		regPort, haveReg := mine[v.Name]
		if !haveReg {
			r.problem("%s is not recorded in the registry; run up", v.Name)
		}
		if haveEnv && haveReg && envPort != regPort {
			r.problem("%s is %d in .env but %d in the registry", v.Name, envPort, regPort)
		}
		port := envPort
		if !haveEnv {
			port = regPort
		}
		for other, ports := range view {
			if other == c.root {
				continue
			}
			for name, p := range ports {
				if port != 0 && p == port {
					r.problem("port %d (%s) is also recorded for %q (%s)", port, v.Name, other, name)
				}
			}
		}
		r.line("  %-20s env=%s registry=%s host=%s", v.Name, portText(envPort, haveEnv), portText(regPort, haveReg),
			hostText(port, state))
		if port != 0 && state != stateRunning && state != stateUnknown && state != stateNone && !probePort(port) {
			r.warn("port %d (%s) cannot be bound although the project is not running; another process may use it", port, v.Name)
		}
	}
}

func portText(p int, ok bool) string {
	if !ok {
		return "-"
	}
	return strconv.Itoa(p)
}

// hostText is what the probe says about a port. A running project holds its own ports, and without docker's answer a
// bound port may be this project's own, so neither is probed.
func hostText(port int, state string) string {
	switch {
	case port == 0:
		return "-"
	case state == stateRunning:
		return "in use by this project"
	case state == stateUnknown || state == stateNone:
		return "not probed"
	case probePort(port):
		return "free"
	}
	return "cannot bind"
}

// statusAppURL checks that APP_URL points at the assigned APP_PORT and that SANCTUM_STATEFUL_DOMAINS has its entry.
// The URL is shown without credentials.
func statusAppURL(r *report, env *envFile, vars []PortVar) {
	raw, ok := env.Get("APP_URL")
	if !ok {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		r.line("APP_URL:           (not a URL with a host)")
		return
	}
	u.User = nil
	r.line("APP_URL:           %q", u.String())
	appPort := 0
	for _, v := range vars {
		if v.Name == "APP_PORT" {
			if s, ok := env.Get("APP_PORT"); ok {
				appPort, _ = strconv.Atoi(s)
			}
		}
	}
	if appPort == 0 {
		return
	}
	if u.Port() != strconv.Itoa(appPort) {
		r.problem("APP_URL %q does not use APP_PORT %d; run up", u.String(), appPort)
		return
	}
	// addStatefulDomain edits what it is given, so it gets a copy; nothing here is written.
	added, warning := addStatefulDomain(&envFile{lines: append([]string(nil), env.lines...)}, u)
	if added != "" {
		r.problem("%s lacks %q for APP_URL; run up", sanctumKey, added)
	}
	if warning != "" {
		r.warn("%s", warning)
	}
}

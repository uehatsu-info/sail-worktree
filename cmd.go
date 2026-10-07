package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func cwd() (string, error) { return os.Getwd() }

func cmdInit() error {
	dir, err := cwd()
	if err != nil {
		return err
	}
	wtTop, prefix, err := worktreeRootAndPrefix(dir)
	if err != nil {
		return fmt.Errorf("run this inside a git repository: %w", err)
	}
	root, cand, err := findProject(wtTop, prefix, configOrLaravel)
	if err != nil {
		return err
	}
	if cand == nil {
		return projectNotFoundError(wtTop, prefix)
	}
	path := filepath.Join(root, configName)
	compose, existed := initCompose(root, cand.marker)
	if compose == "" {
		return fmt.Errorf("no compose file (%s) found in %q", strings.Join(composeNames, ", "), root)
	}
	cfg, err := detectConfig(root, compose)
	if err != nil {
		return err
	}
	if len(cfg.PortVars) == 0 {
		return fmt.Errorf("no port variable found in %q (a host port mapping such as '${APP_PORT:-80}:80'); add one to the compose file, or write %s by hand (see README)", filepath.Join(root, compose), configName)
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	// The project directory may be any subdirectory, so never write through a link placed there.
	if err := writeFileNoFollow(path, append(data, '\n'), 0o644); err != nil {
		if errors.Is(err, errNotOwnFile) {
			return fmt.Errorf("%w; replace it with a real file", err)
		}
		return err
	}
	if existed {
		fmt.Printf("updated %q (detected from %s; edits made by hand are replaced)\n", path, compose)
	} else {
		fmt.Printf("created %q\n", path)
	}
	fmt.Println("commit it to share it with all worktrees, or delete it to go back to detection")
	for _, v := range cfg.PortVars {
		fmt.Printf("  %s (default %d)\n", v.Name, v.Default)
	}
	return nil
}

// initCompose picks the compose file init detects from. An existing .sail-worktree.json is read best effort only to
// keep its compose value; a value that is unsafe or does not open as a regular file falls back to the standard names
// (the write refuses a linked or non-regular .sail-worktree.json later). existed reports a file to be replaced.
func initCompose(root, marker string) (compose string, existed bool) {
	if marker != configName {
		return marker, false
	}
	if b, err := readSmallFile(filepath.Join(root, configName)); err == nil {
		var old Config
		if json.Unmarshal(b, &old) == nil && !unsafeComposePath(old.Compose) {
			if _, err := readSmallFile(filepath.Join(root, old.Compose)); err == nil {
				return old.Compose, true
			}
		}
	}
	if name, ok, err := findMarker(root, composeNames); err == nil && ok {
		return name, true
	}
	return "", true
}

// ctx is the information shared by the commands that run inside a worktree.
// root and main are the project directories of this worktree and of the main worktree; wtTop and mainTop are the
// roots of the two worktrees. All of them are real paths. The configuration is read through config(), because a
// configuration error is reported only after the main-worktree refusal.
type ctx struct {
	root, main     string
	wtTop, mainTop string
	detected       bool // no .sail-worktree.json: compose file and port variables were detected
	cfg            *Config
	cfgErr         error
}

func (c *ctx) config() (*Config, error) { return c.cfg, c.cfgErr }

func loadCtx() (*ctx, error) {
	dir, err := cwd()
	if err != nil {
		return nil, err
	}
	wtTop, prefix, err := worktreeRootAndPrefix(dir)
	if err != nil {
		return nil, fmt.Errorf("run this inside a git repository: %w", err)
	}
	mainTop, err := mainWorktree(wtTop)
	if err != nil {
		return nil, err
	}
	root, cand, detected, err := lookupProject(wtTop, prefix)
	if err != nil {
		return nil, err
	}
	main, err := counterpart(mainTop, cand.rel)
	if err != nil {
		return nil, err
	}
	c := &ctx{root: root, main: main, wtTop: wtTop, mainTop: mainTop, detected: detected}
	if detected {
		c.cfg, c.cfgErr = detectConfig(root, cand.marker)
	} else {
		c.cfg, c.cfgErr = loadConfig(root)
	}
	if cand.rel != strings.TrimSuffix(prefix, "/") {
		fmt.Fprintf(stderr, "project directory: %q\n", root)
	}
	return c, nil
}

// isMain reports whether the command runs in the main worktree. The worktree roots decide; comparing the project
// directories as well is defense in depth, because up and rm must never touch the main worktree.
func (c *ctx) isMain() bool { return c.wtTop == c.mainTop || c.root == c.main }

var nonSlug = regexp.MustCompile(`[^a-z0-9_-]+`)

// projectName is the compose project name: a slug of the two worktree names and a hash of the project directory.
// For a project at the worktree root (root == wtTop) it equals the name older versions computed, so rm keeps working.
func projectName(mainTop, wtTop, root string) string {
	sum := sha1.Sum([]byte(root))
	slug := nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(mainTop)+"-"+filepath.Base(wtTop)), "-")
	return strings.Trim(slug, "-_") + "-" + hex.EncodeToString(sum[:])[:6]
}

func (c *ctx) projectName() string { return projectName(c.mainTop, c.wtTop, c.root) }

// sessionCookieName is the per-worktree session cookie name. localhost shares cookies across ports, so the name is
// derived from the project name to keep logins of other worktrees and projects from mixing.
func sessionCookieName(proj string) string { return proj + "-session" }

// runner runs an external command. Tests replace it.
var runner = runCmdEnv

func cmdUp(args []string) error {
	c, err := loadCtx()
	if err != nil {
		return err
	}
	if c.isMain() {
		return fmt.Errorf("cannot run in the main worktree; run it in a worktree you have created")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	if c.detected && len(cfg.PortVars) == 0 {
		return fmt.Errorf("no port variable found in %q (a host port mapping such as '${APP_PORT:-80}:80'); add one to the compose file, or write %s (see README)", filepath.Join(c.root, cfg.Compose), configName)
	}
	envPath := filepath.Join(c.root, ".env")
	if err := checkOwnEnv(envPath); err != nil {
		return err
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	reg.migrate(c.root) // before allocatePorts, adopt the keys an older version recorded under an alias path (saved when up succeeds)
	src := envOwn
	env, err := readEnv(envPath)
	if os.IsNotExist(err) {
		src = envFromMain
		env, err = readEnv(filepath.Join(c.main, ".env"))
		if os.IsNotExist(err) {
			src = envFromMainExample
			env, err = readEnv(filepath.Join(c.main, ".env.example"))
		}
		if err != nil {
			if c.main != c.mainTop {
				return fmt.Errorf("cannot read the source .env from the main worktree's project directory %q (the main worktree needs the project at the same relative path): %w", c.main, err)
			}
			return fmt.Errorf("cannot read the source .env from the main worktree: %w", err)
		}
		fmt.Fprintln(stdout, "creating .env (copied from the main worktree)")
	} else if err != nil {
		return err
	}

	if k, ok := env.overrideKey(upOverrideKeys); ok {
		return upOverrideError(k, src)
	}
	ports, err := allocatePorts(cfg.PortVars, reg.Worktrees[c.root], reg.used(c.root), portFree)
	if err != nil {
		return err
	}
	for _, v := range cfg.PortVars {
		env.Set(v.Name, strconv.Itoa(ports[v.Name]))
	}
	proj := c.projectName()
	env.Set("COMPOSE_PROJECT_NAME", proj)
	env.Set("SESSION_COOKIE", sessionCookieName(proj))
	var statefulAdded, statefulWarning string
	if appURL, ok := env.Get("APP_URL"); ok {
		if u, err := url.Parse(appURL); err == nil && u.Hostname() != "" && ports["APP_PORT"] != 0 {
			u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(ports["APP_PORT"])) // keeps the brackets of an IPv6 host
			env.Set("APP_URL", u.String())
			statefulAdded, statefulWarning = addStatefulDomain(env, u)
		}
	}
	// The first write: nothing is written in a directory without Sail.
	if _, err := sailPath(c.root); err != nil {
		return err
	}
	if err := env.Write(envPath); err != nil {
		return err
	}
	reg.Worktrees[c.root] = ports
	if err := reg.save(); err != nil {
		return err
	}
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stdout, "  %s=%d\n", n, ports[n])
	}
	if statefulAdded != "" {
		fmt.Fprintf(stdout, "  %s: added %s\n", sanctumKey, statefulAdded)
	}
	if statefulWarning != "" {
		fmt.Fprintf(stderr, "warning: %s\n", statefulWarning)
	}
	return runSail(c.root, cfg, append([]string{"up"}, args...))
}

// envSource is where up took .env from. It is remembered to tailor the guidance in the refusal error.
type envSource int

const (
	envOwn             envSource = iota // the worktree's own .env
	envFromMain                         // the main worktree's .env (copied)
	envFromMainExample                  // the main worktree's .env.example (copied)
)

func upOverrideError(key string, src envSource) error {
	switch src {
	case envOwn:
		return fmt.Errorf(".env has %s, so up cannot continue (it could point at another compose file); remove that line from .env", key)
	case envFromMain:
		return fmt.Errorf("the main worktree's .env has %s, which would be copied into this .env, so up cannot continue (it could point at another compose file). "+
			"Remove it from the main .env (this also affects the source of other worktrees), or create .env in this worktree first without that line and run up again", key)
	case envFromMainExample:
		return fmt.Errorf("the main worktree's .env.example has %s, which would be copied into this .env, so up cannot continue (it could point at another compose file). "+
			"Remove it from .env.example, or create .env in this worktree first without that line and run up again", key)
	}
	return fmt.Errorf("unknown .env source: %d", src)
}

func cmdStop(args []string) error {
	c, err := loadCtx()
	if err != nil {
		return err
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	// stop never writes .env and stopping can be undone, so it does not refuse like up and rm do.
	// It only warns when it may stop another project.
	if !c.isMain() {
		warnStopTarget(c)
	}
	return runSail(c.root, cfg, append([]string{"stop"}, args...))
}

// stdin, stdout and stderr are the input of the confirmation prompt, up's report and warnings (replaced by tests).
var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// safeProjectName is a name that may be embedded in a recovery command. Values in .env are untrusted, so only
// characters that the shell or docker cannot interpret as options are allowed (alphanumeric first, length limit).
var safeProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// nameMismatchError is the error when rm refuses. The first line gives the main remedy (set .env back to the
// worktree's own name). The name in .env (got) is untrusted, so it is shown with %+q and only a got made of safe
// characters is embedded in the recovery command. The worktree's name (want) is built from safe characters by
// projectName, but it is displayed through the same check just in case.
func nameMismatchError(got, want string) error {
	shown := fmt.Sprintf("%+q", want)
	if safeProjectName.MatchString(want) {
		shown = want // without quotes (can be written to .env as is)
	}
	msg := fmt.Sprintf("set COMPOSE_PROJECT_NAME in .env to %s and run rm again (the current %+q does not match this worktree's name, so rm refuses).", shown, got)
	if safeProjectName.MatchString(got) {
		msg += fmt.Sprintf("\nOnly to remove a project that an older version created under a different name, do the following in order."+
			"\n  1. Run `docker compose ls -a` and confirm that %s belongs to this worktree and not to another worktree or project."+
			"\n  2. COMPOSE_* variables in your shell change the target: check with `env | grep '^COMPOSE_'` and unset every variable shown."+
			"\n  3. Run the following (-v also removes volumes, i.e. database data, and cannot be undone):"+
			"\n      docker compose -p %s down -v --rmi local --remove-orphans", got, got)
	} else {
		msg += "\nThe name in .env is not made only of lowercase letters, digits, _ and - (capitals etc. may differ from the name compose uses), so no manual removal command is shown. Check the target with `docker compose ls -a`."
	}
	return fmt.Errorf("%s", msg)
}

// warnStopTarget warns before stop when .env seems to point at another compose file or project.
// It is best effort and cannot detect overrides by shell expressions. It does nothing unless .env is a regular file.
func warnStopTarget(c *ctx) {
	env, ok := readEnvIfRegular(filepath.Join(c.root, ".env"))
	if !ok {
		return
	}
	if k, ok := env.overrideKey(upOverrideKeys); ok {
		fmt.Fprintf(stderr, "warning: .env has %s, so stop may stop a compose project other than this worktree's\n", k)
	}
	if name, ok := env.Get("COMPOSE_PROJECT_NAME"); ok {
		if want := c.projectName(); name != want {
			fmt.Fprintf(stderr, "warning: COMPOSE_PROJECT_NAME in .env (%+q) differs from this worktree's name (%+q); stop may stop another project\n", name, want)
		}
	}
}

func cmdRm(args []string) error {
	yes := false
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			yes = true
		} else {
			return fmt.Errorf("unknown argument: %s", a)
		}
	}
	c, err := loadCtx()
	if err != nil {
		return err
	}
	if c.isMain() {
		return fmt.Errorf("cannot run in the main worktree")
	}
	cfg, err := c.config()
	if err != nil {
		return err
	}
	envPath := filepath.Join(c.root, ".env")
	if err := checkOwnEnv(envPath); err != nil {
		return err
	}
	env, err := readEnv(envPath)
	if err != nil {
		return fmt.Errorf("cannot read .env (run this in a worktree where up has been run): %w", err)
	}
	if k, ok := env.overrideKey(rmOverrideKeys); ok {
		return fmt.Errorf(".env has %s, so rm cannot continue (it could point at another compose file or set of services); remove that line from .env before rm", k)
	}
	proj, ok := env.Get("COMPOSE_PROJECT_NAME")
	if !ok || proj == "" {
		return fmt.Errorf("COMPOSE_PROJECT_NAME is missing in .env")
	}
	// down -v cannot be undone, so do not trust the value in .env: recompute this worktree's name and require an exact
	// match (this refuses a leftover name of another project or worktree, a hand-edited name, or a moved worktree).
	if want := c.projectName(); proj != want {
		return nameMismatchError(proj, want)
	}
	// Do every refusing check before the confirmation prompt (never refuse after the user answered y).
	// -f gets the checked real path, not the configured one, so docker does not resolve the links again.
	composePath, err := composeInsideProject(c.root, cfg.Compose)
	if err != nil {
		return err
	}
	// This read only detects a broken registry before anything is removed (the value is unused). The registry is read
	// again after docker for the release. Do not remove it.
	if _, err := loadRegistry(); err != nil {
		return err
	}
	if !yes {
		fmt.Printf("This removes the containers, networks, volumes (including database data) and built images of project %q. Continue? [y/N] ", proj)
		ans, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			fmt.Println("aborted")
			return nil
		}
	}
	// Pass the project name, directory and compose file explicitly, and run without COMPOSE_* from the environment.
	if err := runner(c.root, cleanEnv(nil), "docker", rmArgs(proj, c.root, composePath)...); err != nil {
		return err
	}
	// Read the registry again after docker so that an update by another up during the removal is not lost.
	reg, err := loadRegistry()
	if err != nil {
		return fmt.Errorf("docker finished removing, but the port assignments cannot be read: %w", err)
	}
	reg.migrate(c.root)
	delete(reg.Worktrees, c.root)
	if err := reg.save(); err != nil {
		return err
	}
	fmt.Println("released the port assignments")
	return nil
}

// rmArgs are the arguments rm passes to docker. The project name, directory and compose file are all explicit.
func rmArgs(proj, root, composePath string) []string {
	return []string{"compose", "--project-name", proj, "--project-directory", root,
		"-f", composePath, "down", "-v", "--rmi", "local", "--remove-orphans"}
}

// sailPath returns vendor/bin/sail of the project, or an error when it is missing.
func sailPath(root string) (string, error) {
	sail := filepath.Join(root, "vendor", "bin", "sail")
	if _, err := os.Stat(sail); err != nil {
		return "", fmt.Errorf("%s not found; run `composer install`", sail)
	}
	return sail, nil
}

func runSail(root string, cfg *Config, args []string) error {
	sail, err := sailPath(root)
	if err != nil {
		return err
	}
	// Shell variables take precedence over .env, so drop the port variables to keep them in line with the assigned ports.
	drop := make([]string, 0, len(cfg.PortVars))
	for _, v := range cfg.PortVars {
		drop = append(drop, v.Name)
	}
	return runner(root, cleanEnv(drop), sail, args...)
}

// cleanEnv returns the current environment without every variable that starts with COMPOSE_, SAIL_FILES and the
// variables in drop (they can point at another compose file or project). DOCKER_HOST etc. are kept because some
// users set them on purpose. This removes environment variables; it is separate from upOverrideKeys and
// rmOverrideKeys, which refuse keys in .env.
func cleanEnv(drop []string) []string { return filterEnv(os.Environ(), drop) }

// filterEnv removes cleanEnv's targets from environ. Names are compared case-insensitively (Windows environment
// variable names are; dropping a lowercase compose_ on unix too is on the safe side). The result is never nil, even
// when everything is removed: a nil Env makes exec.Cmd inherit the whole parent environment. Variables with an empty
// name such as Windows' "=C:=C:\..." are kept.
func filterEnv(environ, drop []string) []string {
	skip := map[string]bool{"SAIL_FILES": true}
	for _, k := range drop {
		skip[strings.ToUpper(k)] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		k = strings.ToUpper(k)
		if !skip[k] && !strings.HasPrefix(k, "COMPOSE_") {
			out = append(out, kv)
		}
	}
	return out
}

func runCmdEnv(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

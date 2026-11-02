# AGENTS.md

Guidance for AI coding agents (and humans) working on this repository.

## What this project is

`sail-worktree` is a small Go CLI (standard library only, no dependencies) that lets several git worktrees of a
Laravel Sail project run side by side. For each worktree it creates or updates `.env` and assigns ports that do not
collide with other worktrees or with the host.

Commands: `init`, `up [args...]`, `stop`, `rm [-y]`, `ps`, `ports`, `status`, `check`, `prune [-y]`, `version`. See `README.md` for the user-facing behavior.

- Module: `github.com/uehatsu-info/sail-worktree` (Go version is set by `go.mod`)
- Install: `go install github.com/uehatsu-info/sail-worktree@latest`, or the binaries attached to each GitHub release

## Language policy

- **Everything is written in English**: code, code comments, error messages, warnings, usage text, tests, commit
  messages, PR descriptions, release notes, workflow comments and `README.md`. The tool is meant to be used by
  developers who do not read Japanese.
- **The only exception is `README_JA.md`**, which is a Japanese translation of `README.md`. When you change
  `README.md`, change `README_JA.md` to match (same sections, same facts).
- Do not add Japanese anywhere else. When a test needs non-ASCII input, write it as a Go escape sequence
  (for example `"\u00e9"`) instead of a literal character.
- The existing commit history before `v0.3.0` is in Japanese. Do not rewrite it.

## Layout

| File | Role |
|---|---|
| `main.go` | Command dispatch (`run`, `helpRequested`: `-h`/`--help` of every command), usage text, `version`, `printErr`/`escapeControl` |
| `cmd.go` | `init`, `up`, `stop`, `rm`, `parseYes`/`confirm` (the `-y` argument and prompt of `rm` and `prune`), `mainPorts`/`readMainEnv`/`portsFromEnv` (a main worktree's ports), `readOwnEnvExample` (the `.env` source in a bare layout), error messages, `sailPath`, `cleanEnv`/`filterEnv`, `runOutput`/`output` (read-only queries) |
| `env.go` | `.env` parsing and writing (`Raw`, `Get`, `Set`, `portValue`, `duplicateKeys`), port variable detection, override keys, `checkOwnEnv`, `readEnvIfRegular`, `writeFileNoFollow` |
| `config.go` | `.sail-worktree.json` (project config, `validatePortVars`), `detectConfig`, `readSmallFile`, the port registry (`writeFileAtomic`, `validKey`/`windowsDriveKey`/`keyLinksOK`/`linksStayLocal`, `usableDir`, `MainRecord`, `recordMain`, `forgetWorktree`), `unsafeComposePath`, `composeInsideProject`, `Registry.migrate`, `replacedMain`/`projectMarkers` (main records a move left behind) |
| `ports.go` | Port allocation, `portFree`, `loopbackBindBlocked` |
| `git.go` | Worktree root and the cwd below it (`worktreeRootAndPrefix`, one `rev-parse` call), main worktree detection (real paths; `parseMainWorktree` flags a bare first entry) |
| `project.go` | Project directory lookup (`projectCandidates`, `findMarker`, `findProject` with a `matcher`: `hasConfig`, `isLaravelProject`, `configOrLaravel`; `lookupProject`, `within`), the not-found error and its hints, the main worktree's counterpart |
| `ps.go` | `ps` and its registry view: `entry`, `collectEntries`/`mainEntries` (`registryView.place`/`list`, `gitInRegistryDirs`, `registryPathOK`), `noteHidden`, `foldRegistry`/`foldKey` (aliases folded in memory), `listWorktrees`/`parseWorktreeList`, `attribute`, `cell`/`jsonEscape` |
| `status.go` | `status`: `report`, `statusEnv`, `statusPorts` (`hostState`, `otherHolders`), `statusAppURL`, `probePort` (replaceable port probe) |
| `portsview.go` | `ports` (not `ports.go`, which allocates): `portRow`, `portsOf`, `fillHost`, `lookupOwner`/`lsofOwner`, `parseLsof` |
| `prune.go` | `prune`: `findPrunable`, `Registry.prune`, `staleWorktreeKey`, `recheckReplaced` |
| `check.go` | `check`: `currentMain`, `checkRecord` (records the main worktree), `readMains`, `refreshMains`, `checkConflicts`, `holder`, `suggestion` |
| `lock.go` | The registry lock: `lockRegistry`, `breakStale`, `claimStale`/`claimPath`, `releaser`, `lockTiming` |
| `links_unix.go` / `links_other.go` | Build-tagged helpers (`O_NOFOLLOW`, `O_NONBLOCK`, hard link count) |
| `sanctum.go` | `SANCTUM_STATEFUL_DOMAINS`: `statefulDomain`, `strIs`, `sanctumUnquote`, `statefulDisabled`, `addStatefulDomain` |
| `sail_worktree_test.go`, `project_test.go`, `detect_test.go`, `sanctum_test.go`, `ps_test.go`, `registry_test.go`, `lock_test.go`, `status_test.go`, `portsview_test.go`, `check_test.go`, `prune_test.go`, `links_unix_test.go` | Tests |
| `.github/workflows/ci.yml`, `release.yml`, `.github/dependabot.yml`, `.goreleaser.yaml` | CI and release |

State outside the repository: the port registry is `os.UserConfigDir()/sail-worktree/registry.json`. The project
config `.sail-worktree.json` is committed to the user's Laravel project.

## Build and test

```sh
go vet ./...
GOOS=windows go vet ./...      # also check that the Windows build compiles
go test ./...
gofmt -l .                      # must print nothing
```

CI runs `gofmt`, `go vet`, `go mod tidy` (no diff), a build of every release target, and `go test ./...` on
ubuntu, macOS and Windows. Windows really runs the tests, so write tests that also work there (avoid unix-only paths
outside `links_unix_test.go`). We assume that only Windows may lack the right to create symbolic links, so create them
with `symlinkOrSkip` (skips on Windows only, fails elsewhere) or, for an optional extra check, `trySymlink` (returns
false on Windows only, fails elsewhere), and never call `t.Skip` on an `os.Symlink` or `Mkfifo` error yourself. A Unix
environment that forbids links or FIFOs therefore fails these tests by design, and a Windows runner without the right
does not verify the symlink-refusal paths (nor the optional link check in the `readEnvIfRegular` test).

## Conventions

- Commit subjects start with `feat:`, `fix:`, `refactor:`, `docs:`, `ci:`, `build:`, `chore:` or `test:`. The release
  notes are grouped by these prefixes (`test:` and merge commits are left out). Use `!` for a breaking change, e.g.
  `feat!:`.
- Work on a branch and open a pull request against `main`; merge with a merge commit. Wait for CI to pass.
- Comments explain *why*, not what. Keep them as short as the surrounding code does.
- Keep dependencies at zero unless there is a strong reason.
- Do not commit build output (`/sail-worktree`, `/dist/`).

## Design rules that are easy to break

These come from deliberate decisions; change them only on purpose and update the tests and both READMEs.

- **`.env` and `.sail-worktree.json` are never written through a link.** `up` and `rm` refuse a symlinked or
  hard-linked `.env` (`checkOwnEnv`). Both files are written by `writeFileNoFollow`: `Lstat` (the only link check on
  Windows), open with `O_NOFOLLOW|O_NONBLOCK`, check that the opened file is regular and (Unix) not hard-linked, then
  truncate. Its refusals wrap `errNotOwnFile`, so `init` adds advice only to them. A new `.env` is created with mode
  0600 and a new `.sail-worktree.json` with 0644; an existing file keeps its mode. Duplicate keys that `up` writes are
  collapsed to the first line. Reading is asymmetric on purpose: `up`, `stop` and `rm` read a symlinked
  `.sail-worktree.json`; only writing refuses links, so do not "unify" the two.
- **Override keys differ per command.** `up` refuses `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` in `.env`
  (compose would not read the ports and project name `up` writes). `COMPOSE_PROFILES` is allowed. `rm` also refuses
  `COMPOSE_PROFILES` because it cannot be undone. `stop` refuses nothing and only warns (best effort, non-main
  worktrees, regular files only).
- **`rm` is guarded.** It recomputes the project name and requires `COMPOSE_PROJECT_NAME` in `.env` to match exactly.
  Every refusing check runs *before* the confirmation prompt. It pins `--project-name`, `--project-directory` and `-f`,
  and runs without `COMPOSE_*` from the environment. Values from `.env` are untrusted: they are shown with `%+q`, and
  only names matching `safeProjectName` are put into a suggested shell command. The compose file is resolved through its
  links (`composeInsideProject`) and refused unless its real path is a regular file inside the project directory; `-f`
  gets that real path, so do not "simplify" it back to `Join(root, compose)`. Only `rm` has this check: `up`, `stop` and
  `init` do not run docker with `-f` (Sail finds the file itself), so their `os.Stat` and read of the compose file (to
  find the project and detect port variables) are not a regression. Limits: Windows junctions are not followed (Go 1.23+
  `EvalSymlinks`), a link swapped after the check is not caught, and what the compose file refers to is not checked
  (relative `include:` and `extends:` paths of a file reached through a link are resolved from its target's directory).
  A compose file that is a hard link to a file outside the project directory is not detected, by choice: a path check
  cannot see one, only the link count could, and compose files are sometimes shared that way. A plain checkout cannot
  create one because git stores no hard links, but a script or the user can. Hard-link detection exists only for the
  files this tool writes (`.env`, `.sail-worktree.json`), and only on Unix.
- **`ps`, `ports` and `status` only read.** They never `save` or `migrate` the registry (`foldRegistry` folds symlink
  aliases in a copy), never write `.env` or `.sail-worktree.json`, and never source `.env`. Registry keys are untrusted:
  a key that fails `validKey` (absolute and clean, and on Windows a drive-letter path, an allow-list, because a UNC or
  device path such as `\\host\share`, `\\?\` or `\??\` can open a network connection) is never used as a directory, by
  any command (`migrate` in `up` and `rm` included). On Windows `validKey` also walks the key from its drive root with
  `Lstat` and `Readlink` (`linksStayLocal`, through the replaceable `keyLinksOK`) and refuses it when a symbolic link or
  junction on the way, or in what it leads to, has a target that is not a drive-letter path, because Windows follows a
  link in the middle of a path whenever the path is used (`realPath`, `isDir`, git); a junction is checked although
  `EvalSymlinks` does not follow it, a `ModeDir|ModeIrregular` reparse point (one that does not redirect, such as a
  cloud placeholder) is walked like a directory, and an element that cannot be looked at, a link that cannot be read, or
  a `..` after a name in a link target refuses the key (a target is walked element by element like `EvalSymlinks` does,
  never cleaned first, so no link in it is skipped). The walk may run several times for one key in a command. Limits: a
  link swapped after the walk is not caught, a drive letter mapped to a network share is the user's own setup, and a
  folder where a volume without a drive letter is mounted (`\\?\Volume{...}`) is refused as well. On Windows git never
  runs in the registry directories of other repositories (`gitInRegistryDirs` is off), because git itself looks at paths
  from that repository's metadata (`gitdir` targets, a `.git` file's `gitdir:`), and a UNC or device path there can open
  a network connection; those entries are `unattributed`. Elsewhere git runs there only through `listWorktrees`
  (`worktree list` with `core.fsmonitor=false`, `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and `GIT_COMMON_DIR`
  removed); the worktree paths such a listing prints come from that repository's metadata (git itself still reads them),
  so `parseWorktreeList` drops a record whose path fails `registryPathOK` (`validKey`) before anything looks at it (a
  refused main record drops the listing). The listing of the repository the user runs in passes `anyPath`. External
  queries go through `runOutput` (explicit env, timeout, no stdin, stdout capped while read). docker is asked once
  (`dockerProjects`, through the replaceable `output`, with `cleanEnv(nil)` in the system temp directory); a failure is
  the state `unknown`, never an error, and a test that does not fake `output` must pass `--no-docker`. Every untrusted
  string is printed through `cell`/`escapeControl`. The JSON field names of `ps` are a public contract (`--json` goes
  through `jsonEscape`). Output goes to the package `stdout`, never `fmt.Println`. Main worktree records (`mains`) are
  shown too (`kind` `main`, before the worktree rows; `mainEntries`) with their recorded `Ports`, never a read of the
  main's `.env`; a main key is used as a directory (`isDir`, git) only after `validKey`, docker is never asked about it
  (its compose project name depends on that `.env`, so `name` is empty and `state` is `-`, `stale` or `unattributed`),
  and `status` counts every main record as another holder (`otherHolders`), as `used` does. The `kind` field was added
  to both JSON outputs; existing fields keep their names and meanings. `status` reuses `checkOwnEnv`, `upOverrideKeys`,
  `projectName` and `addStatefulDomain` (on a copy of `.env`) instead of re-implementing them, prints only an allow-list
  of facts (never other `.env` values, and not even an invalid port value, because a port variable name may be any key;
  `APP_URL` without credentials, query and fragment), never reads a symlinked `.env`, and exits 1 after the report when
  it found a problem (warnings do not). A port variable that the worktree's `.env` sets on more than one line is a
  warning (`statusDuplicates`, through `duplicateKeys`; the name, never a value; none when the configuration is broken,
  as there are no names then; the multi-line limit of the `check` bullet applies).
- **`check` writes only the registry.** It records the current repository's main worktree, if it runs in its project
  directory or below it (`currentMain`, `checkRecord`; the `loadCtx` lookup, as for `up`; elsewhere a warning says where
  to run it), refreshes every recorded main from its `.env` (`refreshMains`, also outside a repository) and then reports
  conflicts over the whole registry. The refresh exists because `up` avoids only recorded ports: without it, a suggested
  `up` could keep a port that a main now uses. It never writes `.env` or `.sail-worktree.json`, never sources `.env`,
  and runs neither sail nor docker (git only through `loadCtx` in the cwd). No `.env` is read under the lock, so a slow
  drive does not keep `up` and `rm` waiting: `readMains` reads the recorded mains' `.env` first (from a registry read
  without the lock, used only for the keys and the records a refresh is based on), then `mainPorts` the current main's.
  Under `lockRegistry` the registry is read again; `recordMainRead` records the current main and `refreshMains`
  refreshes the others by one rule: a pre-read result is applied only to a record whose `ports` and `defaults` are still
  the ones it was based on (`sameBase`; a record changed meanwhile, or one that appeared, is newer and the next `check`
  refreshes it; the current main's worktree is still added), except that a current main whose record is gone (pruned
  meanwhile) is recorded from the read. The base of the current main is taken from the registry read before its `.env`,
  so a newer record never passes for it. The registry is saved only when `recordMainRead` or `refreshMains` reports a
  change. A `loadCtx` or configuration error is a warning, so `check` works outside a repository. So is a lock that
  another run holds for longer than the wait (`errLockHeld`; any other lock error, such as no permission or a read-only
  file system, is an error): nothing is recorded or refreshed, the registry is read without the lock (safe, it is
  replaced atomically) and reported, and the warning says the report may rest on records that refreshing would have
  changed; a broken registry stays an error. A `mains` key is used as a directory only after `validKey` (see the
  read-only rule above) and `isDir` (`Lstat` on the key; a link as its last element is not followed), its `.env` only
  through `readMainEnv`. Conflict lines print only valid port numbers. Records of missing directories or invalid keys,
  and main records that another record replaced (`replacedMain`, see the `prune` rule), are warnings (their recorded
  ports printed as they are), never conflicts, so a deleted clone or a moved project cannot make `check` fail for ever.
  An entry of a holder main's `worktrees` list that fails `usableDir` (prune's test) is a warning too, although it holds
  no ports, so that `prune` removes only what `check` warns about. A conflict is fixed by the user: a worktree by `up`,
  a main worktree by hand (`suggestion` only prints a value; `VAR=N` is shown only for a name matching `shellName`).
  Exit 1 only for a conflict (`report.finish`, shared with `status`) or an error; an error still prints the warnings
  collected so far (`printWarnings`). A variable of a record's `defaults` that the main's `.env` sets on more than one
  line is a warning (`warnDuplicates`, from the `.env` that `readMains` already read, so a main recorded by this run is
  checked from the next one; the name through `cell`, never a value). `duplicateKeys` reads line by line, as `portValue`
  does, so a line inside a multi-line quoted value counts: a documented limit, because only the warning could skip such
  lines (it would then disagree with the port read) and a quote-aware `Set` would lose every key after an unclosed
  quote.
- **`prune` writes only the registry.** It removes what `check` warns about: a `worktrees` key that fails `validKey` or
  does not resolve to a directory (`staleWorktreeKey`, the `foldRegistry` rule, so an alias key of a live worktree is
  kept: it still holds that worktree's ports, and the next `up` there folds it with `migrate`, not `prune`), a `mains`
  key that fails `usableDir` (`validKey`, then `isDir`; links are not followed, mains are recorded as real paths), a
  `mains` key that another record replaced (`replacedMain`, only for a key that passes `usableDir`; main keys are never
  folded, so without it a record that a move left behind would conflict with the new one for ever: an alias whose real
  path is a usable key of its own, or a directory without project markers (`projectMarkers`: `.sail-worktree.json`, or
  `artisan` and a compose file, looked at with `Lstat` only, a link counts, an error other than not-exist counts as
  present) while a usable key that is its own real path and has markers lies strictly below it; a `realPath` failure
  keeps the record, and a main without such a second record keeps its ports; git is not asked), and entries of a kept
  main's `worktrees` list that fail `usableDir`. `validKey` always comes first, so an invalid key is never looked at (on
  Windows its walk reads only the elements and links on the way, up to the first refused link). It lists and asks
  (package `stdin`/`stdout`, `-y` skips) without the lock, then takes `lockRegistry`, reads the registry again and
  removes only listed records that are still recorded and still unusable (with the same per-kind test), saving only when
  something was removed; records that became unusable after the prompt are left for the next run, because the user did
  not confirm them. A replaced main is tested again after the prompt and before the lock (`recheckReplaced`, so that the
  lock is not held while it reads the filesystem) and under the lock only compared in the registry (`stillPrunable`: its
  record equals the listed one, and the record that replaces it is still there). It never writes `.env` or
  `.sail-worktree.json` and runs neither docker, sail nor git: the containers, networks and volumes of a removed
  worktree are left alone and the output says so (`docker compose ls -a`). Untrusted keys are printed with `%q` and port
  names through `cell`. A directory that is only out of reach for the moment counts as missing. So removing the record
  of a project that is still in use (one on a disk that is out of reach for the moment, on Windows one run from a UNC
  path or through a link to one, which is not a usable key, or a main whose checkout has no project markers at the
  moment while a main record with markers lies below it: a branch without the project there, or a feature branch that
  moved the project from `laravel/` to the root, whose `up` records the root as the counterpart) frees its ports in the
  registry until `up`, or for a main worktree also `check` in its project directory, records it again; a port bound at
  that moment is still skipped by `up`'s `portFree`. The prompt lists the records first.
- **Main worktree records (`mains`).** The registry's `mains` map is keyed by the main worktree's project directory
  (`ctx.main`) and holds `ports`, `defaults` (the compose defaults, used when `.env` drops a variable) and `worktrees`
  (linked worktrees that have a `worktrees` record). It is a cache: an older version drops it on save, and `up` and
  `check` record it again. Ports follow one rule (`portsFromEnv`): a valid value in the main's `.env`, the default when
  the key is absent or empty or there is no `.env`, nothing for an invalid value. The value is read with `portValue`
  (quotes, inline comments, the last line of a duplicated key), as compose and Sail's bash do; `Get` keeps comments and
  reads the first line, because `up` writes its result back and collapses duplicates to the first line. A default counts
  only if it is 1 to 65535 (`validPort`), so a hand-edited recorded default outside that reserves nothing (`loadConfig`
  already refuses one in the configuration; the check stays for the registry). A main directory that does not exist is
  never recorded (`counterpart` may return a lexical path). A `.env` that is a link or not a regular file is not read
  for the record, although `up` copies `.env` through a link: the record keeps its ports, and a new record reserves
  nothing. The variables are those of the run's configuration (`cfg.PortVars`), which may be a branch's, so `recordMain`
  merges: a variable of the old record's `defaults` that the run does not know keeps its recorded default and port. Only
  a `check` run in the main worktree itself (`checkTarget.inMain`) replaces the record, so a variable that left the
  project is dropped only there (or by hand); until then it keeps its port reserved, and a variable that only a branch
  has is dropped there and comes back with that branch's next `up` or `check`. A kept value is not refreshed by `up`
  (which reads only the run's variables) or by a `check` in the project directory of a linked worktree of the same
  repository (which records its main from that read and drops its pre-lock read of the current main), however often they
  run; a `check` in the main worktree re-records or drops it, and a `check` anywhere else (`refreshMains`) re-reads it.
  An unreadable `.env`, or a record that another run changed while this run waited for the lock, keeps the whole record,
  `inMain` included. `MainRecord` is a value type so that a hand-written `null` cannot cause a nil dereference. Alias
  paths in a record's `worktrees` list are folded like the top-level `worktrees` keys: by `migrate` (saved by `up` and
  `rm`; only in the current main's list, so an alias in another main's list is not folded by it), and by `check` in
  memory only (`foldKey`, the key `ofMain` looks a worktree up by); an entry that fails `validKey` is never resolved.
- **`ports` examines the host.** It probes each distinct port once through `probePort` (a bind that is closed at once,
  so nothing is written; a running project's own ports are not probed) and asks `lsof` (Unix only, through `output`,
  fixed arguments, `lookupOwner` in tests) only about ports that cannot be bound. A process name is whatever the process
  called itself: it is printed through `cell`/`jsonEscape`. The port argument is digits only, 1 to 65535. Main worktree
  rows are probed like any row that is not `running`, and count in `CONFLICT` (`portsOf`), so `ports --all` agrees with
  `check` on conflicts, except that stale or unattributed rows, and main records that `check` finds replaced
  (`replacedMain`; `status` counts them as other holders too), count here and only give a warning in `check`. The JSON
  fields of `ports`, `process` and `pid` included, are a public contract too.
  A recorded value that fails `validPort` (a hand-edited registry) is `host` `invalid` (`hostInvalid`, decided before
  `running` and the probe cache): never probed (binding 0 picks a random port) nor given to `lsof`, and left out of
  `CONFLICT`, as `check` ignores it. `invalid` is a value added to `host`; the existing values keep their meanings.
- **`ps --all` attribution.** With `--all` an entry is attributed to the longest listed worktree that contains it, so an
  independent repository nested inside a listed worktree is attributed to the outer one when that one is already known.
  A main record is attributed only to the main (first) worktree of a listing (`attributeMain`). On Windows only the
  current repository's worktrees are listed (`gitInRegistryDirs`). One command runs git at most once in a directory, a
  failure included, and adds a repository's listing once (`registryView.list`), since each run may take its timeout.
- **`cleanEnv`** removes every `COMPOSE_*` variable, `SAIL_FILES` and the port variables (names compared
  case-insensitively) from the environment passed to `sail` and `docker`. It must never return `nil`: a `nil`
  `exec.Cmd.Env` inherits the whole parent environment. A port variable named `COMPOSE_X_PORT` (allowed, as it is no
  compose setting) is removed on both counts and reaches compose only through the `.env` that `up` writes, as every
  port variable does; a `DOCKER_X_PORT` is removed as a port variable, while `DOCKER_HOST` and the like are kept.
- **Paths are real paths.** The worktree root, the main worktree and the project directory are resolved through
  symlinks, because the project name contains a hash of the project directory. `Registry.migrate` merges keys an older
  version recorded under a symlinked path, and replaces aliases of root in the current main's `worktrees` list (the only
  one root can be listed in; other lists are not resolved under the lock; a changed list is sorted); it only changes
  memory and the caller saves on success. A registry key is resolved only after `validKey` (on
  Windows that includes the link walk).
- **Project directory.** `up`, `stop` and `rm` (`lookupProject`) take the nearest directory with `.sail-worktree.json`
  anywhere up to the worktree root; only when there is none, the nearest directory where `isLaravelProject` holds
  (`artisan` is a regular file and a compose file exists), and then the configuration is detected (`ctx.detected`).
  Keep the file first: it keeps every existing setup on the same directory. `init` takes the nearest directory of either
  kind (`configOrLaravel`), so running it never moves the project somewhere the other commands would not look.
  `ctx.root`/`ctx.main` are the project directories, `ctx.wtTop`/`ctx.mainTop` the worktree roots; the registry keys are
  project directories (the JSON name `worktrees` stays). Rules:
  - The cwd below the root comes from the same git call as the root (`git rev-parse --show-toplevel --show-prefix`),
    parsed by the pure `parseTopAndPrefix`: exactly two lines, an absolute root, `\r` removed only on Windows. Do not
    walk `os.Getwd` instead: it keeps the case the user typed (macOS) and short names (Windows), which would change
    the hash.
  - `projectCandidates` never looks above the root, refuses empty, `.` and `..` elements, checks every candidate with
    `within` before anything is `Stat`ed, and skips candidates below a `vendor` or `node_modules` element of the prefix
    (not of the root itself). The chosen directory goes through `realPath` and `within` again.
  - The first marker name that exists decides; one that is not a regular file is an error, never a reason to fall
    back to a parent.
  - The project name is `projectName(mainTop, wtTop, root)`: the slug comes from the two worktree names and the hash
    from the project directory, so a project at the root keeps the name older versions wrote (a test pins the old
    formula). Only a slug that is empty after trimming becomes `fallbackSlug` (`wt`), because compose refuses a name
    that does not start with `[a-z0-9]`. Production code calls `ctx.projectName()`.
  - `isMain` decides on the worktree roots, with the project directories compared as well (defense in depth).
  - A bare first entry of `git worktree list` (`ctx.bare`) means there is no main worktree checkout: `mainTop` is the
    bare directory (it still gives the project name's slug), `ctx.main` is empty and `counterpart` is not called,
    `isMain` is false in every worktree, `up` and `check` record no main, and `up` copies `.env` from the worktree's own
    `.env.example` (`readOwnEnvExample`: a regular file only, since it comes from the branch checked out there).
  - The main worktree's counterpart (`counterpart`) is the same relative path there. It may not exist, but it must not
    resolve outside the main worktree, because `up` copies `.env` from it and Sail sources it. Only the directory is
    contained: the `.env` file in it may still be a link, as for a root project. A missing path is checked only
    lexically, so a broken link in the middle passes, but nothing can be read through it. Windows junctions are not
    followed by `EvalSymlinks`, here as for the compose check.
  - `isLaravelProject` checks `artisan` before the compose file, so a broken compose file in a directory without
    `artisan` (`docker/`, `.devcontainer/`) never stops the walk; an `artisan` that is not a regular file means "not a
    project".
  - The hints in the not-found error enter only plain directories (no links, no junctions) and skip `vendor`,
    `node_modules` and `.git`. The hints and the warning about a nearer Laravel project ignored for a file further up
    are best effort: their errors never fail a command.
  - Detection (`detectConfig`, `detectPortVars`) takes only host port mappings (`${X_PORT:-n}:`) with a default between
    1 and 65535, because `up` rewrites every variable it detects; a usual `environment:` entry such as
    `DB_PORT=${DB_PORT:-3306}` must not match. It is a line heuristic, not a YAML parser: only `- ...` list lines are
    read. The compose file and `.sail-worktree.json` are read with `readSmallFile` (non-blocking open, regular file, at
    most 1 MiB, an error rather than a cut).
  - A configuration error is kept in `ctx.cfgErr` and returned by `ctx.config()` after the main-worktree refusal; no
    command reads `ctx.cfg` directly. `stop` fails on it too, as it did with a broken file. The exception: `stop` and
    `rm` take a reserved name found by detection (`reservedPortVarError`, through `ctx.teardownConfig()`): they write no
    port variable, and detection yields only `*PORT` names, so the name cannot be `DOCKER_HOST` or `PATH`. Only
    `SAIL_SHARE_SERVER_PORT` (read by Sail's `share`) ends in `PORT` (a test pins it): without `.sail-worktree.json`, a
    compose file that maps it stops `up` and `init`, `status` reports a configuration problem and `check` warns and
    records no main, while `stop` and `rm` run (`stop` drops it from sail's environment; `rm` keeps a shell value, with
    which compose only expands the compose file, and `down -v` picks what it removes by the pinned project name). Port
    variables are required only for `up` and only when detected (a file with `port_vars: []` still works).
  - `up`'s order: main refusal, configuration, detected port variables, the existing `.env` checks and port
    allocation, then `sailPath` right before the first write, so nothing is written without Sail and the order of the
    existing refusals stays the same.
  - `rm` passes only the one compose file to `-f`, so `compose.override.yaml` is not read (volumes defined only there
    are left behind). A broken link named `compose.yaml` is skipped by `findMarker`, so a `docker-compose.yml` next to
    it is used.
- **Error output.** `main` prints the final error through `printErr`, which escapes what `strconv.IsPrint` rejects and
  invalid UTF-8 bytes; `\n` is kept for the layout (a newline inside a path can still start a line of its own).
  New messages with paths use `%q`, so printable non-ASCII stays readable; values from `.env` and the existing compose
  errors (`composeInsideProject`) keep `%+q`, so do not "unify" them.
- **The registry is written atomically.** `Registry.save` goes through `writeFileAtomic`: a temporary file next to the
  target (same filesystem), `Sync`, then `rename` over it, so a reader (`ps`, `ports`, `status` take no lock) never
  sees a partial file. A symbolic link at the path is resolved first, a dangling one too (the link stays, what it
  points at is replaced or created), a hard link is broken, the mode of an existing file is kept and a new file is
  0644 whatever the umask. On Windows a replace fails while a reader has the file open, so the rename is retried
  (`renameRetries`). A process killed between the temporary file and the rename leaves a harmless `.registry-*.tmp`,
  and the directory is not synced (atomicity, not durability). Do not go back to `os.WriteFile`: its
  truncate-then-write window is what a race smoke test covers. This is the registry only; `.env` and
  `.sail-worktree.json` keep `writeFileNoFollow`.
- **Registry handling in `rm`.** Read the registry once before the prompt (to fail early on a broken file) and again
  after `docker` (so an update made meanwhile by another `up` is not lost); release the worktree only after docker
  succeeds. The lock is taken after docker and before the second read (never while docker runs); if it cannot be taken
  the error says that docker finished and that rm can be run again (`down` is idempotent). The main worktree's record
  stays; only the worktree leaves its `worktrees` list (`forgetWorktree`).
- **The registry lock.** `up`, `rm`, `check` and `prune` update the registry under `lockRegistry` (`registry.json.lock`
  next to it, `O_EXCL`, mode 0600, pid inside; the file is closed at once because Windows cannot remove an open file).
  `up` reads the main's `.env` (`mainPorts`) before it, after a registry read that is used only as the base of that
  main's record (`recordMainRead`, the rule of `check`; the ports it read are also avoided by this allocation, since a
  record kept as newer may come from an older read), and takes it after the `.env` checks and before `loadRegistry`, so
  reading, `allocatePorts`, writing `.env` and `save` are one step, and releases it right after `save` and before
  `runSail`; `rm` takes it after docker; `check`
  reads the mains' `.env` before it and holds it to read the registry again, record, refresh and save (what it read
  before locking only decides which records may be refreshed; the registry it saves is the one read after locking). If
  `check` times out on a lock that another run holds (`errLockHeld`), it warns and reports on the registry read without
  it, writing nothing; any other lock error fails it (on Windows also an error other than "exists" left at the
  deadline). `prune` takes it only after its prompt. Never hold it while sail or docker runs, and read the registry
  after locking. `ps`, `ports` and `status` never lock (the atomic save keeps them consistent). A lock whose mtime is
  older than `lockTiming.stale` or more than 5 seconds in the future is abandoned. A path has no compare-and-delete, so
  every run that removes a lock holds that lock's claim, `registry.json.lock.claim-<mtime in ns>` (`claimPath`,
  `O_EXCL`): `claimStale` takes it, checks that the path still holds the file that was judged (same file and same mtime,
  since an inode can be reused) and removes it; `unlock` (`releaser`) is idempotent and removes only the file it
  created, under its claim, and leaves its lock to a run that holds the claim already. A fresh lock is therefore never
  removed or moved (no rename, no `os.Link`). A run killed while it holds a claim leaves the claim file, and that one
  lock is then not broken automatically (the error's hand recovery still works); nothing removes such a leftover. A lock
  whose write or close fails after a successful `Stat` is released through `releaser` as well, never removed by path
  (this run may have been suspended for longer than `stale` and its path taken by another run); if `Stat` failed, the
  file has no identity and is left to the stale rule or the hand recovery. The claims protect only runs of versions that
  use them: v0.8.0 and v0.8.1 break a stale lock by renaming it and remove locks by path, and earlier versions take no
  lock, so running one of them at the same time as this version reopens the window claims close (a fresh lock removed,
  two runs updating the registry at once); the README says to use one version at a time. `wait` is longer than `stale`.
  Only on Windows are errors other than "exists" retried (a pending removal looks like access denied), always after
  `poll`, since only a broken stale lock is retried at once; elsewhere they fail at once. The pid in the error comes
  through `readSmallFile` and is shown only if it is a number. An `O_EXCL` file was chosen over flock/LockFileEx (no
  per-OS code, no dependency); its cost is the stale rule and the hand recovery in the error message, and a run
  suspended for longer than `stale` can lose its lock to the next one. Do not "improve" this into a lock held during
  `sail up`.
- **Ports.** A new port is searched from default+1 upwards (the default is left to the main worktree), skipping ports
  of other worktrees, ports recorded for any main worktree (`used` includes `mains`, so `up` moves a worktree off a
  main's port and says so) and ports in use. A port counts as free only if it binds on all interfaces and on `127.0.0.1`;
  `loopbackBindBlocked` decides how a failure on `127.0.0.1` is treated per OS. A current assignment is kept only if
  `validPort` holds; otherwise a new port is searched, so a hand-edited out-of-range value is not written back to `.env`.
- **Sail sources `.env`.** `vendor/bin/sail` runs `source ./.env` (or `.env.$APP_ENV`), so `.env` is executed as shell
  code. `stop` does not refuse a symlinked `.env` for this reason: calling `sail` directly has the same exposure.
- **`SANCTUM_STATEFUL_DOMAINS`.** Only when `up` rewrites `APP_URL` and `.env` sets the key, `addStatefulDomain`
  appends the new `APP_URL`'s entry (lower-case host, `:port` unless it is the scheme's default; IPv6 canonical with
  brackets). It follows Sanctum's `fromFrontend`: an element equal (before trimming) to
  `__SANCTUM_CURRENT_REQUEST_HOST__` is `getHttpHost()`, which is the entry itself as long as the browser reaches the
  worktree at `APP_URL` with an unchanged Host header (Sail's normal setup; not behind a proxy that rewrites it);
  otherwise `strIs(trim(e)+"/*", entry+"/")`. It only ever appends. An absent key, Laravel's disabling words and a value
  of only commas are left alone; the words are compared lower-cased after `sanctumUnquote`, which trims spaces outside
  quotes (as phpdotenv does) but not inside them, so `" null "` is a plain string. Because Sail sources `.env`, the
  entry and the value must pass allow-lists, and anything else is left alone with a warning naming the entry to add (it
  repeats on every `up` until, in a value that is quoted or has no space, tab, `#` or quote, an element (trimmed like
  PHP's `trim`) equals the entry); do not loosen them to "escape" values instead. `Get` keeps stripping any quotes at
  both ends. `Set` collapses duplicate lines of the key and drops an `export` prefix, as for every key `up` writes. The
  added line and the warning are printed after `.env` and the registry are saved. Known limit (existing): `up` writes
  `APP_URL` back unquoted, so shell metacharacters in it that quotes protected (such as `&`, `;`, `#`, `$`, `(`, `|` or
  a space in the query) are no longer protected.
- `compose` in `.sail-worktree.json` must be a relative path inside the project directory (`unsafeComposePath`),
  including on Windows forms such as `C:x`, `\\srv\x` and `/x`. That check only reads the string; `rm` also checks the
  real path with `composeInsideProject` (see "`rm` is guarded"). Each `port_vars` entry must have a `shellName` name and
  a `validPort` default (`validatePortVars`), because `up` writes `NAME=port` into `.env`; the names are not held to the
  detection rule, so a hand-added variable may use any name compose does. A name in `reservedNames` (keys `up`
  writes, the `COMPOSE_*` and `DOCKER_*` settings docker compose and the docker CLI document, the BuildKit and buildx
  settings, the proxy names and `SSH_AUTH_SOCK`, the settings `bin/sail` reads, sources in the comment,
  `SAIL_FILES`, a few shell names; case-insensitive, as `cleanEnv` drops them) is refused, by `detectConfig`
  too, so `init` never writes a file that `loadConfig` refuses (only `SAIL_SHARE_SERVER_PORT` ends in `PORT`, so
  detection finds no other; see the configuration error rule above). Other `COMPOSE_*` and `DOCKER_*` names, such as
  `DOCKER_WEB_PORT`, are allowed on purpose: v0.8.1 handled them (#74). Keep the shell part short, add a key there when
  `up` starts to write it, and add a compose, docker, build or Sail setting when they start to read one; check the
  sources in the comment again before each release. A repeated name (also compared
  case-insensitively) is refused, because `up` would assign two ports and `Set` keeps only the first line;
  `detectPortVars` yields each name once (`composePortRe` matches upper-case names only), and a test pins that.

## Testing notes

- `setupWorktreeRepo` creates a real main worktree and a linked worktree in a temp dir, points `HOME`/`XDG_CONFIG_HOME`
  at temp dirs so the real registry is never touched, and `chdir`s into the linked worktree. `setupSubdirWorktreeRepo`
  does the same with the project in `laravel/` (optionally only on the feature branch) and `chdir`s into `wt/laravel`;
  `setupDetectedRepo` creates a project without `.sail-worktree.json` (`artisan` and a compose file) at a given path and
  `chdir`s into it in the linked worktree; `initRepo` is a single repository for `init`.
- Tests replace package variables (`runner`, `stdin`, `stdout`, `stderr`) through helpers such as `captureRunner`,
  `captureStdout` and `captureStderr`; restore them with `t.Cleanup`. These tests use `t.Setenv`/`t.Chdir`, so they
  cannot run in parallel.
- A refusal that must happen before the prompt is tested by checking that stdin was not consumed and no command ran.
- Compare messages that quote a path with `fmt.Sprintf("%q", path)` or `strconv.Quote`, never with a hand-written
  backslash string, so the same test passes on Windows.
- `readEnvIfRegular`'s defense against a path swapped for a FIFO between `Lstat` and `open` cannot be tested
  deterministically; the FIFO test only covers the `Lstat` stage.

## CI and release

- `ci.yml` runs on pushes to `main` and on pull requests.
- `release.yml` runs when a tag matching `v*` is pushed: it runs the tests, runs GoReleaser (darwin, linux and windows
  on amd64 and arm64; zip for Windows, tar.gz otherwise; `checksums.txt`), then a `verify` job downloads the
  linux/amd64 archive, checks its checksum and checks that `sail-worktree version` prints the tag. The version comes
  from the build info, so build from a tagged, clean checkout.
- Actions are pinned to full commit SHAs with the version in a comment, and GoReleaser is pinned to an exact version.
  Dependabot opens weekly PRs for the actions. Do not switch back to floating tags.
- To release: check the sources in the `reservedNames` comment for new settings (see the `port_vars` rule), merge to
  `main`, create an annotated tag (`git tag -a vX.Y.Z -m vX.Y.Z`) on the merge commit, push the tag, and check that the
  Release workflow and the `verify` job pass. Use a minor bump for user-visible or breaking changes while the version is
  below 1.0.0.
- Do not move or re-push a published tag: the Go module proxy caches tags. Publish a new version instead.
- `GoReleaser` builds the release notes from commit subjects, so write subjects that read well to a user.

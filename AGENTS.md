# AGENTS.md

Guidance for AI coding agents (and humans) working on this repository.

## What this project is

`sail-worktree` is a small Go CLI (standard library only, no dependencies) that lets several git worktrees of a
Laravel Sail project run side by side. For each worktree it creates or updates `.env` and assigns ports that do not
collide with other worktrees or with the host.

Commands: `init`, `up [args...]`, `stop`, `rm [-y]`, `ps`, `version`. See `README.md` for the user-facing behavior.

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
| `main.go` | Command dispatch, usage text, `version`, `printErr`/`escapeControl` |
| `cmd.go` | `init`, `up`, `stop`, `rm`, error messages, `sailPath`, `cleanEnv`/`filterEnv`, `runOutput`/`output` (read-only queries) |
| `env.go` | `.env` parsing and writing (`Raw`, `Get`, `Set`), port variable detection, override keys, `checkOwnEnv`, `readEnvIfRegular`, `writeFileNoFollow` |
| `config.go` | `.sail-worktree.json` (project config), `detectConfig`, `readSmallFile`, the port registry, `unsafeComposePath`, `composeInsideProject`, `Registry.migrate` |
| `ports.go` | Port allocation, `portFree`, `loopbackBindBlocked` |
| `git.go` | Worktree root and the cwd below it (`worktreeRootAndPrefix`, one `rev-parse` call), main worktree detection (real paths) |
| `project.go` | Project directory lookup (`projectCandidates`, `findMarker`, `findProject` with a `matcher`: `hasConfig`, `isLaravelProject`, `configOrLaravel`; `lookupProject`, `within`), the not-found error and its hints, the main worktree's counterpart |
| `ps.go` | `ps` and its registry view: `entry`, `collectEntries`, `foldRegistry` (aliases folded in memory), `listWorktrees`/`parseWorktreeList`, `attribute`, `cell`/`jsonEscape` |
| `status.go` | `status`: `report`, `statusEnv`, `statusPorts`, `statusAppURL`, `probePort` (replaceable port probe) |
| `links_unix.go` / `links_other.go` | Build-tagged helpers (`O_NOFOLLOW`, `O_NONBLOCK`, hard link count) |
| `sanctum.go` | `SANCTUM_STATEFUL_DOMAINS`: `statefulDomain`, `strIs`, `sanctumUnquote`, `statefulDisabled`, `addStatefulDomain` |
| `sail_worktree_test.go`, `project_test.go`, `detect_test.go`, `sanctum_test.go`, `ps_test.go`, `status_test.go`, `links_unix_test.go` | Tests |
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
- **`ps` and `status` only read.** They never `save` or `migrate` the registry (`foldRegistry` folds symlink aliases
  in a copy), never write `.env` or `.sail-worktree.json`, and never source `.env`. Registry keys are untrusted: a key
  that is not an absolute clean path is never used as a directory; git runs in registry directories only through
  `listWorktrees` (`worktree list` with `core.fsmonitor=false`, `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and
  `GIT_COMMON_DIR` removed). External queries go through `runOutput` (explicit env, timeout, no stdin, stdout capped
  while read). docker is asked once (`dockerProjects`, through the replaceable `output`, with `cleanEnv(nil)` in the
  system temp directory); a failure is the state `unknown`, never an error, and a test that does not fake `output`
  must pass `--no-docker`. Every untrusted string is printed through `cell`/`escapeControl`. The JSON field names of
  `ps` are a public contract (`--json` goes through `jsonEscape`). Output goes to the package `stdout`, never
  `fmt.Println`. `status` reuses `checkOwnEnv`, `upOverrideKeys`, `projectName` and `addStatefulDomain` (on a copy of
  `.env`) instead of re-implementing them, prints only an allow-list of facts (never other `.env` values; `APP_URL`
  without credentials), never reads a symlinked `.env`, and exits 1 after the report when it found a problem (warnings
  do not). Known limit: with `--all` an entry is attributed to the longest listed worktree that contains it, so an
  independent repository nested inside a listed worktree is attributed to the outer one when that one is already
  known.
- **`cleanEnv`** removes every `COMPOSE_*` variable, `SAIL_FILES` and the port variables (names compared
  case-insensitively) from the environment passed to `sail` and `docker`. It must never return `nil`: a `nil`
  `exec.Cmd.Env` inherits the whole parent environment.
- **Paths are real paths.** The worktree root, the main worktree and the project directory are resolved through
  symlinks, because the project name contains a hash of the project directory. `Registry.migrate` merges keys an older
  version recorded under a symlinked path; it only changes memory and the caller saves on success.
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
    formula). Production code calls `ctx.projectName()`.
  - `isMain` decides on the worktree roots, with the project directories compared as well (defense in depth).
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
    command reads `ctx.cfg` directly. `stop` fails on it too, as it did with a broken file. Port variables are required
    only for `up` and only when detected (a file with `port_vars: []` still works).
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
- **Registry handling in `rm`.** Read the registry once before the prompt (to fail early on a broken file) and again
  after `docker` (so an update made meanwhile by another `up` is not lost); release the worktree only after docker
  succeeds.
- **Ports.** A new port is searched from default+1 upwards (the default is left to the main worktree), skipping ports
  of other worktrees and ports in use. A port counts as free only if it binds on all interfaces and on `127.0.0.1`;
  `loopbackBindBlocked` decides how a failure on `127.0.0.1` is treated per OS.
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
  real path with `composeInsideProject` (see "`rm` is guarded").

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
- To release: merge to `main`, create an annotated tag (`git tag -a vX.Y.Z -m vX.Y.Z`) on the merge commit, push the
  tag, and check that the Release workflow and the `verify` job pass. Use a minor bump for user-visible or breaking
  changes while the version is below 1.0.0.
- Do not move or re-push a published tag: the Go module proxy caches tags. Publish a new version instead.
- `GoReleaser` builds the release notes from commit subjects, so write subjects that read well to a user.

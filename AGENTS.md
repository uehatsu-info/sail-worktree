# AGENTS.md

Guidance for AI coding agents (and humans) working on this repository.

## What this project is

`sail-worktree` is a small Go CLI (standard library only, no dependencies) that lets several git worktrees of a
Laravel Sail project run side by side. For each worktree it creates or updates `.env` and assigns ports that do not
collide with other worktrees or with the host.

Commands: `init`, `up [args...]`, `stop`, `rm [-y]`, `version`. See `README.md` for the user-facing behavior.

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
| `main.go` | Command dispatch, usage text, `version` |
| `cmd.go` | `init`, `up`, `stop`, `rm`, error messages, `cleanEnv`/`filterEnv` |
| `env.go` | `.env` parsing and writing, port variable detection, override keys, `checkOwnEnv`, `readEnvIfRegular` |
| `config.go` | `.sail-worktree.json` (project config), the port registry, `unsafeComposePath`, `composeInsideWorktree`, `Registry.migrate` |
| `ports.go` | Port allocation, `portFree`, `loopbackBindBlocked` |
| `git.go` | Worktree root and main worktree detection (real paths) |
| `links_unix.go` / `links_other.go` | Build-tagged helpers (`O_NOFOLLOW`, `O_NONBLOCK`, hard link count) |
| `sail_worktree_test.go`, `links_unix_test.go` | Tests |
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
ubuntu, macOS and Windows. Windows really runs the tests, so write tests that also work there (skip when symlinks
cannot be created, avoid unix-only paths outside `links_unix_test.go`).

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

- **`.env` is never written through a link.** `up` and `rm` refuse a symlinked or hard-linked `.env`
  (`checkOwnEnv`). Writing opens with `O_NOFOLLOW`, checks the opened file, then truncates. A new `.env` is created
  with mode 0600; an existing file keeps its mode. Duplicate keys that `up` writes are collapsed to the first line.
- **Override keys differ per command.** `up` refuses `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` in `.env`
  (compose would not read the ports and project name `up` writes). `COMPOSE_PROFILES` is allowed. `rm` also refuses
  `COMPOSE_PROFILES` because it cannot be undone. `stop` refuses nothing and only warns (best effort, non-main
  worktrees, regular files only).
- **`rm` is guarded.** It recomputes the worktree's project name and requires `COMPOSE_PROJECT_NAME` in `.env` to
  match exactly. Every refusing check runs *before* the confirmation prompt. It pins `--project-name`,
  `--project-directory` and `-f`, and runs without `COMPOSE_*` from the environment. Values from `.env` are untrusted:
  they are shown with `%+q`, and only names matching `safeProjectName` are put into a suggested shell command.
  The compose file is resolved through its links (`composeInsideWorktree`) and refused unless its real path is a
  regular file inside the worktree; `-f` gets that real path, so do not "simplify" it back to `Join(root, compose)`.
  Only `rm` has this check: `up`, `stop` and `init` do not run docker with `-f` (Sail finds the file itself), so their
  `os.Stat` is not a regression. Limits: Windows junctions are not followed (Go 1.23+ `EvalSymlinks`), a link swapped
  after the check is not caught, and what the compose file refers to is not checked (relative `include:` and
  `extends:` paths of a file reached through a link are resolved from its target's directory).
- **`cleanEnv`** removes every `COMPOSE_*` variable, `SAIL_FILES` and the port variables (names compared
  case-insensitively) from the environment passed to `sail` and `docker`. It must never return `nil`: a `nil`
  `exec.Cmd.Env` inherits the whole parent environment.
- **Paths are real paths.** The worktree root and the main worktree are resolved through symlinks, because the project
  name contains a hash of the path. `Registry.migrate` merges keys an older version recorded under a symlinked path;
  it only changes memory and the caller saves on success.
- **Registry handling in `rm`.** Read the registry once before the prompt (to fail early on a broken file) and again
  after `docker` (so an update made meanwhile by another `up` is not lost); release the worktree only after docker
  succeeds.
- **Ports.** A new port is searched from default+1 upwards (the default is left to the main worktree), skipping ports
  of other worktrees and ports in use. A port counts as free only if it binds on all interfaces and on `127.0.0.1`;
  `loopbackBindBlocked` decides how a failure on `127.0.0.1` is treated per OS.
- **Sail sources `.env`.** `vendor/bin/sail` runs `source ./.env` (or `.env.$APP_ENV`), so `.env` is executed as shell
  code. `stop` does not refuse a symlinked `.env` for this reason: calling `sail` directly has the same exposure.
- `compose` in `.sail-worktree.json` must be a relative path inside the worktree (`unsafeComposePath`), including on
  Windows forms such as `C:x`, `\\srv\x` and `/x`. That check only reads the string; `rm` also checks the real path
  with `composeInsideWorktree` (see "`rm` is guarded").

## Testing notes

- `setupWorktreeRepo` creates a real main worktree and a linked worktree in a temp dir, points `HOME`/`XDG_CONFIG_HOME`
  at temp dirs so the real registry is never touched, and `chdir`s into the linked worktree.
- Tests replace package variables (`runner`, `stdin`, `stderr`) through helpers such as `captureRunner` and
  `captureStderr`; restore them with `t.Cleanup`. These tests use `t.Setenv`/`t.Chdir`, so they cannot run in parallel.
- A refusal that must happen before the prompt is tested by checking that stdin was not consumed and no command ran.
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

# Review scope

This file is read by the `/gemma` review skills.

- It decides how many reviewers look at a change. **three** means three reviewers, one per lens. **one** means a single reviewer with all three lenses.
- It also lists what a reviewer should read for each area. The design rules live in `AGENTS.md` ("Design rules that are easy to break"); a reviewer reads the bullets named in the table, not the whole file.
- Pull requests target `main`.

How a path is matched:

- A changed path that matches any `three` row makes the whole change tier **three**.
- Anything else is tier **one**.
- If every changed file is Markdown (`*.md`), the change is tier **one**, except for this file (`.claude/review-scope.md`) and a change to the "Design rules that are easy to break" section of `AGENTS.md`, which stay **three**.
- When a change edits this file, decide the tier with both the version on the base branch (`git show origin/main:.claude/review-scope.md`) and the changed version, and take the higher tier. Otherwise a change could remove its own row and pass with one reviewer.
- The implementer may raise a tier-one change to **three** when the change clearly touches the risks below, and must say why. A read-only command (`ps`, `ports`, `status`) that starts to write, lock, source `.env` or run a new external program is such a change.

## Tier three

| Paths | Area | What to read in `AGENTS.md` |
|---|---|---|
| `env.go`, `links_unix.go`, `links_other.go`, `sanctum.go` | Writing `.env` and `.sail-worktree.json` (never through a link), values that Sail sources as shell code | "`.env` and `.sail-worktree.json` are never written through a link", "Sail sources `.env`", "`SANCTUM_STATEFUL_DOMAINS`" |
| `cmd.go` | `up`, `stop`, `rm`, `init`: override keys, the `rm` guards, the order of checks, the environment passed to `sail` and `docker` | "Override keys differ per command", "`rm` is guarded", "Registry handling in `rm`", "`cleanEnv`", "Error output" |
| `config.go`, `lock.go`, `check.go`, `prune.go` | The registry (atomic write, migration, schema, `mains`), the lock, `.sail-worktree.json`, the compose path checks, `check` (records the main worktree), `prune` (removes records) | "The registry is written atomically", "The registry lock", "Registry handling in `rm`", "`compose` in `.sail-worktree.json`", "`check` writes only the registry", "`prune` writes only the registry", "Main worktree records (`mains`)" |
| `git.go`, `project.go` | Worktree root, main worktree, project directory lookup (paths feed the project name hash and decide what `up` and `rm` touch) | "Paths are real paths", "Project directory" |
| `ports.go` | Port allocation and the free-port test | "Ports" |
| `go.mod`, `go.sum`, `.github/workflows/**`, `.github/dependabot.yml`, `.goreleaser.yaml`, `.claude/review-scope.md` | Dependencies, CI, release, and this file | "Build and test", "CI and release" |

## Tier one

Everything else, including the following areas.

| Area | What to read in `AGENTS.md` |
|---|---|
| `ps.go`, `status.go`, `portsview.go` (read-only commands) | "`ps`, `ports` and `status` only read", "`ports` examines the host", "`ps --all` attribution" |
| `main.go` (dispatch, usage, `printErr`) | "Error output" |
| Tests only (`*_test.go`) | "Testing notes", "Build and test" |
| Documents (`README.md`, `README_JA.md`, `AGENTS.md` outside the design rules) | "Language policy" |

## Maintenance

- When a change adds a new `.go` file, the implementer asks whether to add it to the tier-three table. A file that writes files, takes the lock, or runs `sail`, `docker` or `git` belongs there. The implementer does not decide alone.
- When a change makes CI call a script or adds a configuration file that a tool reads, the implementer asks whether to add it to the table.
- Keep this file small. It is read on every review.

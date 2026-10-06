English | [日本語](README_JA.md)

# sail-worktree

A tool for running multiple Laravel Sail projects side by side using `git worktree`.
It generates a `.env` for each worktree and automatically assigns ports so they never collide.

All output of the tool (usage, messages, warnings and errors) is in English. [README_JA.md](README_JA.md) is a Japanese translation of this document.

## Installation

```sh
go install github.com/uehatsu-info/sail-worktree@latest
```

Or clone the repository and build it:

```sh
go build -o sail-worktree .
```

## Usage

### 1. `sail-worktree init`

Run it in the main worktree of your Laravel project.
It detects port variables such as `${APP_PORT:-80}` from the `ports:` section of `compose.yml` and generates `.sail-worktree.json`.
Commit this file so that it is shared by all worktrees.

### 2. `sail-worktree up [args...]`

Run it in an existing worktree.

```sh
git worktree add ../myapp-feature-x feature-x
cd ../myapp-feature-x
composer install
sail-worktree up -d
```

- If `.env` does not exist, it is copied from the main worktree's `.env` (or `.env.example` if that is missing).
- Each port variable gets a free port. The search starts at the default value + 1, leaving the default value to the main worktree.
- Ports already assigned to other worktrees and ports in use on the host are skipped.
- `COMPOSE_PROJECT_NAME` is set, and the port in `APP_URL` is updated.
- `SESSION_COOKIE` is set to `<COMPOSE_PROJECT_NAME>-session` (an existing value is overwritten). Cookies are shared across ports on `localhost`, so without a distinct name the logins of different worktrees interfere with each other.
- Shell variables with the same names as the port variables, every `COMPOSE_*` variable and `SAIL_FILES` are removed from the environment passed to `sail` (also for `stop`) and to `docker compose` in `rm`, because they take precedence over `.env`. `DOCKER_HOST` and `DOCKER_CONTEXT` are kept on purpose.
- Assigned ports are reused on subsequent runs.
- Finally, it runs `vendor/bin/sail up <args>`.

### 3. `sail-worktree stop`

Runs `sail stop`.

`stop` does not refuse anything: it never writes `.env` and stopping is easy to undo. Instead, in a non-main worktree it prints a warning to stderr when `.env` (a regular file only; links and FIFOs are not read) has `COMPOSE_FILE`, `COMPOSE_ENV_FILES` or `SAIL_FILES`, or when its `COMPOSE_PROJECT_NAME` differs from the name recomputed for this worktree, since `sail stop` could then stop another project. The warning is best effort (a shell expression in `.env`, or `.env.$APP_ENV`, is not detected) and is not printed in the main worktree.

### 4. `sail-worktree rm [-y]`

Removes the containers, networks, volumes (including DB data) and built images, and releases the assigned ports.
Equivalent to `docker compose --project-name <name> --project-directory <root> -f <compose> down -v --rmi local --remove-orphans`. Use `-y` to skip the confirmation prompt.
`.env` is not deleted.
Because this is irreversible, `rm` refuses unless `COMPOSE_PROJECT_NAME` in `.env` equals the name recomputed for this worktree (a hand-edited name, a name copied from another worktree, or a moved worktree is refused), and unless `.env` has none of `COMPOSE_FILE`, `COMPOSE_PROFILES`, `COMPOSE_ENV_FILES`, `SAIL_FILES`. (`up` accepts `COMPOSE_PROFILES`, but `rm` does not: delete that line before `rm`.) `rm` also refuses a `compose` file that is not a regular file inside the worktree once its links are followed (for example a link to a file outside the worktree, or a link to a directory outside it). All of these checks run before the confirmation prompt. When the name does not match, the error says how to recover: normally set `COMPOSE_PROJECT_NAME` in `.env` back to the name it shows (without quotes); only to remove a project that an older version created under a different name does it show a manual `docker compose -p <name> down -v ...` command, and only if the name consists of safe characters (lowercase letters, digits, `_`, `-`; a name with capitals may differ from the one compose actually uses). Check with `docker compose ls -a` that the project is this worktree's and not another's before running it. When `up` refuses a `COMPOSE_FILE`-style line, the error tells whether it came from this worktree's `.env`, the main worktree's `.env` or its `.env.example`, and how to fix it.

### 5. `sail-worktree version`

Prints the version (the tag for `go install ...@vX.Y.Z`; a local `go build` prints `(devel)` or a pseudo-version such as `v0.0.0-<date>-<commit>`). Pin a tag in your project instead of `@latest`.

## Notes

- `up` and `rm` cannot be run in the main worktree.
- `.env` safety: `up` and `rm` refuse a `.env` that is a symbolic link or (on Unix) has other hard links (so the main worktree's `.env` is never rewritten through a link), and `up` also refuses `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` (with them, compose would not read the ports and project name `up` writes). `stop` does not check this, but note that Sail `source`s `.env` (or `.env.$APP_ENV`) as shell code, so a symlinked `.env` runs its target as shell code whether you use this tool or call `sail` directly; keep `.env` a real, trusted file. A new `.env` is created with mode 0600; an existing file keeps its mode (run `chmod 600 .env` yourself for one created by an older version). If a key that `up` writes appears on several lines, `up` keeps the first and drops the rest, so duplicate lines cannot disagree (Sail uses the last value, Laravel's Dotenv the first).
- `compose` in `.sail-worktree.json` must be a relative path inside the worktree (`rm` passes it to `-f`). `rm` follows links first: it refuses a file whose real path is outside the worktree or is not a regular file, and it passes the real path to `-f`. Only `rm` is guarded this way, because `up` and `stop` run `sail`, which finds the compose file by itself and does not pass `-f`. Limits: Windows junctions are not followed, so they are not detected; a link swapped after the check is not caught; relative `include:` and `extends:` paths in a compose file reached through a link are resolved from the directory of its target; and what the compose file itself refers to (including a hard link to a file outside the worktree) is not checked. If your `compose` is a link to a file outside the worktree, copy that file into the worktree or point the link at a file inside it.
- A port is considered free only if it can be bound on all interfaces and on `127.0.0.1` (a permission error on `127.0.0.1` is ignored only on macOS, which does not let an unprivileged user bind a privileged port there although Docker can; a `127.0.0.1` that does not exist at all is also treated as free). Ports below 1024 can be assigned on macOS, but are skipped on Linux for an unprivileged user (the search then continues from 1024). On Windows `O_NOFOLLOW` is not available, so only the symlink check applies.
- Ports in the main worktree's own `.env` are not in the registry. The search starts at the default value + 1, so give the main worktree a port that is not default + 1 (for example `APP_PORT=8080`), or start it first. Ports are assigned without a lock, so do not run several `up` commands at the same time.
- Port assignments are stored in `os.UserConfigDir()/sail-worktree/registry.json` (macOS: `~/Library/Application Support/sail-worktree/registry.json`).
- `vendor/bin/sail` must exist in the worktree (run `composer install` first).

## Upgrading

- The worktree path is now resolved through symlinks (for example `/tmp` → `/private/tmp` on macOS), and the project name contains a hash of that path. If you ran `up` through a symlinked path with an older version, the name changes: `up` writes the new name and leaves the old containers and volumes behind, and `rm` refuses with "does not match". Remove the old project by hand (`docker compose -p <old name> down -v`) or put the old name back in `.env`. The ports are kept, though: the port registry is migrated automatically, and entries recorded under a symlinked path are merged into the real path on the next `up`, so the worktree keeps its previous ports (an entry that already exists for the real path wins; entries for paths that no longer exist are left alone). `rm` also releases such alias entries along with the real one, but it only runs once the name matches.
- An existing `SESSION_COOKIE` in a worktree `.env` is overwritten on the next `up`, so you are logged out of that worktree once.
- For `up` and `rm`, a symlinked or hard-linked `.env` is now an error. `up` also rejects `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` lines in `.env` (also ones copied from the main worktree), and `rm` additionally rejects `COMPOSE_PROFILES` (`up` accepts it). Replace the link with a real file, or delete the line. `stop` no longer refuses these; it only warns.

## Development

```sh
go vet ./...
go test ./...
```

Please write code comments, error messages, commit messages and pull request descriptions in English. Commit subjects follow `feat:`, `fix:`, `refactor:`, `docs:`, `ci:` and `test:` prefixes; the release notes are grouped by them.

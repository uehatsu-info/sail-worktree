English | [日本語](README_JA.md)

# sail-worktree

A tool for running multiple Laravel Sail projects side by side using `git worktree`.
It generates a `.env` for each worktree and automatically assigns ports so they never collide.

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
- Shell variables with the same names as the port variables (and `COMPOSE_FILE`, `COMPOSE_PROFILES`, `COMPOSE_ENV_FILES`, `SAIL_FILES`, `COMPOSE_PROJECT_NAME`) are removed from the environment passed to `sail`, because they take precedence over `.env`.
- Assigned ports are reused on subsequent runs.
- Finally, it runs `vendor/bin/sail up <args>`.

### 3. `sail-worktree stop`

Runs `sail stop`.

### 4. `sail-worktree rm [-y]`

Removes the containers, networks, volumes (including DB data) and built images, and releases the assigned ports.
Equivalent to `docker compose --project-name <name> --project-directory <root> -f <compose> down -v --rmi local --remove-orphans`. Use `-y` to skip the confirmation prompt.
`.env` is not deleted.
Because this is irreversible, `rm` refuses unless `COMPOSE_PROJECT_NAME` in `.env` equals the name recomputed for this worktree (a hand-edited name, a name copied from another worktree, or a moved worktree is refused), and unless `.env` has none of `COMPOSE_FILE`, `COMPOSE_PROFILES`, `COMPOSE_ENV_FILES`, `SAIL_FILES`.

### 5. `sail-worktree version`

Prints the version (the tag for `go install ...@vX.Y.Z`, `(devel)` for a local build). Pin a tag in your project instead of `@latest`.

## Notes

- `up` and `rm` cannot be run in the main worktree.
- `.env` safety: `up` and `rm` refuse a `.env` that is a symbolic link or has other hard links (so the main worktree's `.env` is never rewritten through a link). A new `.env` is created with mode 0600; an existing file keeps its mode. `up` replaces every line of a key it manages, so duplicate lines cannot disagree (Sail uses the last value, Laravel's Dotenv the first).
- A port is considered free only if it can be bound on both all interfaces and `127.0.0.1`.
- Ports in the main worktree's own `.env` are not in the registry. The search starts at the default value + 1, so give the main worktree a port that is not default + 1 (for example `APP_PORT=8080`), or start it first. Ports are assigned without a lock, so do not run several `up` commands at the same time.
- Port assignments are stored in `os.UserConfigDir()/sail-worktree/registry.json` (macOS: `~/Library/Application Support/sail-worktree/registry.json`).
- `vendor/bin/sail` must exist in the worktree (run `composer install` first).

## Development

```sh
go vet ./...
go test ./...
```

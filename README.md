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
- Assigned ports are reused on subsequent runs.
- Finally, it runs `vendor/bin/sail up <args>`.

### 3. `sail-worktree stop`

Runs `sail stop`.

### 4. `sail-worktree rm [-y]`

Removes the containers, networks, volumes (including DB data) and built images, and releases the assigned ports.
Equivalent to `docker compose down -v --rmi local --remove-orphans`. Use `-y` to skip the confirmation prompt.
`.env` is not deleted.

## Notes

- `up` and `rm` cannot be run in the main worktree.
- Port assignments are stored in `os.UserConfigDir()/sail-worktree/registry.json` (macOS: `~/Library/Application Support/sail-worktree/registry.json`).
- `vendor/bin/sail` must exist in the worktree (run `composer install` first).

## Development

```sh
go vet ./...
go test ./...
```

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

Run it in the main worktree, in your Laravel project's directory (where `compose.yaml` is) or below it.
It detects port variables such as `${APP_PORT:-80}` from the `ports:` section of the compose file and generates `.sail-worktree.json` next to it.
Commit this file so that it is shared by all worktrees.
The compose file is the nearest `compose.yaml`, `compose.yml`, `docker-compose.yml` or `docker-compose.yaml` from the current directory up to the worktree root (see [Project directory](#project-directory)).

### 2. `sail-worktree up [args...]`

Run it in an existing worktree, in the project directory (or below it).

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
Because this is irreversible, `rm` refuses unless `COMPOSE_PROJECT_NAME` in `.env` equals the name recomputed for this worktree (a hand-edited name, a name copied from another worktree, or a moved worktree is refused), and unless `.env` has none of `COMPOSE_FILE`, `COMPOSE_PROFILES`, `COMPOSE_ENV_FILES`, `SAIL_FILES`. (`up` accepts `COMPOSE_PROFILES`, but `rm` does not: delete that line before `rm`.) `rm` also refuses a `compose` file that is not a regular file inside the project directory once its links are followed (for example a link to a file outside the project directory, or a link to a directory outside it). All of these checks run before the confirmation prompt. When the name does not match, the error says how to recover: normally set `COMPOSE_PROJECT_NAME` in `.env` back to the name it shows (without quotes); only to remove a project that an older version created under a different name does it show a manual `docker compose -p <name> down -v ...` command, and only if the name consists of safe characters (lowercase letters, digits, `_`, `-`; a name with capitals may differ from the one compose actually uses). Check with `docker compose ls -a` that the project is this worktree's and not another's before running it. When `up` refuses a `COMPOSE_FILE`-style line, the error tells whether it came from this worktree's `.env`, the main worktree's `.env` or its `.env.example`, and how to fix it.

### 5. `sail-worktree version`

Prints the version (the tag for `go install ...@vX.Y.Z`; a local `go build` prints `(devel)` or a pseudo-version such as `v0.0.0-<date>-<commit>`). Pin a tag in your project instead of `@latest`.

## Project directory

The Laravel project does not have to be at the root of the repository. The project directory is the directory that holds `.sail-worktree.json` (for `init`: the compose file). Every command looks for it from the current directory upwards and stops at the worktree root; directories under `vendor` and `node_modules` are skipped, and a `.sail-worktree.json` or compose file that exists but is not a regular file is an error. `.env`, `vendor/bin/sail`, the compose file and the port assignments all belong to the project directory.

```
myapp/                  <- worktree root
|-- docs/
`-- laravel/            <- project directory
    |-- .sail-worktree.json
    |-- compose.yaml
    `-- vendor/
```

```sh
cd myapp/laravel && sail-worktree init      # in the main worktree, once
git worktree add ../myapp-feature-x feature-x
cd ../myapp-feature-x/laravel
composer install
sail-worktree up -d
```

- The main worktree needs the project at the same relative path: `up` copies `.env` from `<main worktree>/laravel/.env` (or `.env.example`). That directory must not resolve outside the main worktree.
- When you run a command below the project directory (for example in `laravel/app`), it prints `project directory: "<path>"` to stderr.
- When no project directory is found, the error names the subdirectories that hold `.sail-worktree.json` (or a compose file, before `init`), so running a command at the worktree root tells you where to go.
- The project name is `<main worktree name>-<worktree name>-<hash of the project directory>`. For a project at the worktree root this is the same name as before.

## Notes

- `up` and `rm` cannot be run in the main worktree.
- `.env` safety: `up` and `rm` refuse a `.env` that is a symbolic link or (on Unix) has other hard links (so the main worktree's `.env` is never rewritten through a link), and `up` also refuses `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` (with them, compose would not read the ports and project name `up` writes). `stop` does not check this, but note that Sail `source`s `.env` (or `.env.$APP_ENV`) as shell code, so a symlinked `.env` runs its target as shell code whether you use this tool or call `sail` directly; keep `.env` a real, trusted file. A new `.env` is created with mode 0600; an existing file keeps its mode (run `chmod 600 .env` yourself for one created by an older version). If a key that `up` writes appears on several lines, `up` keeps the first and drops the rest, so duplicate lines cannot disagree (Sail uses the last value, Laravel's Dotenv the first).
- `compose` in `.sail-worktree.json` must be a relative path inside the project directory (`rm` passes it to `-f`). `rm` follows links first: it refuses a file whose real path is outside the project directory or is not a regular file, and it passes the real path to `-f`. Only `rm` is guarded this way, because `up` and `stop` run `sail`, which finds the compose file by itself and does not pass `-f`. See also the bullets below.
- Limits: Windows junctions are not followed, so they are not detected; a link swapped after the check is not caught; relative `include:` and `extends:` paths in a compose file reached through a link are resolved from the directory of its target; and what the compose file refers to (`include:`, `extends:`, `env_file:`, volumes and so on) is not checked.
- A compose file that is a hard link to a file outside the project directory is not detected, by choice: only a link count could reveal one, and that count would also flag files that are shared through a hard link on purpose, so `rm` does not check it for `compose`. A plain checkout cannot create a hard link (git stores none), but a script or you can. (`.env` is different: `up` and `rm` refuse a hard-linked `.env`, on Unix only. `init` likewise refuses to write `.sail-worktree.json` through a symbolic link or a hard link.)
- To replace a `compose` link, or a hard link that `rm` does not detect, with a real copy, follow "Replacing a linked `compose` file" below.
- A port is considered free only if it can be bound on all interfaces and on `127.0.0.1` (a permission error on `127.0.0.1` is ignored only on macOS, which does not let an unprivileged user bind a privileged port there although Docker can; a `127.0.0.1` that does not exist at all is also treated as free). Ports below 1024 can be assigned on macOS, but are skipped on Linux for an unprivileged user (the search then continues from 1024). On Windows `O_NOFOLLOW` is not available, so only the symlink check applies.
- Ports in the main worktree's own `.env` are not in the registry. The search starts at the default value + 1, so give the main worktree a port that is not default + 1 (for example `APP_PORT=8080`), or start it first. Ports are assigned without a lock, so do not run several `up` commands at the same time.
- Port assignments are stored in `os.UserConfigDir()/sail-worktree/registry.json` (macOS: `~/Library/Application Support/sail-worktree/registry.json`).
- `vendor/bin/sail` must exist in the project directory (run `composer install` first).
- Error messages show control characters, format characters (such as bidirectional overrides) and invalid bytes from paths escaped, so a crafted directory name cannot send escape sequences to your terminal.

### Replacing a linked `compose` file

Use this when `compose` is a symbolic link that resolves outside the project directory (`rm` refuses it) or a hard link to a file outside it (`rm` does not detect it). A symbolic link to a regular file inside the project directory already passes `rm`'s check, so you do not need these steps for it. Copying straight over the link would also change the file it points to, so these steps copy to a new name and move the copy over the link.

Run them in one Unix shell, from the project directory (where `.sail-worktree.json` is). They were tried on macOS in sh and zsh, not on Linux or Windows.

Before step 1, shut down containers, editors and file watchers that write in the worktree.

1. Put the value of `compose` from `.sail-worktree.json` in a variable, quoted: `f='compose.yaml'`. If the value contains a quote (`'`) or a backslash, do not use these steps (the quoted variable would break). Write it without `..`: `rm` and the shell resolve `..` differently when a link is in the path. For `docs/../compose.yaml`, first change `compose` to a path without `..`, such as `compose.yaml`.
2. Run `ls -ldL -- "$f"`. It must start with `-` (a regular file), as in `-rw-r--r-- 1 you staff ... compose.yaml`. If it starts with anything else, or `ls` fails, stop: replace the link with a regular file inside the project directory, or set `compose` to such a file.
3. Run `readlink -- "$f"`: it shows where a symbolic link points (a hard link prints nothing). The copy brings that content into the worktree, so stop if you do not want it there.
4. If `compose` is in a subdirectory, run `ls -ld` on each directory in the path, without a trailing slash (for `docs/sub/compose.yaml`: `ls -ld -- docs docs/sub`). `ls -ldL` follows links, so only this shows a link in the path. Each must start with `d`; on anything else, or an `ls` error, stop and fix that link first.
5. Run `[ ! -e "$f.new" ] && [ ! -L "$f.new" ] && cp -- "$f" "$f.new" && mv -- "$f.new" "$f" || echo "not replaced: $f" >&2`. `$f.new` is a temporary name that must not exist, not even as a link. If it prints `not replaced`, nothing was replaced: look at `$f.new` (it was there already, or it is a copy that was not moved), remove it with `rm -- "$f.new"` or move it away if it is yours, and run the line again.
6. Check the result, because no message does not prove it worked. `ls -l -- "$f"` must start with `-`, and its link count (the number after the permissions) must be 1; a hard link that is still there shows 2 or more. `ls -l` does not show whether a directory in the path is a link: for a file in a subdirectory, check the directories again as in step 4.
7. Check that relative `include:`, `extends:` and `env_file:` paths still point where you mean, because `rm` does not check them (see the limits above). The copy is a new file: its mode is the source's under your umask and it has no ACLs or extended attributes, so set them again if you need them.

Your checks and the copy are not one step, so a process writing in the worktree at that moment can still make step 5 write outside it (a link planted as `$f.new`, or a directory in the path swapped for a link). Shutting down the processes first lowers this risk but does not remove it: other tools, git operations and other users can still write there. An outside file overwritten that way leaves no trace in these checks.

## Upgrading

- Laravel projects in a subdirectory of the repository are supported (see [Project directory](#project-directory)). Projects at the worktree root are unaffected: same project directory, project name and port assignments.
- `init` writes `.sail-worktree.json` next to the nearest compose file from the current directory upwards; it used to write at the worktree root always. Running it in a subdirectory that has its own compose file (such as `.devcontainer/`) therefore writes there.
- `up`, `stop` and `rm` use the nearest `.sail-worktree.json`. A directory with its own committed `.sail-worktree.json` (outside `vendor` and `node_modules`) becomes the project, with its own `.env` and `vendor/bin/sail`, and gets a different project name; the containers of a project at the root are left alone, and `rm` there refuses because the name does not match.
- A compose file name that exists but is not a regular file (such as a directory named `compose.yaml`) is now an error instead of being accepted, and `init` refuses a `.sail-worktree.json` that is a link or not a regular file.
- `rm` checks the compose file against the project directory, so for a project in a subdirectory a link to a compose file elsewhere in the worktree is refused.
- `init` prints the created path quoted (`created "<path>"`), and error messages show control characters escaped.

- The worktree path is now resolved through symlinks (for example `/tmp` → `/private/tmp` on macOS), and the project name contains a hash of that path. If you ran `up` through a symlinked path with an older version, the name changes: `up` writes the new name and leaves the old containers and volumes behind, and `rm` refuses with "does not match". Remove the old project by hand (`docker compose -p <old name> down -v`) or put the old name back in `.env`. The ports are kept, though: the port registry is migrated automatically, and entries recorded under a symlinked path are merged into the real path on the next `up`, so the worktree keeps its previous ports (an entry that already exists for the real path wins; entries for paths that no longer exist are left alone). `rm` also releases such alias entries along with the real one, but it only runs once the name matches.
- An existing `SESSION_COOKIE` in a worktree `.env` is overwritten on the next `up`, so you are logged out of that worktree once.
- For `up` and `rm`, a symlinked or hard-linked `.env` is now an error. `up` also rejects `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` lines in `.env` (also ones copied from the main worktree), and `rm` additionally rejects `COMPOSE_PROFILES` (`up` accepts it). Replace the link with a real file, or delete the line. `stop` no longer refuses these; it only warns.

## Development

```sh
go vet ./...
go test ./...
```

Please write code comments, error messages, commit messages and pull request descriptions in English. Commit subjects follow `feat:`, `fix:`, `refactor:`, `docs:`, `ci:` and `test:` prefixes; the release notes are grouped by them.

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

Every command except `version` prints the usage for `-h` or `--help` and does nothing else. `up` and `stop` pass their arguments to sail, so for them only a first `-h`/`--help` counts (`up -d --help` is an ordinary `up` that passes `--help` to sail, after writing `.env`); the other commands take it only on its own (`rm -y --help` is an error).

### 1. `sail-worktree init` (optional)

You do not need to run `init`: `up`, `stop` and `rm` find the compose file and its port variables by themselves (see [Project directory](#project-directory)). Run `init` only to pin that list in a file, for example when detection misses a port (a long-syntax `published:` entry, a flow-style list such as `ports: ['${APP_PORT:-80}:80']`, or ports in an `include:`d file) or when you want to edit the list.

Run it in your Laravel project's directory (where `artisan` and the compose file are) or below it. It detects port variables such as `${APP_PORT:-80}` from host port mappings like `- '${APP_PORT:-80}:80'` in the compose file and writes `.sail-worktree.json` next to it. It prints "created", or "updated" when it replaces an existing file, overwriting any hand edits. Commit the file to share it with all worktrees, or delete it to go back to detection. The file wins over detection, so if `up` seems to ignore a change to the compose file, check `git status` for a forgotten, untracked `.sail-worktree.json`.

#### The `.sail-worktree.json` file

```json
{
  "compose": "compose.yaml",
  "port_vars": [
    { "name": "APP_PORT", "default": 80 },
    { "name": "VITE_PORT", "default": 5173 }
  ]
}
```

- `compose`: the compose file, as a relative path inside the project directory (`rm` passes it to `docker compose -f`).
- `port_vars`: the variables `up` assigns ports to. `name` must be the variable the compose file uses: `up` writes that key in `.env`, so do not list other keys. It must be a shell variable name (letters, digits and `_`, not starting with a digit), and not one that has a meaning of its own (in any case): a key `up` writes itself (`COMPOSE_PROJECT_NAME`, `SESSION_COOKIE`, `APP_URL`, `SANCTUM_STATEFUL_DOMAINS`), a setting docker compose or the docker CLI reads (such as `COMPOSE_FILE`, `COMPOSE_PROFILES`, `DOCKER_HOST` or `DOCKER_CONTEXT`; other names such as `DOCKER_WEB_PORT` or `COMPOSE_WEB_PORT` are fine), `SAIL_FILES`, `PATH`, `HOME`, `SHELL`, `USER`, `PWD`, `IFS`, or Sail's `APP_SERVICE`, `WWWUSER`, `WWWGROUP`, `APP_ENV`. None of these names ends in `PORT`, so detection never finds one in the compose file. `default` (1 to 65535) is the port the compose file uses when the variable is unset; the search for a free port starts at `default` + 1. List each variable once (names are compared case-insensitively). A file with any other name or default, or with a repeated name, is refused.

You can write the file by hand, for example to add a port that detection does not find; running `init` later replaces it with what it detects.

### 2. `sail-worktree up [args...]`

Run it in an existing worktree, in the project directory (or below it).

```sh
git worktree add ../myapp-feature-x feature-x
cd ../myapp-feature-x
composer install
sail-worktree up -d
```

- The compose file and the port variables come from `.sail-worktree.json` if there is one; otherwise they are detected from the compose file on every run (only `- ...` list lines with a host port mapping such as `'${APP_PORT:-80}:80'` count, so a usual `environment:` entry such as `- DB_PORT=${DB_PORT:-3306}` is not rewritten). Without the file, a branch whose compose file adds or removes a port variable gets its ports assigned on the next `up`, or released from the registry (the old line in `.env` stays).
- If `.env` does not exist, it is copied from the main worktree's `.env` (or `.env.example` if that is missing).
- In a bare repository with linked worktrees (`git clone --bare`, then `git worktree add`), there is no main worktree checkout: every worktree is a linked one, `.env` is copied from the worktree's own `.env.example` (a regular file only, not a link; without one, create `.env` first), and nothing is recorded as a main worktree (so no worktree uses the default ports). `status` says that the repository is bare instead of naming a main worktree.
- Nothing is written when `vendor/bin/sail` is missing.
- Each port variable gets a free port. The search starts at the default value + 1, leaving the default value to the main worktree.
- Ports already assigned to other worktrees and ports in use on the host are skipped.
- `COMPOSE_PROJECT_NAME` is set, and the port in `APP_URL` is updated.
- `SESSION_COOKIE` is set to `<COMPOSE_PROJECT_NAME>-session` (an existing value is overwritten). Cookies are shared across ports on `localhost`, so without a distinct name the logins of different worktrees interfere with each other.
- When `up` updates `APP_URL` (above) and `.env` sets `SANCTUM_STATEFUL_DOMAINS`, `up` appends the new `APP_URL`'s `host:port` to it (only the host on the scheme's default port) and prints `SANCTUM_STATEFUL_DOMAINS: added <entry>`. Laravel Sanctum adds `APP_URL` by itself only when the variable is not set, so without this, cookie authentication fails in the worktree.
  - Nothing is added when an entry already matches under Sanctum's rule (wildcards and `__SANCTUM_CURRENT_REQUEST_HOST__` count), when the variable is absent, or when its value turns stateful domains off (empty, `null`, `false`, `true`, `empty`, the same words in parentheses such as `(null)`, or only commas).
  - A value that is not a plain list (a variable such as `${APP_URL}`, a comment, a quote inside, a space outside quotes, ...), or an `APP_URL` host that is not a plain host name or IP address (other characters, an IPv6 zone, an IPv4-mapped IPv6 address), is left alone with a warning that says what to add. The warning repeats on every `up` until the value contains that entry as an element of its own (add it by hand inside the quotes of a quoted value with no comment after them, or to an unquoted value that has no spaces, tabs, `#` or quotes at all) or, for a host warning, until `APP_URL` changes.
  - Existing entries are never removed: an entry for an older port stays, and entries for other ports (such as the Vite port) are not updated.
- Shell variables with the same names as the port variables, every `COMPOSE_*` variable and `SAIL_FILES` are removed from the environment passed to `sail` (also for `stop`) and to `docker compose` in `rm`, because they take precedence over `.env`. `DOCKER_HOST` and `DOCKER_CONTEXT` are kept on purpose.
- Assigned ports are reused on subsequent runs. A recorded value that is not a port number (1 to 65535, from a hand-edited registry) is replaced by a new port.
- Finally, it runs `vendor/bin/sail up <args>`.

### 3. `sail-worktree stop`

Runs `sail stop`.

`stop` does not refuse anything: it never writes `.env` and stopping is easy to undo. Instead, in a non-main worktree it prints a warning to stderr when `.env` (a regular file only; links and FIFOs are not read) has `COMPOSE_FILE`, `COMPOSE_ENV_FILES` or `SAIL_FILES`, or when its `COMPOSE_PROJECT_NAME` differs from the name recomputed for this worktree, since `sail stop` could then stop another project. The warning is best effort (a shell expression in `.env`, or `.env.$APP_ENV`, is not detected) and is not printed in the main worktree.

### 4. `sail-worktree rm [-y]`

Removes the containers, networks, volumes (including DB data) and built images, and releases the assigned ports.
Equivalent to `docker compose --project-name <name> --project-directory <project directory> -f <compose> down -v --rmi local --remove-orphans`. Use `-y` to skip the confirmation prompt.
`.env` is not deleted.
Because this is irreversible, `rm` refuses unless `COMPOSE_PROJECT_NAME` in `.env` equals the name recomputed for this worktree (a hand-edited name, a name copied from another worktree, or a moved worktree is refused), and unless `.env` has none of `COMPOSE_FILE`, `COMPOSE_PROFILES`, `COMPOSE_ENV_FILES`, `SAIL_FILES`. (`up` accepts `COMPOSE_PROFILES`, but `rm` does not: delete that line before `rm`.) `rm` also refuses a `compose` file that is not a regular file inside the project directory once its links are followed (for example a link to a file outside the project directory, or a link to a directory outside it). All of these checks run before the confirmation prompt. When the name does not match, the error says how to recover: normally set `COMPOSE_PROJECT_NAME` in `.env` back to the name it shows (without quotes); only to remove a project that an older version created under a different name does it show a manual `docker compose -p <name> down -v ...` command, and only if the name consists of safe characters (lowercase letters, digits, `_`, `-`; a name with capitals may differ from the one compose actually uses). Check with `docker compose ls -a` that the project is this worktree's and not another's before running it. When `up` refuses a `COMPOSE_FILE`-style line, the error tells whether it came from this worktree's `.env`, the main worktree's `.env` or its `.env.example`, and how to fix it.

### 5. `sail-worktree version`

Prints the version (the tag for `go install ...@vX.Y.Z`; a local `go build` prints `(devel)` or a pseudo-version such as `v0.0.0-<date>-<commit>`). Pin a tag in your project instead of `@latest`.

## Inspecting worktrees

`ps`, `ports` and `status` only read. They never write `.env`, `.sail-worktree.json` or the registry, and never source `.env`. `check` and `prune` write only the registry.

### `sail-worktree ps [--all] [--json] [--no-docker]`

Lists the worktrees and main worktrees recorded in the port registry, which is a view of `registry.json` and not `docker ps`. A main worktree (recorded by `up` or [`check`](#sail-worktree-check)) comes first, marked `(main)` in the `WORKTREE` column; it shows the recorded ports, which `up` keeps free, not a fresh read of its `.env`. Its `STATE` is `-` (docker is not asked: its compose project name depends on its `.env`, which is not read), or `stale` / `unattributed` as below.

```
WORKTREE      SUBDIR  BRANCH  PORTS                             STATE    DIR
myapp (main)  .       main    APP_PORT=80 FORWARD_DB_PORT=3306  -        /work/myapp
myapp-x       .       feat-x  APP_PORT=81 FORWARD_DB_PORT=3307  running  /work/myapp-x
```

- Without `--all` it shows the projects of the repository you are in (the `SUBDIR` column tells projects of one repository apart); entries of other repositories are not shown. Run it anywhere inside the repository; no `.sail-worktree.json` or Laravel project is needed. `--all` shows every entry of the registry, also outside a repository, and runs `git worktree list` in the other entries' directories (not on Windows; see the [Notes](#notes)).
- `STATE` is `stale` for an entry whose worktree or directory is gone (or that git lists as prunable) or whose key is not a valid path, `unattributed` (only with `--all`) for a directory that exists but that git does not place in a worktree (not a repository, git failed, or on Windows another repository, where git is not run), and otherwise docker's view of the entry's compose project, asked once with `docker compose ls -a` (`COMPOSE_*` variables are removed from its environment): `running` (a container runs), `stopped` (containers exist, none is `running`; paused or restarting ones count as stopped), `down` (docker knows no such project) or `unknown` (docker could not be asked: not installed, not running, too slow; this never fails `ps`). The project is looked up by the name recomputed for the worktree, so a worktree whose `.env` has another `COMPOSE_PROJECT_NAME` can show `down` while it runs. `DOCKER_HOST`, `DOCKER_CONTEXT` and `DOCKER_CONFIG` are kept on purpose, so the state is that daemon's view (a remote one is reached over the network, for at most 10 seconds). `--no-docker` does not ask docker and shows `-`. A stale entry still holds its ports until `rm` releases them, `prune` removes it (when its directory is gone or its key is invalid) or you edit the registry. Entries that are gone or invalid and cannot be attributed to this repository are counted in a note on stderr; use `--all` to list them.
- Keys that an older version recorded under a symlinked path are folded into their real path in memory only; the registry is not rewritten.
- `--json` prints an array of objects with the fields `kind` (`worktree` or `main`), `dir`, `worktree`, `subdir`, `branch` (`(detached)` for a detached HEAD, empty when unknown), `ports` (an object), `name` (the compose project name; empty for a main worktree) and `state`.

### `sail-worktree status [--no-docker]`

Shows how the current worktree is set up and reports what is wrong with it. Run it in the project directory (or below it), like `up`; it works in the main worktree too, where it only says that `up` and `rm` do not run there and that `check` records its ports.

```
project directory: "/work/myapp-x"
worktree:          myapp-x (branch feat-x)
main worktree:     "/work/myapp"
compose project:   myapp-myapp-x-1a2b3c
configuration:     .sail-worktree.json, compose file "compose.yaml"
docker:            running
.env:              ok
ports:
  APP_PORT             env=81 registry=81 host=in use by this project
APP_URL:           "http://localhost:81"
```

- It shows only a fixed list of facts (paths, the compose project name, the assigned ports, `APP_URL` without credentials, query and fragment); other `.env` values are never printed.
- `problems:` lists what `up` would fix or refuse; any problem is reported after the whole report with `error: found N problem(s)` and exit status 1: a configuration error; `.env` missing, a symbolic or hard link, or not a regular file (a link is not read); `COMPOSE_FILE`, `COMPOSE_ENV_FILES` or `SAIL_FILES` in `.env`; `COMPOSE_PROJECT_NAME` missing or different from this worktree's name; a port variable that is missing or not a number in `.env`, different from the registry, not in the registry, or also recorded for another entry or for a main worktree (of any repository; the next `up` moves it); an `APP_URL` that does not use `APP_PORT`; and a `SANCTUM_STATEFUL_DOMAINS` that lacks the entry for `APP_URL` (the same rule as `up`).
- The report ends with `warnings:` and then `problems:`. `warnings:` do not change the exit status: a port that cannot be bound while the project is not running (another process may use it; a permission error looks the same), the `SANCTUM_STATEFUL_DOMAINS` value that `up` only warns about, and a port variable that `.env` sets on more than one line (`status` shows the first line, but Sail and compose use the last; `up` keeps only the first; read line by line, as `check` does). A port is not probed while docker says the project is running, nor when docker could not be asked (`unknown`) or with `--no-docker` (the `docker:` line then says `not asked`, and the ports show `host=not probed`).
- The docker state is the one `ps` shows (see above). To see whether a port is free, `status` binds it for an instant and closes it again; nothing is written to disk, but a firewall may notice the bind.

### `sail-worktree ports [PORT] [--all] [--json] [--no-docker]`

Lists the ports recorded in the registry, one row per port variable, sorted by port: which entry records it and whether this host can bind it. Like `ps` it works anywhere inside the repository and shows the repository you are in (`--all`: every entry).

```
PORT  VARIABLE         WORKTREE  SUBDIR  STATE    HOST                       CONFLICT
81    APP_PORT         myapp-x   .       stopped  unavailable (php pid 123)  -
82    APP_PORT         myapp-y   .       running  in use by this project     -
3307  FORWARD_DB_PORT  myapp-x   .       stopped  free                       -
```

- `HOST` is `free`, `unavailable` (the port cannot be bound: in use, or not allowed to bind), `in use by this project` (docker says the project runs, so its own port is not probed) or `invalid` (the recorded value is not a port number from 1 to 65535, which only a hand-edited registry has; it is neither probed nor looked up, and never counts in `CONFLICT`, as `check` ignores it too). For an unavailable port, on Unix and if `lsof` can see it, the name and pid of the first listening process are added (`lsof` is only called for such ports; without it, or on Windows, you get plain `unavailable`; `status` calls the same condition `cannot bind`). Every port is bound for an instant and closed again; nothing is written to disk, but a firewall may notice the bind.
- `STATE` is the one `ps` shows (including `stale` for entries whose worktree is gone, which still hold their ports); `--no-docker` does not ask docker, so a running project's own ports are probed too and may show as `unavailable`; the same happens on Windows with `--all` for other repositories' projects, whose entries are `unattributed`.
- Main worktrees' ports are listed too (`WORKTREE` is marked `(main)`, `STATE` is `-` as in `ps`). They are probed like any other port, so a running main worktree's port shows as `unavailable`.
- `CONFLICT` is `yes` when the port is recorded more than once among the listed rows: by two entries (a main worktree included), or by two variables of one entry. `up` never assigns the same port twice, so between worktrees this points at a hand-edited registry or an alias that was not merged; a worktree on a main worktree's port is moved by its next `up`. With `--all`, every recorded entry is compared, as in `check` (which leaves out records of directories that are gone, and main worktree records that another record replaced; `status` still counts those as other holders).
- With `PORT` (digits only, 1 to 65535) only that port is shown; if nothing records it (also when only another repository does, without `--all`), one row without a variable still tells whether the host can bind it (`STATE` `-`; in `--json`, `kind` is `none`, `state` is `-` and `variable`, `worktree` and `subdir` are empty).
- `--json` prints an array of objects with the fields `kind` (`worktree`, `main`, or `none` for the row of a port nothing records), `port`, `variable`, `worktree`, `subdir`, `state`, `host` (the four values above), `process` (empty when unknown), `pid` (0 when unknown) and `conflict`.

### `sail-worktree check`

Records the main worktree of the project you are in, then reports ports that more than one main worktree or worktree uses, across every repository in the registry. Like `up`, run it in the project directory (or below it), in the main worktree or in a linked one: this is how a repository that you use only in its main worktree becomes known. Anywhere else, such as the repository root of a project in a subdirectory or outside a repository, it records no main worktree (a warning says why and where to run it), but still updates the recorded ones as described below and reports. In a bare repository it records none either, since there is no main worktree checkout (a warning says so).

```
recorded main worktree: "/work/shop"
registry: 2 main worktree record(s), 1 worktree record(s)
conflicts:
  port 80: main worktree "/work/blog" APP_PORT, main worktree "/work/shop" APP_PORT
    keeps it: main worktree "/work/blog" APP_PORT
    fix: set APP_PORT=82 in the .env of "/work/shop"
problems:
  - port 80 is used 2 times
error: found 1 problem(s)
```

- It writes only the registry: under the same lock as `up`, replaced atomically, and only when the record changed. It never writes `.env` or `.sail-worktree.json`, never sources `.env`, and runs neither docker nor sail. If another run holds the lock for longer than the wait, `check` records and updates nothing, reports on the registry as it is, and says under `warnings:` that the report may be based on records that updating would have changed. Any other failure to take the lock (no permission, a read-only file system; on Windows also an access error that is still there when the wait ends) is an error: only the warnings collected until then are printed, and nothing is reported.
- What is recorded for a main worktree: for each port variable, the value in its `.env` (from the last line of the key, which Sail and compose use), or the compose default when `.env` does not set it (or there is no `.env`); an invalid value reserves nothing. A `.env` that is a link or not a regular file is not read, and the previous record is kept (a warning says so for the repository you are in). The main worktree must have the project directory at the same relative path, and it must be a project there (`.sail-worktree.json`, or `artisan` with a compose file); otherwise nothing is recorded (`check` warns that the main worktree has no project directory there). `up` records the same, when it runs in a linked worktree; like `check`, it reads that `.env` before it waits for the lock and keeps a record that another run changed meanwhile (the next `up` or `check` updates it). The port variables are those of the configuration of the worktree you run in, which may be a branch's: a variable that it does not have keeps its previous record, so a branch without it does not free the main worktree's port. Only `check` run in the main worktree itself records exactly the main's variables, which drops one that the main no longer has.
- The first holder keeps the port: main worktrees before worktrees, each in path order. For a worktree, the fix is to run `sail-worktree up` in it (it assigns another port). For a main worktree, `check` proposes a port that no record uses and that this host can bind (bound for an instant and closed again); set it in that `.env` yourself and restart its containers. A variable name that is not a plain shell name is only quoted.
- On every run that takes the lock, `check` also updates every recorded main worktree from its `.env` (links are not followed; a record that another run changes while `check` waits for the lock, the one of the repository you are in included, is kept as the newer one and left for the next `check`), so a port changed there since is seen, and `up` avoids it from then on. A port variable added to the configuration is seen after the next `up` or `check` in that repository's project directory. A variable that only the main worktree's record keeps (see above) is updated only by `check` run outside that repository's project directory (outside the repository, or at its root when the project is in a subdirectory), or dropped or re-recorded by `check` run in the main worktree: `up` and `check` in the project directory of its linked worktrees leave it as recorded.
- A port variable that a recorded main worktree's `.env` sets on more than one line is listed under `warnings:` (the variable and the directory, not the values): Sail and compose use the last line, Laravel's Dotenv the first, so keep only one. `.env` is read line by line, so a line inside a multi-line quoted value that looks like `NAME=...` counts too (as it does for the port that is recorded). Like the update above, it uses the variables of the record, so a main worktree that this run records for the first time is checked from the next run. It does not change the exit status.
- Two repositories whose main worktrees both use the compose defaults (`APP_PORT` 80, `FORWARD_DB_PORT` 3306 and so on) conflict: they cannot run at the same time. Give one of them other ports if you run them together; if you never do, the report is expected.
- A record whose directory no longer exists (or whose key is not a path this tool uses; see the Notes on Windows network paths) is listed under `warnings:` and is not a conflict; `up` still keeps its ports free. So is a main worktree record that another record replaced, which a move leaves behind: one whose path goes through a symbolic link to a directory that has its own record (you moved `~/work` and left a `~/work` link), or a directory that is no longer a project (no `.sail-worktree.json`, nor `artisan` with a compose file) with a main worktree record of a project below it (you moved the project from the repository root into `laravel/`). A linked worktree in a main worktree's record whose directory is gone is listed there too, although it holds no ports. Remove such records with [`prune`](#sail-worktree-prune--y).
- The exit status is 1 when there is a conflict (`error: found N problem(s)`); warnings do not change it. An error (a broken registry, a lock that cannot be created) also exits with 1.

### `sail-worktree prune [-y]`

Removes the registry records that `check` lists under `warnings:`: worktree and main worktree records of directories that no longer exist (a deleted clone, a worktree removed with `git worktree remove` instead of `rm`) or whose key is not a path this tool uses, main worktree records that another record replaced (see [`check`](#sail-worktree-check)), and, in a main worktree's record, linked worktrees whose directory is gone. Each main worktree line says why. Run it anywhere.

```
these registry records are stale:
  worktree "/work/shop-old" (ports: APP_PORT=81 FORWARD_DB_PORT=3307)
  main worktree "/work/blog" (ports: APP_PORT=80): it is not a usable local directory
prune removes only these records from the registry; it does not touch containers, networks or volumes (find those of a removed worktree with `docker compose ls -a`).
Remove these records? [y/N] y
removed 1 worktree record(s), 1 main worktree record(s), 0 linked worktree entr(ies)
```

- It asks before it removes anything (`-y` skips the question); with nothing to remove it says so and exits 0.
- It writes only the registry: under the same lock as `up`, taken after you answer, and replaced atomically. The registry is read again under the lock, and only listed records that are still recorded and still unusable are removed (others are reported as `kept`). A replaced main worktree record is checked again after you answer, and is removed only if it has not changed since the list and the record that replaces it is still there. It never writes `.env` or `.sail-worktree.json` and runs neither docker, sail nor git, so the containers, networks and volumes of a removed worktree stay; find them with `docker compose ls -a` and remove them yourself.
- A worktree record that an older version made under a symbolic link to a directory that exists is not removed (`check` does not warn about it either); the next `up` in that worktree moves it to the real path. The same holds for such a path in a main worktree's list of linked worktrees. A main worktree record whose path goes through such a link before its last element is removed only when the real path has its own record; until then it is the only record of that main worktree and keeps its ports. A directory that is only out of reach for the moment (an unmounted disk) counts as gone, and a main worktree that has no project at that path for the moment (its checkout is on a branch without it) while a project below it has a record counts as replaced, so read the list before you answer; `up`, or `check` in that repository, records it again.

## Project directory

The Laravel project does not have to be at the root of the repository. Every command looks for its project directory from the current directory upwards and stops at the worktree root:

1. the nearest directory with `.sail-worktree.json`, if there is one anywhere up to the worktree root;
2. otherwise the nearest directory with `artisan` and a compose file (`compose.yaml`, `compose.yml`, `docker-compose.yml` or `docker-compose.yaml`, the first one that exists).

`init` writes into the nearest directory of either kind, never into a directory that has only a compose file (such as `.devcontainer/`). Directories under `vendor` and `node_modules` are skipped, and a `.sail-worktree.json`, or a compose file next to `artisan`, that exists but is not a regular file is an error. `.env`, `vendor/bin/sail`, the compose file and the port assignments all belong to the project directory.

```
myapp/                  <- worktree root
|-- docs/
`-- laravel/            <- project directory
    |-- artisan
    |-- compose.yaml
    `-- vendor/
```

```sh
cd myapp                                    # the main worktree
git worktree add -b feature-x ../myapp-feature-x
cd ../myapp-feature-x/laravel
composer install
sail-worktree up -d
```

- A branch without your committed `.sail-worktree.json` (one created before that commit) silently falls back to detection, so a hand-added port or a custom `compose` path is not used there (a custom path that is not a standard name gives a not-found error). Merge that commit into the branch, or rebase the branch onto it.
- A `.sail-worktree.json` further up wins over a nearer Laravel project, and `up`, `stop` and `rm` warn about it. Ignore the warning if the nearer app is unrelated; to use that app instead, run `sail-worktree init` in it (it then becomes the project).
- The main worktree needs the project at the same relative path: `up` copies `.env` from `<main worktree>/laravel/.env` (or `.env.example`). That directory must not resolve outside the main worktree (followed through symbolic links; Windows junctions are not followed). Only the directory is checked: a `.env` there that is itself a link is followed, as for a project at the root.
- A project directory that resolves outside the worktree is an error. As with the compose check, a link swapped in after these checks is not caught.
- When you run `up`, `stop` or `rm` below the project directory (for example in `laravel/app`), it prints `project directory: "<path>"` to stderr.
- When no project directory is found, the error names the subdirectories that would be found, and the directories on the way up that have only `artisan` or only a compose file, so running a command at the worktree root tells you where to go.
- The project name is `<main worktree name>-<worktree name>-<hash of the project directory>`, where the two names are lower-cased, each run of characters other than `a-z`, `0-9`, `_` and `-` becomes one `-`, and `-` and `_` are trimmed at both ends. When nothing is left (worktree names without an ASCII letter or digit), `wt` is used instead. For a project at the worktree root this is the same name as before. In a bare repository the main worktree name is the bare directory's (`myapp.git` gives `myapp-git`).

## Notes

- `up` and `rm` cannot be run in the main worktree.
- `.env` safety: `up` and `rm` refuse a `.env` that is a symbolic link or (on Unix) has other hard links (so the main worktree's `.env` is never rewritten through a link), and `up` also refuses `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` (with them, compose would not read the ports and project name `up` writes). `stop` does not check this, but note that Sail `source`s `.env` (or `.env.$APP_ENV`) as shell code, so a symlinked `.env` runs its target as shell code whether you use this tool or call `sail` directly; keep `.env` a real, trusted file. A new `.env` is created with mode 0600; an existing file keeps its mode (run `chmod 600 .env` yourself for one created by an older version). If a key that `up` writes appears on several lines, `up` keeps the first and drops the rest, so duplicate lines cannot disagree (Sail uses the last value, Laravel's Dotenv the first).
- `compose` in `.sail-worktree.json` must be a relative path inside the project directory; it, or the detected compose file, is what `rm` passes to `-f`. Only that one file is passed, so `compose.override.yaml` is not read by `rm`: volumes defined only there are left behind. `rm` follows links first: it refuses a file whose real path is outside the project directory or is not a regular file, and it passes the real path to `-f`. Only `rm` is guarded this way, because `up` and `stop` run `sail`, which finds the compose file by itself and does not pass `-f`. See also the bullets below.
- Limits: Windows junctions are not followed, so they are not detected; a broken link named `compose.yaml` is skipped, so a `docker-compose.yml` next to it is used; a link swapped after the check is not caught; relative `include:` and `extends:` paths in a compose file reached through a link are resolved from the directory of its target; and what the compose file refers to (`include:`, `extends:`, `env_file:`, volumes and so on) is not checked.
- A compose file that is a hard link to a file outside the project directory is not detected, by choice: only a link count could reveal one, and that count would also flag files that are shared through a hard link on purpose, so `rm` does not check it for `compose`. A plain checkout cannot create a hard link (git stores none), but a script or you can. (`.env` is different: `up` and `rm` refuse a hard-linked `.env`, on Unix only. `init` likewise refuses to write `.sail-worktree.json` through a symbolic link, or through a hard link on Unix.)
- To replace a `compose` link, or a hard link that `rm` does not detect, with a real copy, follow "Replacing a linked `compose` file" below.
- A port is considered free only if it can be bound on all interfaces and on `127.0.0.1` (a permission error on `127.0.0.1` is ignored only on macOS, which does not let an unprivileged user bind a privileged port there although Docker can; a `127.0.0.1` that does not exist at all is also treated as free). Ports below 1024 can be assigned on macOS, but are skipped on Linux for an unprivileged user (the search then continues from 1024). On Windows `O_NOFOLLOW` is not available, so only the symlink check applies.
- Ports in a main worktree's own `.env` are recorded in the registry (under `mains`) when `up` runs in one of its linked worktrees or `check` runs in its project directory (or below it). `up` never assigns them, and moves a worktree off such a port (it prints `(was N, ...)`). The value is read as compose reads it: an inline comment (` # ...`) is ignored, and an empty value means the compose default. A repository where neither has run is not known, so run `check` once in the project directory of each repository you use only in its main worktree.
- Port assignments are stored in `os.UserConfigDir()/sail-worktree/registry.json` (macOS: `~/Library/Application Support/sail-worktree/registry.json`). The file is replaced atomically, so a reader never sees half of it.
- Several `up` commands (and `rm`, `check` and `prune`) can run at the same time: while one reads the registry, chooses ports, writes `.env` and saves, the others wait on a lock file, `registry.json.lock` next to the registry (they print `waiting for another sail-worktree run ...` after a second). The lock is released before `sail up` starts and is not held while `rm` runs docker. `ps`, `ports` and `status` never take it; a `check` that waits too long for it reports without recording anything. If a run was killed and left the lock behind, the next run removes it once it is 60 seconds old; before that, and only if no sail-worktree is running, you can delete the file yourself (the error message names it). A run killed at the very moment it removes a lock can also leave a `registry.json.lock.claim-*` file; that lock is then not removed automatically, so delete the lock file as the error message says (the claim file can be deleted too). A run that is suspended for over a minute while it holds the lock (a sleeping laptop, for example) can lose it to the next run. Do not run an older sail-worktree (v0.8.1 or earlier) at the same time as this one: those versions take the lock differently or not at all, so two runs could again update the registry at once. What remains is a short window between saving the ports and the containers binding them, in which another program can still take a port.
- `vendor/bin/sail` must exist in the project directory (run `composer install` first).
- On Windows, a path in the registry is only used if it starts with a drive letter (`C:\...`; a mapped network drive such as `Z:\...` is fine). A UNC or device path (`\\server\share\...`, `\\?\...`) is never looked at, because looking at it can make Windows connect to that server and send your credentials. Neither is a drive-letter path that goes through a symbolic link or junction to such a path. So if you run a project from a UNC path (possible from PowerShell), `up`, `rm` and `status` work there (`status` finds its own record), but `ps` and `ports` treat its record as invalid (only counted in a note, or `stale` with `--all`), `check` warns about it and `prune` offers to remove it (its ports are then not reserved until `up`, or for a main worktree also `check` in its project directory, records it again); map a drive letter instead. Paths git reports for the directory you run in are your own and are used as they are. With `--all`, `ps` and `ports` do not run git in the other repositories of the registry on Windows, because git itself would look at paths from their metadata: those entries (of directories that exist) show as `unattributed`, without worktree name, branch or docker state. On other systems they run `git worktree list` there (git reads that metadata, which is harmless without UNC paths) and use only the worktree paths that pass the rule for registry keys.
- Error messages show control characters (except newlines, which they use for layout), format characters (such as bidirectional overrides) and invalid bytes escaped, so a crafted directory name cannot send escape sequences to your terminal (a newline in a path can still start a line of its own).

### Replacing a linked `compose` file

Use this when `compose` is a symbolic link that resolves outside the project directory (`rm` refuses it) or a hard link to a file outside it (`rm` does not detect it). A symbolic link to a regular file inside the project directory already passes `rm`'s check, so you do not need these steps for it. Copying straight over the link would also change the file it points to, so these steps copy to a new name and move the copy over the link.

Run them in one Unix shell, from the project directory (where `.sail-worktree.json` or `artisan` is). They were tried on macOS in sh and zsh, not on Linux or Windows.

Before step 1, shut down containers, editors and file watchers that write in the worktree.

1. Put the value of `compose` from `.sail-worktree.json` (or the name of the detected compose file) in a variable, quoted: `f='compose.yaml'`. If the value contains a quote (`'`) or a backslash, do not use these steps (the quoted variable would break). Write it without `..`: `rm` and the shell resolve `..` differently when a link is in the path. For `docs/../compose.yaml`, first change `compose` to a path without `..`, such as `compose.yaml`.
2. Run `ls -ldL -- "$f"`. It must start with `-` (a regular file), as in `-rw-r--r-- 1 you staff ... compose.yaml`. If it starts with anything else, or `ls` fails, stop: replace the link with a regular file inside the project directory, or set `compose` to such a file.
3. Run `readlink -- "$f"`: it shows where a symbolic link points (a hard link prints nothing). The copy brings that content into the worktree, so stop if you do not want it there.
4. If `compose` is in a subdirectory, run `ls -ld` on each directory in the path, without a trailing slash (for `docs/sub/compose.yaml`: `ls -ld -- docs docs/sub`). `ls -ldL` follows links, so only this shows a link in the path. Each must start with `d`; on anything else, or an `ls` error, stop and fix that link first.
5. Run `[ ! -e "$f.new" ] && [ ! -L "$f.new" ] && cp -- "$f" "$f.new" && mv -- "$f.new" "$f" || echo "not replaced: $f" >&2`. `$f.new` is a temporary name that must not exist, not even as a link. If it prints `not replaced`, nothing was replaced: look at `$f.new` (it was there already, or it is a copy that was not moved), remove it with `rm -- "$f.new"` or move it away if it is yours, and run the line again.
6. Check the result, because no message does not prove it worked. `ls -l -- "$f"` must start with `-`, and its link count (the number after the permissions) must be 1; a hard link that is still there shows 2 or more. `ls -l` does not show whether a directory in the path is a link: for a file in a subdirectory, check the directories again as in step 4.
7. Check that relative `include:`, `extends:` and `env_file:` paths still point where you mean, because `rm` does not check them (see the limits above). The copy is a new file: its mode is the source's under your umask and it has no ACLs or extended attributes, so set them again if you need them.

Your checks and the copy are not one step, so a process writing in the worktree at that moment can still make step 5 write outside it (a link planted as `$f.new`, or a directory in the path swapped for a link). Shutting down the processes first lowers this risk but does not remove it: other tools, git operations and other users can still write there. An outside file overwritten that way leaves no trace in these checks.

## Upgrading

### v0.9.0

**Check before you upgrade.** These can need action:

- **A `.sail-worktree.json` that is now refused.** `up`, `stop` and `rm` fail on it, `status` reports a problem and `check` warns, when its `port_vars` has:
  - a `name` that is not a shell variable name, or a `default` that is missing or outside 1 to 65535 (before, `up` could write other lines into `.env` or assign port 1);
  - a `name` listed twice (in any case);
  - a `name` with a meaning of its own, such as `PATH`, `COMPOSE_FILE` or `DOCKER_HOST` (see [the file](#the-sail-worktreejson-file)).

  Only a hand-written file can list such a name: detection finds none, and other `DOCKER_*` and `COMPOSE_*` variables such as `DOCKER_WEB_PORT` keep working as in v0.8.1. Fix the entry by hand, or run `init`, which replaces the file with what it detects; for a reserved name, rename the variable in the compose file.
- **Scripts that read `ps --json` or `ports --json`.** Main worktree records now appear in the same array (first in `ps`), so a script that expects only worktrees should keep only the objects with `"kind": "worktree"`. In `ports PORT --json`, the row of a port nothing records has `"kind": "none"` and `"state": "-"` (before, `state` was empty). In `ports --json`, `host` can also be `"invalid"`. The other fields keep their names and meanings.
- **Do not run v0.8.1 or earlier at the same time as this version.** v0.8.0 and v0.8.1 break a stale lock by renaming it and remove locks by path, and earlier versions take no lock, so two runs could update the registry at once; the new lock protection works only between runs of this version. The older versions also do not have the Windows path rules below. See the lock note in the [Notes](#notes).
- **An older version drops `mains`.** `registry.json` has a new `mains` section with the main worktrees' ports (`worktrees` is unchanged). An older version that saves the registry (its `up` or `rm`) drops it, and until the next `up` or `check` records it again, the main worktrees' ports are not kept free.
- **Windows projects run from a UNC path** (or from a drive-letter path that goes through a symbolic link or junction to one). `ps` and `ports` treat their registry records as invalid (`up`, `rm` and `status` still work there, and the records still keep their ports free); map a drive letter instead. `check` warns about such a record and `prune` offers to remove it, but removing the record of a project that is still in use frees its ports in the registry until `up`, or for a main worktree also `check` in its project directory, records it again. These rules cover the paths in the registry only; see the [Notes](#notes) for their limits.
- **Worktrees whose names have no ASCII letter or digit** get the project name `wt-<hash>` instead of `-<hash>`, which compose refused. Such a worktree could not start, so nothing is lost; run `up` once before `rm` or `stop` so that `.env` gets the new name. Other names are unchanged.

**Changed behaviour.**

- `up` records the main worktree's ports and no longer assigns them to a worktree. A worktree whose port a recorded main worktree (of any repository) uses gets another port on its next `up`, which prints `APP_PORT=82 (was 81, ...)`.
- `ps` and `ports` also list main worktree records (first in `ps`), and `ports` counts them in `CONFLICT`. `status` reports a port that a main worktree record also holds, and warns about a port variable that the worktree's `.env` sets on more than one line.
- `init` refuses arguments: `init --help` (or `-h`) prints the usage, and any other argument is an error, also next to `--help` (`init --help --bogus`). Before, `init` ignored them and rewrote `.sail-worktree.json`.
- Every command except `version` accepts `-h`/`--help` and only prints the usage (see [Usage](#usage)). For `up` and `stop` only `-h`/`--help` as the first argument counts; before, `up --help` and `stop -h` were passed to sail. For the other commands `-h`/`--help` next to another argument is an error.
- On Windows, a registry path that is not a drive-letter path (a UNC or device path), or that goes through a link to one, is not used, so `ps` and `ports` treat its record as invalid; and with `--all` they do not run git in other repositories, whose entries show as `unattributed`. These rules cover the paths in the registry only; see the [Notes](#notes) for their limits.

**New.**

- Command `check` (see [`check`](#sail-worktree-check)): it records the main worktree of the project you run it in and reports ports used more than once across all repositories.
- Command `prune` (see [`prune`](#sail-worktree-prune--y)): it removes the registry records of directories that no longer exist and the main worktree records that a move left behind, which `check` warns about; before, the registry had to be edited by hand.
- A `kind` field in the `--json` output of `ps` and `ports` (`worktree`, `main`, or `none`; see above for scripts).

### v0.8.0

- New read-only commands `ps`, `ports` and `status` (see [Inspecting worktrees](#inspecting-worktrees)). Nothing changes for the existing commands' options or output.
- `up` and `rm` now take a lock while they update the port registry, so several `up` commands can run at the same time without choosing the same port or dropping each other's entry. While they run, a file `registry.json.lock` exists next to `registry.json`; a run that is killed can leave it behind, and the next run removes it once it is 60 seconds old (see the [Notes](#notes)). The old advice not to run several `up` commands at the same time no longer applies.
- `registry.json` is now replaced atomically instead of being rewritten in place, so a reader never sees a half-written file. The format is unchanged and nothing needs to be migrated. If `registry.json` is a symbolic link, the link is kept and its target is replaced; a hard link is broken.

### v0.7.0

- `.sail-worktree.json` is optional: without it, `up`, `stop` and `rm` detect the compose file and the port variables on every run in a directory with `artisan` and a compose file. Projects that have the file behave as before.
- `init` needs `artisan` next to the compose file: it no longer writes into a directory that has only a compose file (such as `.devcontainer/`). It may print "updated" and replaces hand edits in an existing file.
- Port detection only takes host port mappings (`'${APP_PORT:-80}:80'`) with a default between 1 and 65535, so `init` no longer lists `environment:` entries such as `DB_PORT=${DB_PORT:-3306}`.
- Without the file, ports follow the compose file of the branch checked out in the worktree: a variable that disappears is released on the next `up`.
- `up` checks `vendor/bin/sail` before it writes `.env` or the registry, and prints "created .env" after writing it.
- In the main worktree, "cannot run in the main worktree" now comes before a configuration error, and the not-found error no longer says to run `init`.

### Earlier versions

- Laravel projects in a subdirectory of the repository are supported (see [Project directory](#project-directory)). Projects at the worktree root are unaffected: same project directory, project name and port assignments.
- `init` writes `.sail-worktree.json` next to the nearest compose file from the current directory upwards; it used to write at the worktree root always. Running it in a subdirectory that has its own compose file (such as `.devcontainer/`) therefore writes there (changed in v0.7.0: it now needs `artisan` there).
- `up`, `stop` and `rm` use the nearest `.sail-worktree.json`. A directory with its own committed `.sail-worktree.json` (outside `vendor` and `node_modules`) becomes the project, with its own `.env` and `vendor/bin/sail`, and gets a different project name: the containers of a project at the root are left alone, and `rm` run in that directory removes only that directory's own project (it refuses when that directory has no `.env` from `up`, or one whose name does not match).
- When `init` looks for the compose file, a name that exists but is not a regular file (such as a directory named `compose.yaml`) is now an error instead of being accepted, and `init` refuses a `.sail-worktree.json` that is a link or not a regular file.
- `rm` checks the compose file against the project directory, so for a project in a subdirectory a link to a compose file elsewhere in the worktree is refused.
- `init` prints the created path quoted (`created "<path>"`), and error messages show control characters escaped.
- The worktree path is now resolved through symlinks (for example `/tmp` → `/private/tmp` on macOS), and the project name contains a hash of that path. If you ran `up` through a symlinked path with an older version, the name changes: `up` writes the new name and leaves the old containers and volumes behind, and `rm` refuses with "does not match". Remove the old project by hand (`docker compose -p <old name> down -v`) or put the old name back in `.env`. The ports are kept, though: the port registry is migrated automatically, and entries recorded under a symlinked path are merged into the real path on the next `up`, so the worktree keeps its previous ports (an entry that already exists for the real path wins; entries for paths that no longer exist are left alone). `rm` also releases such alias entries along with the real one, but it only runs once the name matches.
- An existing `SESSION_COOKIE` in a worktree `.env` is overwritten on the next `up`, so you are logged out of that worktree once.
- `up` now appends the worktree's `APP_URL` to `SANCTUM_STATEFUL_DOMAINS` when `.env` sets it (see `up` above), and keeps the brackets of an IPv6 host when it rewrites `APP_URL` (`http://[::1]` used to become the invalid `http://::1:<port>`).
- For `up` and `rm`, a symlinked or hard-linked `.env` is now an error. `up` also rejects `COMPOSE_FILE`, `COMPOSE_ENV_FILES` and `SAIL_FILES` lines in `.env` (also ones copied from the main worktree), and `rm` additionally rejects `COMPOSE_PROFILES` (`up` accepts it). Replace the link with a real file, or delete the line. `stop` no longer refuses these; it only warns.

## Development

```sh
go vet ./...
go test ./...
```

Please write code comments, error messages, commit messages and pull request descriptions in English. Commit subjects follow `feat:`, `fix:`, `refactor:`, `docs:`, `ci:` and `test:` prefixes; the release notes are grouped by them.

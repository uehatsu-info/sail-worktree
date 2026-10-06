[English](README.md) | 日本語

# sail-worktree

Laravel Sail プロジェクトを `git worktree` で複数同時に動かすためのツールです。
ワークツリーごとに `.env` を生成し、ポート番号が衝突しないよう自動で割り当てます。

## インストール

```sh
go install github.com/uehatsu-info/sail-worktree@latest
```

または、リポジトリを clone してビルドします。

```sh
go build -o sail-worktree .
```

## 使い方

### 1. `sail-worktree init`

対象 Laravel プロジェクトのメインワークツリーで実行します。
`compose.yml` の `ports:` から `${APP_PORT:-80}` のようなポート変数を検出し、`.sail-worktree.json` を生成します。
このファイルはコミットして、全ワークツリーで共有してください。

### 2. `sail-worktree up [args...]`

作成済みのワークツリーで実行します。

```sh
git worktree add ../myapp-feature-x feature-x
cd ../myapp-feature-x
composer install
sail-worktree up -d
```

- `.env` がなければメインワークツリーの `.env` をコピーします（なければ `.env.example`）。
- 各ポート変数に空きポートを割り当てます。デフォルト値の +1 から探索し、メインワークツリー用にデフォルト値は空けておきます。
- 他のワークツリーの割り当て済みポートと、ホストで使用中のポートは避けます。
- `COMPOSE_PROJECT_NAME` を設定し、`APP_URL` のポートも更新します。
- `SESSION_COOKIE` を `<COMPOSE_PROJECT_NAME>-session` に設定します（既存の値は上書きします）。`localhost` はポートが違っても Cookie を共有するので、名前を分けないとワークツリー同士のログインが混ざります。
- ポート変数と同名のシェル変数（および `COMPOSE_FILE`・`COMPOSE_PROFILES`・`COMPOSE_ENV_FILES`・`SAIL_FILES`・`COMPOSE_PROJECT_NAME`）は、`.env` より優先されてしまうので、`sail` に渡す環境から外します。
- 割り当て済みのポートは、次回以降も同じ値を再利用します。
- 最後に `vendor/bin/sail up <args>` を実行します。

### 3. `sail-worktree stop`

`sail stop` を実行します。

### 4. `sail-worktree rm [-y]`

コンテナ・ネットワーク・ボリューム（DB データ含む）・ビルドイメージを削除し、割り当てたポートを解放します。
`docker compose --project-name <名前> --project-directory <root> -f <compose> down -v --rmi local --remove-orphans` 相当です。確認プロンプトは `-y` で省略できます。
`.env` は削除しません。
取り返しがつかないので、`.env` の `COMPOSE_PROJECT_NAME` がこのワークツリー用に再計算した名前と一致しないとき（手で書き換えた・別のワークツリーの名前が残っている・ワークツリーを移動した）と、`.env` に `COMPOSE_FILE`・`COMPOSE_PROFILES`・`COMPOSE_ENV_FILES`・`SAIL_FILES` があるときは拒否します。

### 5. `sail-worktree version`

版を表示します（`go install ...@vX.Y.Z` ならタグ、手元ビルドは `(devel)`）。プロジェクトでは `@latest` でなくタグを固定してください。

## 補足

- `up` と `rm` はメインワークツリーでは実行できません。
- `.env` の安全: `up` と `rm` は、`.env` がシンボリックリンクのとき、または他のファイルとハードリンクされているときは拒否します（リンク越しにメインの `.env` を書き換えないため）。新規作成する `.env` は 0600、既存ファイルのモードは変えません。`up` は管理するキーの行を全て置き換えるので、重複行で値が割れることはありません（Sail は最後の値、Laravel の Dotenv は最初の値を使います）。
- ポートは、全インターフェースと `127.0.0.1` の両方で束縛できるときだけ空きと判定します。
- メインワークツリー自身の `.env` のポートはレジストリに載りません。探索はデフォルト値の +1 から始まるので、メインには +1 以外のポート（例: `APP_PORT=8080`）を割り当てるか、メインを先に起動してください。ポートの割り当てはロックしないので、複数の `up` を同時に実行しないでください。
- ポートの割り当て記録は `os.UserConfigDir()/sail-worktree/registry.json` に保存されます（macOS: `~/Library/Application Support/sail-worktree/registry.json`）。
- ワークツリーには `vendor/bin/sail` が必要です（`composer install` 済みであること）。

## 開発

```sh
go vet ./...
go test ./...
```

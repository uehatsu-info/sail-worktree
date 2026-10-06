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
- 割り当て済みのポートは、次回以降も同じ値を再利用します。
- 最後に `vendor/bin/sail up <args>` を実行します。

### 3. `sail-worktree stop`

`sail stop` を実行します。

### 4. `sail-worktree rm [-y]`

コンテナ・ネットワーク・ボリューム（DB データ含む）・ビルドイメージを削除し、割り当てたポートを解放します。
`docker compose down -v --rmi local --remove-orphans` 相当です。確認プロンプトは `-y` で省略できます。
`.env` は削除しません。

## 補足

- `up` と `rm` はメインワークツリーでは実行できません。
- ポートの割り当て記録は `os.UserConfigDir()/sail-worktree/registry.json` に保存されます（macOS: `~/Library/Application Support/sail-worktree/registry.json`）。
- ワークツリーには `vendor/bin/sail` が必要です（`composer install` 済みであること）。

## 開発

```sh
go vet ./...
go test ./...
```

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
- ポート変数と同名のシェル変数・`COMPOSE_*` の全て・`SAIL_FILES` は、`.env` より優先されてしまうので、`sail` に渡す環境（`stop` も同じ）と、`rm` の `docker compose` に渡す環境から外します。`DOCKER_HOST`・`DOCKER_CONTEXT` は意図して使う人がいるので外しません。
- 割り当て済みのポートは、次回以降も同じ値を再利用します。
- 最後に `vendor/bin/sail up <args>` を実行します。

### 3. `sail-worktree stop`

`sail stop` を実行します。

`stop` は何も拒否しません（`.env` を書き換えず、止めるのは取り返しがつくため）。代わりに、メイン以外のワークツリーで `.env`（通常ファイルのみ。リンクや FIFO は読みません）に `COMPOSE_FILE`・`COMPOSE_ENV_FILES`・`SAIL_FILES` があるとき、または `COMPOSE_PROJECT_NAME` がこのワークツリー用に再計算した名前と違うときに、別のプロジェクトを止める可能性があるので、stderr に警告を出します。警告はベストエフォートです（`.env` 内のシェル式や `.env.$APP_ENV` は検出できません）。メインワークツリーでは出しません。

### 4. `sail-worktree rm [-y]`

コンテナ・ネットワーク・ボリューム（DB データ含む）・ビルドイメージを削除し、割り当てたポートを解放します。
`docker compose --project-name <名前> --project-directory <root> -f <compose> down -v --rmi local --remove-orphans` 相当です。確認プロンプトは `-y` で省略できます。
`.env` は削除しません。
取り返しがつかないので、`.env` の `COMPOSE_PROJECT_NAME` がこのワークツリー用に再計算した名前と一致しないとき（手で書き換えた・別のワークツリーの名前が残っている・ワークツリーを移動した）と、`.env` に `COMPOSE_FILE`・`COMPOSE_PROFILES`・`COMPOSE_ENV_FILES`・`SAIL_FILES` があるときは拒否します（`up` は `COMPOSE_PROFILES` を許しますが `rm` は許しません。`rm` の前にその行を消してください）。これらの検査は全て確認プロンプトの前に行います。名前が一致しないときのエラーには復旧手順が出ます。通常は `.env` の `COMPOSE_PROJECT_NAME` を表示された名前（引用符なし）に戻してください。古い版が別の名前で作ったプロジェクトを消す場合だけ、手動の `docker compose -p <名前> down -v ...` を示します（名前が安全な文字種＝小文字英数字・`_`・`-` のときのみ。大文字を含む名前は compose が実際に使う名前と異なり得ます）。実行前に `docker compose ls -a` で、他のワークツリーやプロジェクトのものでなくこのワークツリーのものであることを確認してください。`up` が `COMPOSE_FILE` 等の行を拒否したときのエラーは、それがこのワークツリーの `.env`・メインワークツリーの `.env`・その `.env.example` のどれ由来かと、直し方を示します。

### 5. `sail-worktree version`

版を表示します（`go install ...@vX.Y.Z` ならタグ、手元の `go build` は `(devel)` か `v0.0.0-<日時>-<コミット>` の疑似バージョン）。プロジェクトでは `@latest` でなくタグを固定してください。

## 補足

- `up` と `rm` はメインワークツリーでは実行できません。
- `.env` の安全: `up`・`rm` は、`.env` がシンボリックリンクのとき、または（Unix で）他のファイルとハードリンクされているときは拒否します（リンク越しにメインの `.env` を書き換えないため）。`up` は `COMPOSE_FILE`・`COMPOSE_ENV_FILES`・`SAIL_FILES` があるときも拒否します（あると compose が `up` の書くポートやプロジェクト名を読まなくなります）。`stop` はこの検査をしませんが、Sail は `.env`（`APP_ENV` があれば `.env.$APP_ENV`）をシェルとして `source` するので、シンボリックリンクの `.env` はこのツール経由でも `sail` を直接実行しても、リンク先がシェルとして実行されます。`.env` は信頼できる実ファイルにしてください。新規作成する `.env` は 0600、既存ファイルのモードは変えません（古い版が作った `.env` は自分で `chmod 600 .env` してください）。`up` が書くキーが複数行にあるときは、最初の行を残して残りを取り除くので、重複行で値が割れることはありません（Sail は最後の値、Laravel の Dotenv は最初の値を使います）。
- `.sail-worktree.json` の `compose` は、ワークツリー内の相対パスにしてください（`rm` が `-f` に渡します）。
- ポートは、全インターフェースと `127.0.0.1` の両方で束縛できるときだけ空きと判定します（`127.0.0.1` の権限エラーは無視します）。1024 未満のポートは macOS では割り当て得ますが、Linux の一般ユーザーでは飛ばして 1024 から探します。Windows には `O_NOFOLLOW` がないので、シンボリックリンクの検査だけです。
- メインワークツリー自身の `.env` のポートはレジストリに載りません。探索はデフォルト値の +1 から始まるので、メインには +1 以外のポート（例: `APP_PORT=8080`）を割り当てるか、メインを先に起動してください。ポートの割り当てはロックしないので、複数の `up` を同時に実行しないでください。
- ポートの割り当て記録は `os.UserConfigDir()/sail-worktree/registry.json` に保存されます（macOS: `~/Library/Application Support/sail-worktree/registry.json`）。
- ワークツリーには `vendor/bin/sail` が必要です（`composer install` 済みであること）。

## アップグレード

- ワークツリーのパスはシンボリックリンクを解決した実パスになりました（例: macOS の `/tmp` → `/private/tmp`）。プロジェクト名にはそのパスのハッシュが入ります。古い版でシンボリックリンク経由のパスから `up` していた場合は名前が変わり、`up` は新しい名前を書いて旧コンテナ・ボリュームを残し、`rm` は「一致しません」で拒否します。旧プロジェクトは手で消す（`docker compose -p <旧名> down -v`）か、`.env` に旧名を戻してください。ただしポートは引き継がれます。ポートのレジストリは自動で移行され、シンボリックリンク経由のパスで記録されたエントリは次の `up` で実パスのエントリに統合されるので、そのワークツリーは以前のポートのままです（実パスに既にエントリがあればそちらを優先し、存在しないパスのエントリは触りません）。`rm` もそうした別名のエントリを実パスのエントリと一緒に解放しますが、`rm` が動くのは名前が一致したときだけです。
- ワークツリーの `.env` に既にある `SESSION_COOKIE` は次の `up` で上書きされるので、そのワークツリーのログインは一度切れます。
- `up` と `rm` は、`.env` がシンボリックリンク・ハードリンクのときエラーになります。また `up` は `.env` に `COMPOSE_FILE`・`COMPOSE_ENV_FILES`・`SAIL_FILES` の行があるとき（メインワークツリーの `.env` からコピーされたものを含む）、`rm` はさらに `COMPOSE_PROFILES` の行があるときもエラーにします（`up` は `COMPOSE_PROFILES` を許します）。リンクを実ファイルに置き換えるか、その行を消してください。`stop` はこれらを拒否せず、警告だけ出します。

## 開発

```sh
go vet ./...
go test ./...
```

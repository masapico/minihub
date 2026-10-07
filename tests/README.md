# 検証手順

## Goの基本チェック

AI連携は`go test ./internal/service ./internal/config ./internal/httpapi ./internal/storage/backend ./internal/storage/sqlitestore -run AI`でモックAPI、履歴上限、タイムアウト、待機上限、停止、保存・移行・復元を検証します。共通試験は`MINIHUB_TEST_STORAGE=sqlite`でも実行してください。

`node tests/ai-browser.cjs <実行ファイル>`はPlaywrightとChromium系ブラウザで、独立した一時サーバー・モックAIを使い、ロボットボタン、候補検索・キーボード挿入、下書き復元、通常投稿と返信の履歴、回答表示、狭い画面を確認します。`--sqlite`でSQLiteと`/hub`配備、`--disabled`でAI未設定時の非表示を確認します。`NODE_PATH`と`BROWSER_PATH`は既存ブラウザ試験と同様です。file方式の有効モードは`docs/images/ai.png`も更新します。既存データは使用しません。

```bash
go test ./...
go vet ./...
go test -race ./...
```

`go test -race`にはCGOとCコンパイラーが必要です。対応する環境で実行してください。

## ベースパスとリバースプロキシ

`node --test tests/urls.test.mjs` は追加ライブラリなしで、URL生成とログインの戻り先の検証を実行します。Goテストでは設定の検証・正規化、公開パスの境界、静的アセットとページ内リンク、認証リダイレクトを確認します。

`base-path-browser.cjs` は一時データの実サーバーと、パス・Hostを保持するHTTP／WebSocketプロキシでブラウザ操作を検証します。Playwrightが `NODE_PATH` 上に必要です。`BROWSER_PATH` でChromium系ブラウザを指定できます。Windowsでは省略時にEdgeを使用します。

```powershell
go build -o .tmp/minihub-base-path.exe ./cmd/minihub
$env:NODE_PATH = (Resolve-Path '.tmp/ui-review/node_modules').Path
node tests/base-path-browser.cjs .tmp/minihub-base-path.exe
```

ドメイン直下と `/hub` をfile方式、`/apps/chat` をSQLite方式・CLIによる設定上書きで確認します。ログイン後の詳細画面への復帰、予定調整の作成・編集・公開・一覧の期間切替と戻る操作、チャット内リンク・別タブでの作成、Cookieの送信範囲とログアウト、静的アセット、リアルタイム投稿、WebSocket再接続での取り逃し取得を検証します。予定調整のシナリオには専用の一時データだけを使用し、終了時に削除します。

## ブラウザ検証

## READMEの画面画像を更新する

Node.js 24以降とChromium系ブラウザを使用します。npmパッケージのインストールは不要です。リポジトリのルートから実行してください。

```powershell
go build -o .tmp/minihub-readme.exe ./cmd/minihub
$env:BROWSER_PATH = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
node scripts/capture-readme.mjs .tmp/minihub-readme.exe
```

macOS／Linuxでは、実行ファイル名とブラウザのパスを環境に合わせます。

```bash
go build -o /tmp/minihub-readme ./cmd/minihub
BROWSER_PATH=/usr/bin/chromium node scripts/capture-readme.mjs /tmp/minihub-readme
```

ブラウザの既定値はWindowsでMicrosoft Edge、macOSでGoogle Chrome、Linuxで`chromium`です。専用の一時データとローカルサーバーで架空の会話・アンケート・予定調整を作成し、一般ユーザーの実画面を日本語・日本時間、1440×1000、倍率1で撮影します。予定の候補日は撮影日の翌週に生成します。既存の設定・データは使用しません。

予定調整は集計表が見切れないようページ全体を撮影します。3画面の撮影がすべて成功すると `docs/images/chat.png`、`poll.png`、`schedule.png` を更新します。撮影に失敗した場合は掲載画像を更新せず、起動したサーバー・ブラウザと一時データを片付けます。

画面を変更した際は再撮影し、日本語の読みやすさ、内容の見切れ、スレッド・回答・集計の表示とREADMEの画像リンクを確認して、変更した画像をコミットしてください。

OS通知のブラウザ検証は `node tests/os-notifications-browser.mjs <ビルドした実行ファイル>` で実行します。`--disabled` を付けると、実設定でOS通知を禁止し、理由表示、保存済みON設定があっても権限要求・通知生成・通知用ロックがないこと、アプリ内通知と未読表示の継続を検証します。両方のモードを実行してください。`BROWSER_PATH` でChromium系ブラウザを指定できます。

`channel-membership-browser.mjs` は一時データの実サーバーとChromeで、ログイン失敗時のエラー表示・再試行、公開チャンネル未参加時の未読表示・参加ボタン、参加後の未読を検証します。チャンネル検索では、名前・ID・全角英数字による検索、非公開チャンネルの認可、一覧更新後の検索保持、折りたたみの復元、未読件数、デスクトップ／スマートフォン幅も確認します。

```bash
go build -o /private/tmp/minihub-membership ./cmd/minihub
node tests/channel-membership-browser.mjs /private/tmp/minihub-membership
```

`channel-management.cjs` は Playwright の HTTP fixture を利用し、実サーバーや実データを変更せずにチャンネル管理画面を検証します。500ユーザー、検索・並べ替え、複数選択の保持、管理者の保護、保存失敗・再試行、権限移譲、アーカイブ、画面幅を確認します。

Playwright を利用できる環境でリポジトリのルートから実行してください。別の場所にインストール済みの場合は `NODE_PATH` にその `node_modules` を指定します。`BROWSER_PATH` は任意のChromium系ブラウザ実行ファイル、`SCREENSHOT_DIR` は任意のスクリーンショット出力先です。

```powershell
$env:NODE_PATH = (Resolve-Path '.tmp/ui-review/node_modules').Path
$env:BROWSER_PATH = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
node tests/channel-management.cjs
```

## スレッド返信

`threads-browser.mjs` はNode.js標準機能とChromeのCDPを使い、一時データの実サーバーで返信機能を検証します。Node.jsの `fetch` / `WebSocket` が使えるバージョンとChromium系ブラウザが必要です。`BROWSER_PATH` でブラウザ実行ファイルを指定できます。既定値はmacOSのGoogle Chromeです。

```bash
go build -o /private/tmp/minihub-threads ./cmd/minihub
node tests/threads-browser.mjs /private/tmp/minihub-threads
```

60件の返信のページング、本文の遅延取得、通常未読・スレッド既読・メンション確認の分離、下書き保持、安全な本文描画、デスクトップ／スマートフォン幅、返信更新一覧、WebSocket再接続を確認します。スクリーンショットは出力された一時ディレクトリに残ります。

負荷検証は通常のテストから分離し、次の環境変数で有効にします。データはテスト用一時ディレクトリに生成されます。

```bash
THREAD_BENCH_MESSAGES=1000000 go test ./internal/storage/filestore -run TestThreadIndexLoadProfile -count=1 -v
THREAD_CONNECTION_PROFILE=1 go test ./internal/realtime -run TestThreadConnectionProfile -count=1 -v
```

前者は索引構築・ヒープ差分・通常履歴・返信取得・要約・追記を測定します。後者は実サービスの認可を通る500ユーザーのWebSocketへ10返信／秒を60秒配信します。500ブラウザのHTTP再取得や500人の同時投稿を模擬したものではありません。

返信ブラウザテストは参加者メンションの名前検索・挿入・取消／やり直し・通知先・取得失敗／遅延応答・グループ経由参加・500人候補表示も検証します。IMEは合成イベントであり、実機IME・タッチ操作の確認は別途行ってください。
接続状態の表示は `presence-browser.cjs` で通常投稿とスレッド返信、複数タブ、最後の切断を確認します。Playwright を利用できる環境で、`NODE_PATH` と `BROWSER_PATH` を設定し、ビルドした実行ファイルの絶対パスを渡して実行してください。500人の同時購読は `PRESENCE_CONNECTION_PROFILE=1 go test ./internal/realtime -run TestPresenceConnectionProfile -count=1 -v` で検証できます。
同テストは返信の見たまま書式、書式付き下書きの復元、リンク、16 KiB制限、プレーンテキスト貼り付け、省スペース表示も検証します。
本流では、返信ボタンがリアクション操作行の末尾に並ぶことと、狭い画面で操作行が横にはみ出さないことも検証します。

現在のリアクションボタンは「了解」「完了」「確認中」「感謝」の4種類です。API／保存データのキーは順に `ack`、`done`、`eyes`、`thanks` です。`eyes` は互換性のためのキー名で、画面には「確認中」の砂時計アイコンを表示します。返信ブラウザテストは `ack` の追加後、リアクション数と返信ボタンの配置を確認します。

## SQLite共通試験と移行試験

通常の`go test ./...`はfile版のサービス試験に加え、SQLite固有の比較・採番・クラッシュ復旧試験、移行・バックアップ復元試験を実行します。サービス／HTTP／初期管理者／WebSocketの共通試験をSQLiteで実行する場合は次のようにします。

```powershell
$env:MINIHUB_TEST_STORAGE = 'sqlite'
go test ./...
go test -race ./...
Remove-Item Env:MINIHUB_TEST_STORAGE
```

`-race`にはCGOとCコンパイラーが必要です。Windowsでは対応するMinGW-w64系コンパイラーを`CC`へ指定し、`CGO_ENABLED=1`で実行します。通常配布用ビルドにはCGOは不要です。

```powershell
$env:THREAD_BENCH_MESSAGES = '3650000' # または1000000
go test ./internal/storage/sqlitestore -run '^TestHistoryProfile$' -count=1 -v -timeout=20m
go test ./internal/storage/filestore -run '^TestThreadIndexLoadProfile$' -count=1 -v -timeout=10m
Remove-Item Env:THREAD_BENCH_MESSAGES

$env:THREAD_CONNECTION_PROFILE = '1'
$env:THREAD_MIXED_PROFILE = '1'
$env:MINIHUB_TEST_STORAGE = 'sqlite' # fileの場合はこの変数を設定しない
go test ./internal/realtime -run '^TestThreadConnectionProfile$' -count=1 -v -timeout=5m
Remove-Item Env:THREAD_CONNECTION_PROFILE,Env:THREAD_MIXED_PROFILE,Env:MINIHUB_TEST_STORAGE
```

大量履歴は従来と同じ分布（20投稿者、30%返信、10件ごとの元発言へ3返信）です。SQLiteは生成後に接続を閉じて開き直し、永続索引による初回ページ取得を測ります。fileはJSONLから初回メモリ索引を構築します。両者ともOSキャッシュが暖まった開発環境での測定です。SQLiteのGoヒープ差分はDBのページキャッシュやプロセスRSS全体の測定ではありません。

混在試験は500接続・10返信／秒・合計600返信に加え、1秒ごとに10人のスレッド既読を並行更新し、リアクションを1件追加します。合計600件の既読更新と60件のリアクション、600件の返信通知の順序と全受信を確認します。HTTPの500ブラウザ同時再取得や数百万件の履歴との組み合わせを模擬したものではありません。

## アンケート

`polls-browser.mjs` は一時データの実サーバーとChromeで、アンケートの作成、回答変更、匿名集計、一覧モーダルの削除、チャンネル情報内でのアンケート、スマートフォン幅を検証します。

```bash
go build -o /private/tmp/minihub-polls-browser ./cmd/minihub
node tests/polls-browser.mjs /private/tmp/minihub-polls-browser
```

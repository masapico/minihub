# minihub 開発・保守資料

この資料は、開発・保守担当が現行実装の構成、データ、処理の流れを追うためのものです。図は Mermaid で記述し、GitHub の Markdown 表示で閲覧できます。仕様の詳細は [DESIGN.md](../DESIGN.md)、利用手順は [利用者マニュアル](user-manual.md)と[管理者マニュアル](admin-manual.md)を参照してください。

| 資料 | 内容 |
| --- | --- |
| [APIリファレンス](api.md) | 認証、curl使用例、エンドポイント、ページング、リアルタイムイベント |
| [アーキテクチャ](architecture.md) | 配置、コンポーネント、認証・認可、ストレージ選択 |
| [データモデル](data-model.md) | 論理ER図、SQLiteの保存構造、file方式の配置 |
| [処理フロー](workflows.md) | 投稿、既読、再接続、管理、投票・予定、移行 |
| [AI連携](ai.md) | AIアカウント、送信する履歴、外部APIのHTTP契約 |
| [保存方式とSQLiteへの移行](sqlite.md) | 保存方式の選択、停止中の移行、エラー時の復旧、バックアップ |
| [投稿の物理削除](retention-proposal.md) | 削除範囲、停止中の実行手順、復旧 |
| [通信負荷の見積もり](network-load.md) | 500同時接続時の概算、配信・HTTP取得の負荷、導入前の実測 |
| [リリースビルドと手動公開](releases.md) | 4種の配布物、ビルドスクリプト、検証、GitHub Releasesへの登録 |

図を更新するときは `internal/domain`、`internal/service`、`internal/storage`、`internal/httpapi`、`internal/realtime` と照合してください。


## 操作資料とHTML版

開発・保守用の資料はMarkdownで管理し、GitHubなどで閲覧できます。利用者・管理者へ配布する操作資料は[HTML版の一覧](../docs_html/index.html)にまとめています。`docs_html`フォルダーを丸ごとコピーし、`index.html`をブラウザで開くと閲覧できます。

| 資料 | Markdown | HTML |
| --- | --- | --- |
| ユーザー向け操作マニュアル | [Markdown](user-manual.md) | [HTML](../docs_html/user-manual.html) |
| 管理者向け操作マニュアル | [Markdown](admin-manual.md) | [HTML](../docs_html/admin-manual.html) |
| ブラウザAPIを使ったOS通知の設定 | [Markdown](browser-notifications.md) | [HTML](../docs_html/browser-notifications.html) |
| 保存方式とSQLiteへの移行 | [Markdown](sqlite.md) | [HTML](../docs_html/sqlite.html) |
| AIアカウントと外部API連携 | [Markdown](ai.md) | [HTML](../docs_html/ai.html) |
| 投稿の物理削除 | [Markdown](retention-proposal.md) | [HTML](../docs_html/retention-proposal.html) |

操作資料の内容を変更するときは、対応するMarkdownとHTMLの両方を更新してください。HTML版の内部参照はHTMLページへリンクし、見出しのIDと目次も合わせて更新します。HTTP／HTTPSの手順はREADMEと管理者マニュアルの両形式を合わせて更新してください。HTMLは静的ファイルで、外部CSS・JavaScriptやCDNに依存しません。設計・データモデル・処理フロー・通信負荷の資料はMarkdownのみです。


[検証手順と画面画像の更新](../tests/README.md)も参照してください。

## AI連携

[AIアカウントと外部API連携](ai.md)に設定、送信する履歴、HTTP契約、制限、停止時の動作をまとめています。

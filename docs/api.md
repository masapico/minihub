# minihub APIリファレンス

[開発・保守資料一覧](README.md) / [設計仕様](../DESIGN.md)

## 認証と使用例

```bash
base_url=http://127.0.0.1:8080
cookie_jar=$(mktemp)
read -rsp '管理者パスワード: ' password; echo
login=$(curl --silent --show-error --fail-with-body -c "$cookie_jar" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg password "$password" '{userId:"admin",password:$password}')" \
  "$base_url/api/auth/login")
unset password
csrf_token=$(printf '%s' "$login" | jq -r '.csrfToken')

curl --silent --show-error --fail-with-body -b "$cookie_jar" \
  -H "X-CSRF-Token: $csrf_token" \
  -H 'Content-Type: application/json' \
  -d '{"id":"general","name":"全社連絡","type":"public"}' \
  "$base_url/api/channels"

rm -f "$cookie_jar"
```

この例は `curl` と `jq` を使います。Cookieを一時ファイルへ保存し、ログイン応答の `csrfToken` を更新系APIへ送ります。`general` が既にある場合、作成APIは失敗します。Windows PowerShell では `curl.exe` を使用してください。

## エンドポイント一覧

| メソッド | パス | 用途 |
|---|---|---|
| `POST` | `/api/auth/login` | ログインとセッション開始 |
| `POST` | `/api/auth/logout` | ログアウト |
| `GET` | `/api/me` | ログインユーザー取得 |
| `PUT` | `/api/me/password` | ログインユーザーのパスワード変更 |
| `GET` | `/api/mentions?cursor={cursor}&limit=50` | メンション一覧 |
| `PUT` | `/api/mentions/{messageId}/read` | メンションを個別確認 |
| `GET` | `/api/users` | ユーザー一覧（管理者のみ） |
| `POST` | `/api/users` | ユーザー登録（管理者のみ） |
| `PUT` | `/api/users/{id}` | ユーザー設定変更（管理者のみ） |
| `GET` | `/api/groups/directory` | チャンネル割当用グループ一覧 |
| `GET` | `/api/channels` | 閲覧可能チャネル一覧 |
| `GET` | `/api/channels/read-statuses` | 閲覧可能な全チャネルの既読・未読状態 |
| `POST` | `/api/channels` | チャネル作成（管理者のみ） |
| `GET` | `/api/channels/{id}` | チャネル取得 |
| `GET` | `/api/channels?management=true&includeArchived=true` | 管理可能なチャネル一覧 |
| `GET` | `/api/channels/{id}/management` | チャネル管理情報 |
| `PATCH` | `/api/channels/{id}` | 表示名・種別変更（全体管理者のみ） |
| `PATCH` | `/api/channels/{id}/members` | メンバー・グループ・チャネル管理者の差分更新 |
| `POST` | `/api/channels/{id}/archive` | アーカイブ（全体管理者のみ） |
| `POST` | `/api/channels/{id}/restore` | 復元（全体管理者のみ） |
| `POST` | `/api/channels/{id}/join` | 公開チャネルへ参加 |
| `POST` | `/api/channels/{id}/leave` | 公開チャネルから退出 |
| `GET` | `/api/channels/{id}/messages?limit=100` | 最新メッセージ取得 |
| `GET` | `/api/channels/{id}/messages?before={seq}&limit=100` | 過去方向のメッセージ取得 |
| `GET` | `/api/channels/{id}/messages?after={seq}&limit=100` | 前方向のメッセージ取得 |
| `GET` | `/api/channels/{id}/messages?around={seq}&limit=100` | 指定メッセージ周辺の取得 |
| `POST` | `/api/channels/{id}/messages` | メッセージ投稿 |
| `POST` | `/api/channels/{id}/messages/{seq}/withdraw` | メッセージの取り消し |
| `POST` | `/api/channels/{id}/messages/{seq}/restore` | メッセージの取り消しを戻す |
| `GET` | `/api/channels/{id}/reactions?afterMessageSeq=0` | リアクション状態取得 |
| `GET` | `/api/channels/{id}/reactions?messageSeqs=12,15` | 指定したメッセージのリアクション状態取得（最大101件、`afterMessageSeq` と併用不可） |
| `GET` | `/api/channels/{id}/messages/{seq}/reactions/{key}/users` | リアクションしたユーザーの取得 |
| `PUT` | `/api/channels/{id}/messages/{seq}/reactions/{key}` | リアクション追加 |
| `DELETE` | `/api/channels/{id}/messages/{seq}/reactions/{key}` | リアクション解除 |
| `PUT` | `/api/channels/{id}/read` | 既読位置更新 |
| `GET` | `/api/polls?channelId={id}&active=true` | 閲覧可能なアンケートと匿名集計の一覧 |
| `POST` | `/api/polls` | アンケートの作成・公開。同一 `requestId` の再送は同じアンケートを返す |
| `GET` | `/api/polls/{id}` | 匿名集計と自分の回答の取得 |
| `POST` | `/api/polls/{id}/withdraw` | アンケートの取り消し |
| `POST` | `/api/polls/{id}/restore` | アンケートの取り消しを戻す |
| `PUT` | `/api/polls/{id}/vote` | 自分の回答の作成・変更（`revision` は前回回答の連番、初回は0） |
| `POST` | `/api/polls/{id}/close` | 作成者または管理者による締切 |
| `GET` | `/api/schedules?cursor={cursor}&limit=50` | 閲覧可能な予定調整一覧 |
| `POST` | `/api/schedules` | 予定調整の下書き作成 |
| `GET` | `/api/schedules/{id}` | 予定、回答、集計の取得 |
| `PUT` | `/api/schedules/{id}` | 予定調整の編集 |
| `POST` | `/api/schedules/{id}/publish` | 元チャンネルへ公開 |
| `POST` | `/api/schedules/{id}/close` | 回答の締切 |
| `POST` | `/api/schedules/{id}/reopen` | 回答の再開 |
| `POST` | `/api/schedules/{id}/finalize` | 日程確定と結果投稿 |
| `POST` | `/api/schedules/{id}/withdraw` | 公開済み予定調整の取り消し |
| `POST` | `/api/schedules/{id}/restore` | 予定調整の取り消しを戻す |
| `PUT` | `/api/schedules/{id}/responses/me` | ログインユーザーの回答保存 |
| `GET` | `/api/realtime` | WebSocketリアルタイム通知 |

## 認可とチャンネル管理

チャネル作成者は最初のチャネル管理者になります。チャネル管理者は参加ユーザー・参加グループを変更し、別の有効ユーザーへ管理権限を移譲できます。管理者は必ず個別参加メンバーでもあり、最後のチャネル管理者は解任できません。表示名・公開種別の変更とアーカイブ・復元は全体管理者に限定されます。

## リアクション

リアクションキーは `ack`（了解・親指）、`done`（完了・チェック）、`eyes`（確認中・砂時計）、`thanks`（感謝・ハート）の4種類です。`eyes` は既存のAPI／保存データのキーであり、画面上の「確認中」ボタンは砂時計アイコンです。通常投稿とスレッド返信で同じ操作を使います。公開チャネルでも、追加・解除にはチャネルへの参加が必要です。

## 履歴のページング

メッセージ取得結果はseq昇順で、次のページ形式です。`before` と `after` は排他的な境界であり、継続先がないカーソルは `null` になります。

```json
{"messages":[{"seq":42,"id":"...","ts":"2026-09-09T10:00:00+09:00","userId":"alice","text":"hello"}],"nextBefore":42,"nextAfter":null}
```

## リアルタイムイベント

WebSocketはメッセージ本文ではなく、次のような軽量な更新通知を送ります。画面は通知を受信するとHTTP APIから不足メッセージを取得します。

```json
{"type":"new_message","channelId":"general","seq":42}
{"type":"message_changed","channelId":"general","seq":42}
{"type":"reaction_changed","channelId":"general","messageSeq":42}
{"type":"schedule_changed","channelId":"general","scheduleId":"...","revision":3}
{"type":"channels_changed"}
```

入力中表示では、クライアントから `typing_start` / `typing_stop` とチャンネルIDを送り、返信入力の場合は対象の `threadRootSeq` も付けます。サーバーは認証済みユーザーIDと接続単位の識別子を付け、同じチャンネルを閲覧できる他ユーザーへ配信します。通常投稿と返信はそれぞれの入力欄に分けて表示され、返信は同じスレッドを開いている場合だけ見えます。入力本文は送信・保存されず、通知が途切れた場合も画面側で約5秒後に表示を消します。

## 予定調整の期間指定

APIは `GET /api/schedules?period=upcoming|past|all` で期間を選べ、省略時は全件です。

接続が切れた場合、画面は2秒後に再接続し、現在表示しているチャネルの最終取得シーケンス以降を `nextAfter` がなくなるまで取得します。

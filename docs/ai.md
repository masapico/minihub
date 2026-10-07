# AIアカウントと外部API連携

minihubは、明示的なAIメンションを含む投稿を保存した後に、OpenAI互換のChat Completions APIへJSONをPOSTします。llama.cppの`llama-server`などへ直接接続でき、回答を呼び出し元投稿のスレッドへ保存します。

## 設定

`minihub.json`のトップレベルに`aiAccounts`を追加して再起動します。省略・空配列は無効です。

```json
{
  "aiAccounts": [
    {
      "id": "assistant",
      "name": "社内アシスタント",
      "enabled": true,
      "url": "http://ai-server:8080/v1/chat/completions",
      "model": "local-model",
      "systemPrompt": "日本語で簡潔に回答してください。",
      "timeout": "120s",
      "token": "YOUR-AI-TOKEN"
    }
  ]
}
```

| 設定 | 内容 |
| --- | --- |
| `id` | 一意の英数字・`_`・`-`、先頭は英数字、1〜61文字 |
| `name` | 空白のみを除く1〜100文字の表示名 |
| `enabled` | 省略時`true`。`false`のAIは候補にも送信先にも含めない |
| `url` | 必須。Chat Completionsの完全なHTTP／HTTPS URL。パスは自動追加しない。URL内の認証情報・フラグメントは禁止 |
| `model` | 必須。接続先で利用できるモデル名。未指定・空白のみは設定エラー |
| `systemPrompt` | 任意。先頭の`system`メッセージとして送る指示文。省略・空白のみなら送らない |
| `timeout` | 依頼処理の制限時間。Goのduration形式。省略時`120s`、正の値 |
| `token` | 任意。Bearerトークンを直接記載。`Bearer `は付けず、値を`Authorization: Bearer <値>`として送信。空文字・省略時は認証なし |
| `tokenEnv` | 任意。トークンを格納した環境変数名。`token`との同時指定は不可 |

認証が必要な場合は、`token`にトークンを直接記載します。空白のみや改行を含む値は指定できません。設定ファイルはGit管理せず、管理者と実行アカウントだけが読み取れる権限にしてください。Windows以外では、`token`を含む設定ファイルに`chmod 600 minihub.json`を適用します。`tokenEnv`も利用でき、有効なAIに指定した環境変数が未設定・空の場合は起動できません。`token`と`tokenEnv`の両方に空でない値を指定すると設定エラーになります。URL・認証情報・モデル名・指示文はブラウザへ返しません。HTTPSの証明書は通常どおり検証し、リダイレクトは追跡しません。

全チャンネルの投稿可能なユーザーがAIを利用できます。非公開チャンネルの内容も設定APIへ送信されます。接続先には、この利用範囲を前提としたAPIを指定してください。temperatureや出力トークン上限は送らず、接続先の既定値を使います。モデルはテキストのChat Completionsと、指示文を指定する場合は`system`ロールに対応している必要があります。

### llama.cppの接続例

手元のGGUFモデルを`llama-server`で起動する例です。実行ファイル名とモデルのパスは環境に合わせて指定します。

```sh
llama-server -m models/model.gguf --alias local-model --host 127.0.0.1 --port 8081
```

同じサーバー上のminihubには`url: "http://127.0.0.1:8081/v1/chat/completions"`と`model: "local-model"`を指定し、認証を設定していなければ`token`は省略します。`model`は`--alias`に一致させます。別マシンから接続する場合は、llama-serverの待受アドレスと接続先URLをその環境に合わせます。

OpenAIへ接続する場合の`url`は`https://api.openai.com/v1/chat/completions`です。利用可能なモデル名とAPIキーを設定します。APIの詳細は[OpenAI公式資料](https://developers.openai.com/api/reference/resources/chat)と[llama.cpp公式資料](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)を参照してください。

## 呼び出しと履歴

通常投稿・返信のロボットボタンでAIを選ぶと、`@ai:assistant（社内アシスタント）`が挿入されます。`@ai:assistant`を直接入力しても呼び出せます。人・グループへのメンションとは別です。同じ投稿で同じAIを複数回指定しても依頼は1件です。異なるAIを指定するとそれぞれへ送信します。

通常投稿からは、その投稿だけを送信します。スレッド内からは、親投稿と呼び出し投稿までの返信を連番順で送信します。取り消された投稿の本文、AIのエラー通知、その後の新しい返信は含めません。

呼び出し先AI自身の過去の回答は`assistant`、人と別AIの投稿は`user`にします。`user`の本文には`表示名 (ユーザーID):`と改行を前置します（別AIのIDは`ai_<設定ID>`）。投稿者名が取得できない場合はIDを使います。保存本文のメンションや書式の記法は維持し、`assistant`の本文には投稿者情報を前置しません。

履歴は親投稿込み1,000件、指示文を含む依頼JSONはUTF-8で1 MiBまでです。取り消された投稿とAIのエラー通知も走査件数上限の対象になります。上限超過時は一部を省略せず送信を中止し、短い新規投稿からの呼び出しを案内します。接続先モデルのコンテキスト上限は別に適用され、超過によるAPIエラーも定型エラーとして通知します。

## OpenAI互換のHTTP契約

設定した`url`へ`Content-Type: application/json`、`Accept: application/json`と依頼IDを示す`X-MiniHub-Request-ID`を設定してPOSTします。ストリーミングは使いません。

```json
{
  "model": "local-model",
  "stream": false,
  "messages": [
    {
      "role": "system",
      "content": "日本語で簡潔に回答してください。"
    },
    {
      "role": "user",
      "content": "Alice (alice):\n@ai:assistant（社内アシスタント） この案を整理してください"
    }
  ]
}
```

回答は最初の選択肢の`choices[0].message.content`から取得します。その他の応答フィールドは許容します。例：

```json
{"choices":[{"index":0,"message":{"role":"assistant","content":"回答"},"finish_reason":"stop"}]}
```

| 応答 | minihubの動作 |
| --- | --- |
| `200 application/json`、最初の`message.content`が空白のみでない文字列 | 同じスレッドへAI名義で投稿 |
| choices欠落・空配列、content欠落・null・型違い・空白のみ、不正JSON | 同じスレッドへ定型エラーを投稿 |
| 204を含む200以外、JSON以外、サイズ超過、タイムアウト | 同じスレッドへ定型エラーを投稿 |

応答JSONは1 MiB、回答本文はUTF-8で16 KiB以内です。外部APIのエラー本文は表示・記録しません。AI回答は既存の安全な本文描画に従い、回答内の人・AIへのメンションは通知や追加実行を発生させません。

## 実行・停止・保存

同時実行は全体で4件、待機は32件です。待機上限でも利用者の元投稿は保存され、AIへの依頼だけがエラーになります。回答は完了順に保存します。送信直前と回答保存直前に投稿権限と取り消し状態を確認し、権限喪失・アーカイブ・元投稿または親投稿の取り消しがある場合は中止します。

依頼はメモリ上だけで管理します。停止時に実行中・待機中の依頼をキャンセルし、再起動後は再開しません。取り消した投稿の復元でも再実行しません。自動再送・ストリーミング・コールバックはありません。タイムアウトや停止でも外部側で回答生成済みの場合があります。Responses API、ツール実行、画像・音声は対象外です。

AIはログインユーザーとして登録しません。投稿の`userId`は`ai_<設定ID>`で、`ai_`は予約接頭辞です。AI有効化時に既存ユーザーにこの接頭辞があれば起動を拒否します。AI投稿には表示名、依頼ID、呼び出し投稿ID、依頼者ID、回答／エラーの種別を保存し、設定削除後も表示できます。取り消し・復元は管理者または権限を持つチャンネル管理者が行います。

file方式はJSONLの任意項目`ai`、SQLiteはスキーマv4のnullableな`ai`列に保存します。既存SQLiteは起動時にv4へ更新します。更新前に停止してデータディレクトリ全体をバックアップしてください。旧バイナリへ戻す場合は更新前バックアップを使用します。AIメタデータは移行・復元・保存期間による削除でも通常投稿と一体で扱います。

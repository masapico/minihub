# minihub × TEGO-core 添付ファイル連携仕様・インターフェース規約書

本ドキュメントは、業務チャットツール **minihub** において、ユーザーがAIメンション時にファイルを添付した場合、バックエンド **TEGO-core** に対してどのような形式でデータを渡すべきかを定義した連携インターフェース規約書です。

---

## 1. 基本方針

1. **OpenAI Chat Completions 互換プロトコルの維持**
   - minihub の既存 AI 連携仕様（`POST /v1/chat/completions`）のデータ構造（JSON スキーマ）を変更せず、そのまま利用します。
2. **メッセージ本文（`content`）への参照メタデータ埋め込み方式**
   - minihub は投稿添付ファイルをサーバー上のストレージに保存した上で、ユーザー発言の `content` 内に「添付ファイルのアクセス情報」を所定の記法で埋め込んで送信します。
3. **TEGO-core 側での自動抽出 & テキスト化**
   - TEGO-core は本文中の添付ファイル記法を検知し、Microsoft `markitdown` を用いて Markdown 構造化テキストへ自動変換した上で、LLM（Amazon Bedrock）のコンテキストに注入します。

---

## 2. HTTP リクエスト仕様

### 2.1 リクエストヘッダー
| ヘッダー名 | 必須 | 内容・例 |
| :--- | :---: | :--- |
| `Content-Type` | 必須 | `application/json` |
| `Accept` | 必須 | `application/json` |
| `X-MiniHub-Request-ID` | 必須 | minihub の一意な依頼ID（UUID等、ログ追跡用） |
| `Authorization` | 任意 | `minihub.json` にトークンが設定されている場合、`Bearer <token>` |

### 2.2 エンドポイント
- `POST {TEGO_CORE_BASE_URL}/v1/chat/completions`
  - （例: `http://127.0.0.1:8000/v1/chat/completions`）

---

## 3. 添付ファイル埋め込み記法規約

ユーザーメッセージ（`role: "user"`）の `content` 末尾に、以下のタグを付与してください。

### ① 単一ファイルの場合
```
[添付ファイル: <ファイルパスまたは内部URL>]
```
または
```
[添付: <ファイルパスまたは内部URL>]
```

### ② 複数ファイルの場合
改行区切りで複数指定が可能です。
```
[添付ファイル: <ファイルパス1>]
[添付ファイル: <ファイルパス2>]
```

---

## 4. ファイルパスの指定基準

minihub から TEGO-core へ渡すパスは、以下のいずれかの形式とします。

| 方式 | パス形式の例 | 推奨ユースケース | 注意点 |
| :--- | :--- | :--- | :--- |
| **A. ローカル絶対パス (推奨)** | `C:/minihub/storage/attachments/202610/uuid_report.xlsx` | minihub と TEGO-core が同一 Windows Server 上で動作する場合 | Windows のバックスラッシュ（`\`）およびスラッシュ（`/`）の双方向に対応。TEGO-core サービス実行権限で読み取り可能であること。 |
| **B. 内部 HTTP/HTTPS URL** | `http://127.0.0.1:8080/api/files/download/uuid_report.pdf` | minihub と TEGO-core が別サーバーまたはコンテナ分離されている場合 | 認証不要で内部ネットワークから直接取得できるエンドポイントであること。 |

---

## 5. 対応ファイル形式

TEGO-core 内蔵の `markitdown` エンジンにより、以下のファイル形式が自動で Markdown へテキスト抽出されます。

- **Microsoft Office**:
  - Excel（`.xlsx`, `.xls`）: 表組みを Markdown テーブルとして高精度に抽出
  - Word（`.docx`, `.doc`）: 見出し・段落構造を Markdown として抽出
  - PowerPoint（`.pptx`, `.ppt`）: スライドごとのテキスト抽出
- **ドキュメント**:
  - PDF（`.pdf`）
- **プレーンテキスト / データ**:
  - `.txt`, `.csv`, `.tsv`, `.json`, `.md`

---

## 6. リクエスト JSON の具体例

### 例1: 単一の Excel ファイルが添付された場合
```json
{
  "model": "openai.gpt-oss-20b-1:0",
  "stream": false,
  "messages": [
    {
      "role": "system",
      "content": "社内アシスタントとして日本語で簡潔に回答してください。"
    },
    {
      "role": "user",
      "content": "山田 太郎 (yamada_t):\n@ai:assistant この売上データを分析して、先月比の成長要因を箇条書きで教えてください。\n[添付ファイル: C:/minihub/storage/files/202610_sales.xlsx]"
    }
  ]
}
```

### 例2: スレッド履歴＋複数ファイルが添付された場合
```json
{
  "model": "openai.gpt-oss-20b-1:0",
  "stream": false,
  "messages": [
    {
      "role": "system",
      "content": "社内アシスタントとして日本語で簡潔に回答してください。"
    },
    {
      "role": "user",
      "content": "山田 太郎 (yamada_t):\n@ai:assistant 営業提案書のレビューをお願いします。"
    },
    {
      "role": "assistant",
      "content": "承知いたしました。提案書ファイルまたは詳細をお知らせください。"
    },
    {
      "role": "user",
      "content": "佐藤 花子 (sato_h):\n@ai:assistant 以下の2点を添付します。確認してください。\n[添付ファイル: C:/minihub/storage/files/proposal_draft.docx]\n[添付ファイル: C:/minihub/storage/files/cost_estimate.pdf]"
    }
  ]
}
```

---

## 7. TEGO-core 側での処理とレスポンス仕様

### 7.1 TEGO-core 側での処理パイプライン
1. `messages` の末尾から `[添付ファイル: ...]` を検出。
2. ファイルを読み込み、Markdown へテキスト変換。
3. 変換テキストをプロンプトのコンテキストブロックとして合成:
   ```text
   --- 添付ファイル内容 (proposal_draft.docx) ---
   # 業務提案書
   ...
   ----------------------------------------------
   ```
4. Amazon Bedrock Converse API に渡して回答生成。

### 7.2 レスポンス形式 (200 OK)
minihub の標準仕様に準拠した形式で返却されます。
```json
{
  "id": "chatcmpl-xxxxxxxxxxxx",
  "object": "chat.completion",
  "created": 1728362400,
  "model": "openai.gpt-oss-20b-1:0",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "添付いただいた提案書と概算見積もりを確認しました。以下の改善点を提案します。\n1. ..."
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 1250,
    "completion_tokens": 340,
    "total_tokens": 1590
  }
}
```

### 7.3 エラー・例外時のフォールバック保証
- **ファイルが存在しない / 変換失敗時**:
  TEGO-core は `500 Internal Server Error` で異常終了せず、プロンプト内に `[添付ファイル読み込みエラー: ファイルが見つかりませんでした]` という注記を補完して回答を生成します。そのため、minihub 側のチャットが定型エラーで停止することはありません。
- **回答サイズ上限**:
  minihub の仕様である **UTF-8 16 KiB** を超えないよう、超過時は自動でトリミングされます。

---

## 8. minihub 実装時の留意点

1. **API 送信前のファイル書き出し完了**:
   minihub が TEGO-core へ POST する前に、添付ファイルがディスクストレージへ完全にフラッシュ（同期保存）されていることを確認してください。
2. **タイムアウト設定**:
   大容量の Office/PDF ファイルのテキスト抽出と推論には数秒〜十数秒要する場合があります。`minihub.json` の `timeout` は既定の `120s`（または長め）を維持してください。
3. **ファイル保存期間とクリーンアップ**:
   TEGO-core はステートレスにファイルを読み取ります。一時ファイルの保持・削除ポリシーは minihub 側の既存ストレージ管理ルールに従ってください。


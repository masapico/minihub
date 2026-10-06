# データモデル

[設計図の目次](README.md) / [アーキテクチャ](architecture.md) / [処理フロー](workflows.md)

## 論理ER図

```mermaid
erDiagram
    USER {
        string id PK
        string name
        string role
        bool enabled
        json groups
    }
    GROUP {
        string id PK
        string name
    }
    CHANNEL {
        string id PK
        string type
        json members
        json groups
        json managers
        datetime archivedAt
    }
    MESSAGE {
        string channelId PK
        int64 seq PK
        string id UK
        string userId
        datetime ts
        int64 threadRootSeq
        string text
    }
    READ_STATE {
        string userId PK
        string channelId PK
        int64 threadRootSeq PK
        int64 lastReadSeq
    }
    REACTION {
        string channelId
        int64 messageSeq
        string userId
        string key
        bool active
    }
    POLL {
        string id PK
        string channelId
        string status
        int64 announcementSeq
    }
    POLL_RESPONSE {
        string pollId
        int64 seq
        string userId
        json optionIds
    }
    SCHEDULE {
        string id PK
        string channelId
        string status
        int64 announcementSeq
    }
    SCHEDULE_RESPONSE {
        string scheduleId
        int64 seq
        string userId
        string choices
    }
    WITHDRAWAL {
        string kind PK
        string channelId PK
        string targetId PK
        string events
    }

    USER }o--o{ GROUP : "所属"
    USER }o--o{ CHANNEL : "直接参加・管理"
    GROUP }o--o{ CHANNEL : "割当"
    CHANNEL ||--o{ MESSAGE : "保持"
    USER ||--o{ MESSAGE : "投稿"
    MESSAGE ||--o{ MESSAGE : "起点と返信"
    USER ||--o{ READ_STATE : "既読位置"
    CHANNEL ||--o{ READ_STATE : "対象"
    MESSAGE ||--o{ REACTION : "反応"
    CHANNEL ||--o{ POLL : "公開先"
    POLL ||--o{ POLL_RESPONSE : "回答履歴"
    CHANNEL ||--o{ SCHEDULE : "公開先"
    SCHEDULE ||--o{ SCHEDULE_RESPONSE : "回答履歴"
    CHANNEL ||--o{ WITHDRAWAL : "取消記録"
```

これは**概念上の関係**です。`User.Groups` がグループ所属の正本で、チャンネルの直接参加者・割当グループ・管理者は `Channel` の配列にあります。独立した所属レコードを両方式で保存するわけではありません。グループ所属者とチャンネル割当グループから実効メンバーを判定します。公開チャンネルの閲覧は非参加者にも許可されますが、投稿は参加が必要です。非公開チャンネルは実効メンバーのみ閲覧・投稿できます。

メッセージの `seq` はチャンネル内で単調増加し、返信も同じ連番を使います。`threadRootSeq=0` は本流、正の値は起点メッセージの連番です。既読位置はユーザー×チャンネルと、閲覧したスレッドごとに保持し、後戻りさせません。`POLL` と `SCHEDULE` は告知メッセージへの参照を持ちます。`WITHDRAWAL` はメッセージ・投票・予定調整の取消と復元の履歴で、元データの物理削除ではありません。

## SQLiteの物理ER図

```mermaid
erDiagram
    users {
        text id PK
        json data
    }
    groups {
        text id PK
        json data
    }
    channels {
        text id PK
        json data
    }
    sessions {
        text id PK
        json data
    }
    schedules {
        text id PK
        json data
    }
    polls {
        text id PK
        json data
    }
    counters {
        text kind PK
        text id PK
        int last_seq
    }
    messages {
        text channel_id PK
        int seq PK
        text id UK
        text ts
        int ts_ns
        text user_id
        text text
        int root
        text mention_ids
        text schedule_id
        text poll_id
    }
    mention_refs {
        text message_id PK
        text kind PK
        text target PK
    }
    mention_reads {
        text user_id PK
        text message_id PK
        text read_at
    }
    read_state {
        text user_id PK
        text channel_id PK
        int root PK
        int last_seq
    }
    reaction_events {
        int event_id PK
        text channel_id
        int seq
        text user_id
        text key
        int active
    }
    reactions {
        text channel_id PK
        int seq PK
        text key PK
        text user_id PK
        int active
    }
    response_events {
        text schedule_id PK
        int seq PK
        text user_id
        json data
    }
    poll_response_events {
        text poll_id PK
        int seq PK
        text user_id
        json data
    }
    withdrawals {
        text kind PK
        text channel_id PK
        text target_id PK
        json data
    }
    migration_complete {
        int id PK
        text manifest
    }

    channels ||--o{ messages : "channel_id"
    messages ||--o{ mention_refs : "message_id FK"
    messages ||--o{ reaction_events : "channel_id seq"
    messages ||--o{ reactions : "channel_id seq"
    users ||--o{ read_state : "user_id"
    channels ||--o{ read_state : "channel_id"
    schedules ||--o{ response_events : "schedule_id"
    polls ||--o{ poll_response_events : "poll_id"
    channels ||--o{ withdrawals : "channel_id"
```

図の線は参照関係を示します。DB制約として宣言された外部キーは `mention_refs.message_id → messages.id` です。その他の参照はサービス層とストレージ処理で検証し、SQLの外部キー制約を示すものではありません。`users`・`groups`・`channels`・`sessions`・`schedules`・`polls` はバージョン付きJSONを1件ずつ保持します。`user_groups`、`channel_members`、`channel_groups`、`channel_managers` はそのJSONから作る**ビュー**であり、別の保存テーブルではありません。

`messages` は `(channel_id, seq)` が主キーで `id` は全体で一意です。`messages` にはスレッド・時刻・投稿者の索引があります。`counters` は採番の正本で、将来履歴を消しても連番を再利用しないための表です。`reaction_events` と回答イベント表は履歴、`reactions` はリアクションの現在状態を持ちます。SQLiteスキーマは現行の `PRAGMA user_version=3` です。

## file方式の保存構成

```mermaid
flowchart TB
    ROOT["dataDir/"]
    ROOT --> MARK["storage.json / .minihub.lock"]
    ROOT --> META["users/*.json<br/>groups/*.json<br/>sessions/*.json"]
    ROOT --> CH["channels/{channelId}/"]
    CH --> CM["meta.json"]
    CH --> MSG["messages/YYYY-MM-DD.jsonl"]
    CH --> REACT["reactions/YYYY-MM-DD.jsonl"]
    ROOT --> STATE["state/{userId}.json<br/>本流・スレッド既読、メンション確認"]
    ROOT --> POLL["polls/{pollId}/<br/>meta.json / responses.jsonl"]
    ROOT --> SCH["schedules/{scheduleId}/<br/>meta.json / responses.jsonl"]
    ROOT --> WD["withdrawals/{channelId}/{kind}/{targetId}.json"]
```

file方式は小さなメタデータをJSONで置換し、メッセージと回答・リアクションの履歴をJSONLへ追記します。日付別メッセージファイルは時刻による分割であり、順序は `seq` で判断します。`state/{userId}.json` はユーザー単位の高水位を保持し、メッセージ×ユーザーの全組み合わせを作りません。未完了のJSONL末尾行は索引に含めません。

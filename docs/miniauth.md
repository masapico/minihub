# miniauth — 閉域網向け軽量認証・SSOサーバー

`miniauth` は、最大500人程度のイントラネット・閉域環境を対象とする軽量なOIDC / OAuth 2.0 認証サーバーおよび社内アプリポータルです。

外部データベース（PostgreSQLやMySQL等）やDockerを必要とせず、単一のGoバイナリとローカルストレージ（JSON/JSONL または SQLite）で動作します。外部CDNにも一切依存せず、完全な閉域・オフライン環境で稼働します。

---

## 主な機能

1. **シングルサインオン (SSO)**:
   - OAuth 2.0 (Authorization Code Flow) および OpenID Connect UserInfo エンドポイントを提供。
   - `minihub`（社内チャット）、社内Wiki、Grafana、GitLab、oauth2-proxy等と連携可能。
2. **組織・ユーザーのマスター管理**:
   - ユーザー作成、タブ区切りによる一括登録、アカウント無効化、パスワード変更。
   - 組織グループ（「営業部」「開発部」など）の作成と管理。
3. **社内アプリポータル**:
   - ログイン後、登録された社内サービスがカード形式で一覧表示され、ワンクリックで遷移・SSOログインできます。
4. **Directory API**:
   - `minihub` などの連携クライアント向けに最新の社員名簿・グループ一覧を配信し、二重管理を防止します。

---

## ビルドと起動

```bash
# ビルド
go build -o miniauth.exe ./cmd/miniauth

# 起動（デフォルト: 127.0.0.1:8090, データディレクトリ: auth_data）
./miniauth.exe -config miniauth.json
```

---

## 設定ファイル例 (`miniauth.json`)

```json
{
  "version": 1,
  "server": {
    "listenAddress": "127.0.0.1:8090",
    "basePath": "",
    "dataDir": "auth_data",
    "sessionTTL": "24h"
  },
  "storage": {
    "type": "file"
  },
  "ui": {
    "workspaceTitle": "社内ポータル & 認証",
    "loginMessage": "社内共通アカウントでログインしてください。"
  },
  "oidc": {
    "issuer": "http://127.0.0.1:8090",
    "jwtSecret": "your-secure-jwt-secret-key-32bytes",
    "serviceToken": "minihub-sync-service-token-12345"
  },
  "clients": [
    {
      "id": "minihub-chat",
      "name": "社内チャット (minihub)",
      "secret": "chat-secret-abcdef",
      "redirectURIs": ["http://127.0.0.1:8080/api/auth/sso/callback"],
      "launchURL": "http://127.0.0.1:8080/",
      "icon": "chat-square-text"
    },
    {
      "id": "internal-wiki",
      "name": "社内Wiki",
      "secret": "wiki-secret-ghijk",
      "redirectURIs": ["http://wiki.corp.local/oauth/callback"],
      "launchURL": "http://wiki.corp.local/",
      "icon": "journal-text"
    }
  ],
  "initialAdmin": {
    "id": "admin",
    "name": "システム管理者",
    "password": "initial-password-12345"
  }
}
```

---

## `minihub` 側の SSO 連携設定 (`minihub.json`)

`minihub.json` の `"auth"` 設定を追加します。

```json
{
  "version": 1,
  "server": {
    "listenAddress": "127.0.0.1:8080",
    "dataDir": "data"
  },
  "auth": {
    "mode": "sso",
    "miniauth": {
      "url": "http://127.0.0.1:8090",
      "clientID": "minihub-chat",
      "clientSecret": "chat-secret-abcdef",
      "serviceToken": "minihub-sync-service-token-12345",
      "syncInterval": "10m"
    }
  }
}
```

### SSO モード時の動作
- **ログイン**: minihub にアクセスすると自動的に miniauth 経由でSSO認証され、パスワード入力なしでログインが完了します。
- **名簿同期**: 起動時および10分ごとに miniauth から社員・グループ名簿が自動同期されます。
- **管理画面**: minihub 側のユーザー・グループ登録UIは隠れ、チャンネルへのユーザー・グループ紐付けに専念できます。「組織・ユーザー管理」リンクをクリックすると miniauth の管理画面が開きます。


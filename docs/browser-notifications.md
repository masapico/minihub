# ブラウザAPIを使ったOS通知の設定

minihubは、タブを開いている間にブラウザの通知APIでOS通知を表示できます。

管理者は設定ファイルの `notifications.osNotificationsEnabled` で利用可否を指定できます。省略時は `true` です。`false` では通知センターのボタンが無効になり、「管理者の設定によりOS通知は利用できません」と表示します。以前ONにした端末でもOS通知と通知権限の要求を停止します。画面再読み込み後はブラウザの通知権限も要求しません。利用者の保存済み設定は保持し、アプリ内通知と未読表示は続きます。設定変更後はサービスを再起動し、開いている画面を再読み込みしてください。`true` にしても利用者の通知を自動でONにはしません。
この文書は通知設定に関するEdgeの管理手順を、minihubの実装条件に合わせて整理したものです。

## 利用者の設定

1. minihubへログインし、右上のベルから通知センターを開きます。
2. 「OS通知を有効にする」を押し、ブラウザから求められたら通知を許可します。許可を求めるのはこの操作をしたときだけです。
3. Windowsとブラウザ側でも通知が許可されていることを確認します。停止するときは同じ場所の「OS通知を停止する」を押します。

通知の対象は、参加中チャンネルの他者の新規投稿・メンションと、参加中スレッドの未読返信・メンションです。通知に本文と送信者名は表示しません。タブをすべて閉じると通知は届きません。OS通知を停止しても画面内通知と未読表示は続きます。

通知を押すと該当投稿・返信を開きます。複数タブで開いても同じ投稿のOS通知は1件です。再接続で取得した過去の投稿は通知しません。OS通知には通知APIとWeb Locks APIが使える安全な接続元が必要です。利用の許可設定はHTTP／HTTPSの選択とは独立しています。

## 管理者: 接続元の準備

通常は組織のCA証明書を使ったHTTPSを用意してください。minihub自身のHTTPS待ち受けは `server.tls.enabled: true` とPEM形式の `certFile`・`keyFile` で設定でき、Secure Cookieは自動的に有効になります。設定例、証明書の信頼・秘密鍵の権限・更新手順は[管理者マニュアルのHTTP／HTTPSの選択](admin-manual.md#httphttpsの選択)を参照してください。リバースプロキシでHTTPSを終端する場合はminihubのSecure Cookieを明示的に有効にします。

- 業務端末からHTTPで開く場合、通常はOS通知を利用できません。minihubは `window.isSecureContext`、通知API、Web Locks APIを確認します。
- `NotificationsAllowedForUrls` は**通知を許可するポリシー**であり、HTTP接続元を安全な接続元にするものではありません。
- 管理対象のEdgeでHTTPを使う場合は、`OverrideSecurityRestrictionsOnInsecureOrigin` で対象の接続元を指定し、通知を利用者に選ばせるか、必要に応じて `NotificationsAllowedForUrls` で許可します。前者は通信を暗号化しないため、適用先を限定してください。
- 指定する接続元は実際のアクセスURLに合わせます。たとえば `http://workstation.local:8080` です。プロトコル、ホスト、ポートを正確に合わせ、不要なワイルドカードは使わないでください。

### 方法A: グループポリシー

1. Edgeの管理用テンプレートがなければ、[Microsoftの手順](https://learn.microsoft.com/en-us/deployedge/configure-microsoft-edge)に従ってPolicy Templatesを導入します。`msedge.admx` と言語に合う `msedge.adml` を対応する `PolicyDefinitions` フォルダーへ配置します。
2. ローカル端末では `gpedit.msc`、ドメイン環境では `gpmc.msc` を開きます。
3. 「コンピューターの構成」→「管理用テンプレート」→「Microsoft Edge」の「Control where security restrictions on insecure origins apply」（`OverrideSecurityRestrictionsOnInsecureOrigin`）を有効にし、minihubのHTTP接続元を登録します。
4. 通知をポリシーで許可する場合は、同じ「Microsoft Edge」配下の「コンテンツの設定」→「特定のサイトからの通知を許可する」（`NotificationsAllowedForUrls`）を有効にし、同じ接続元を登録します。
5. `gpupdate /force` を実行し、Edgeを再起動します。接続元を安全なものとして扱うポリシーはブラウザの再起動が必要です。

### 方法B: 端末ごとのレジストリ設定

管理権限のある端末で、`regedit` を使って次のキーに文字列値（`REG_SZ`）を追加します。値名は `1` から始め、複数の接続元には `2`、`3` と連番を使います。値のデータには実際の接続元を指定します。

| 用途 | レジストリキー |
| --- | --- |
| HTTP接続元の安全な接続元扱い | `HKEY_LOCAL_MACHINE\SOFTWARE\Policies\Microsoft\Edge\OverrideSecurityRestrictionsOnInsecureOrigin` |
| 通知の許可 | `HKEY_LOCAL_MACHINE\SOFTWARE\Policies\Microsoft\Edge\NotificationsAllowedForUrls` |

PowerShellで登録する例です。管理者として実行し、URLを実際の接続元に置き換えてください。

```powershell
$origin = 'http://192.168.1.100:8080'
$edgePolicy = 'HKLM:\SOFTWARE\Policies\Microsoft\Edge'
foreach ($name in @('OverrideSecurityRestrictionsOnInsecureOrigin', 'NotificationsAllowedForUrls')) {
    $path = Join-Path $edgePolicy $name
    New-Item -Path $path -Force | Out-Null
    New-ItemProperty -Path $path -Name '1' -Value $origin -PropertyType String -Force | Out-Null
}
```

登録後はEdgeを再起動します。

## 設定の確認

1. Edgeで `edge://policy` を開き、設定したポリシーが表示され、Statusが `OK` で接続元が正しいことを確認します。
2. minihubのページで開発者ツールのConsoleに `window.isSecureContext` と入力し、`true` になることを確認します。HTTPで `false` の場合、通知許可ポリシーだけでは利用できません。
3. 通知センターで「OS通知を有効にする」を押します。「この接続では利用できません」と表示される場合は、安全な接続元、通知API、Web Locks API、ブラウザ保存領域を確認します。権限が拒否された場合はEdgeのサイト設定とWindowsの通知設定を確認します。
4. 別のユーザーから参加中チャンネルへ投稿してもらい、minihubのタブを開いたまま別の画面を見て、OS通知が届くことを確認します。

minihubはService WorkerやPush APIを使用しません。タブを閉じた後のバックグラウンド通知には対応していません。

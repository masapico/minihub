# minihub ビルド済み版

イントラネット向けチャットのサーバー実行版です。Goや外部DBのインストールは不要です。利用者はブラウザから接続します。

## 配布ファイルを選ぶ

| ファイル名の末尾 | サーバー機 |
| --- | --- |
| `windows_amd64.zip` | Windows x64 |
| `darwin_arm64.tar.gz` | Mac Apple Silicon（Mシリーズ） |
| `linux_amd64.tar.gz` | Linux x64 |
| `linux_arm64.tar.gz` | Linux arm64 |

圧縮ファイルを展開し、展開したフォルダーをターミナル／PowerShellで開いて、以下のコマンドを実行します。macOS／Linuxでは`tar -xzf <ファイル名>`で展開できます。macOS版とWindows版は署名・公証を行っていません。組織の実行許可ポリシーに従って導入してください。

## 初期設定

Windows（PowerShell）：

```powershell
Copy-Item minihub.sample.json minihub.json
```

macOS／Linux：

```bash
cp minihub.sample.json minihub.json
chmod 600 minihub.json
```

`minihub.json`の`initialAdmin.password`を8文字以上・UTF-8で72バイト以下の値へ変更します。実設定は管理者と実行アカウントだけが読めるようにしてください。Windowsではファイルの「プロパティ」→「セキュリティ」でアクセス権を設定します。

## 起動・接続・停止

Windows：

```powershell
./minihub.exe -config minihub.json
```

macOS／Linux：

```bash
./minihub -config minihub.json
```

サーバー機のブラウザで <http://127.0.0.1:8080/login> を開き、ユーザーID`admin`と設定したパスワードでログインします。初回管理者の作成後、実設定から`initialAdmin`を削除してください。起動したターミナルは稼働中、開いたままにします。

閉域内の他のパソコンから接続する場合は、起動中のサーバーを停止してから`0.0.0.0:8080`で起動します。すべてのIPv4ネットワークインターフェースで待ち受ける指定です。

Windows（PowerShell）：

```powershell
./minihub.exe -config minihub.json -addr 0.0.0.0:8080
```

macOS／Linux：

```bash
./minihub -config minihub.json -addr 0.0.0.0:8080
```

設定の`server.listenAddress`を`"0.0.0.0:8080"`へ変更し、通常の起動コマンドを使う方法もあります。明示した`-addr`が設定ファイルの値より優先されます。ファイアウォールでは必要な閉域内の接続元からTCP 8080へのアクセスを許可してください。他のパソコンでは、サーバー機のIPアドレスが`192.168.1.10`の場合、<http://192.168.1.10:8080/login> を開きます。ホスト名でも接続できます。HTTPSの設定は[管理者マニュアル](docs_html/admin-manual.html)を参照してください。

停止は起動したターミナルでCtrl+Cを押し、終了を待ちます。Windowsでは同じ実行アカウントの別のPowerShellからも停止できます。

```powershell
./minihub.exe stop -config minihub.json -wait 60s
```

Windowsの`scripts/backup.bat`はパス設定を編集してから使用します。実行ファイル・設定・データ・バックアップ先のパスを環境に合わせてください。手順は[管理者マニュアル](docs_html/admin-manual.html)に記載しています。

## 更新・バックアップ

1. サーバーを停止し、プロセスの終了を待ちます。
2. 使用中の`minihub.json`と、`server.dataDir`が指すデータディレクトリ全体をバックアップします。SQLiteもディレクトリ全体をコピーします。
3. 新しい配布物を別のフォルダーへ展開し、使用中の実行ファイルだけを差し替えます。設定とデータは保持します。macOS／Linuxでは実行権限も保持してください。
4. 従来の設定で起動し、ログインと履歴を確認します。リリースノートに移行手順がある場合は、その手順に従います。

保存方式は`file`または`sqlite`です。更新と同時に設定値だけで保存方式を切り替えず、[保存方式とSQLiteへの移行](docs_html/sqlite.html)の手順を使用してください。

## 同梱資料

- [操作マニュアル一覧](docs_html/index.html)：ブラウザで開いてオフラインで閲覧できます。
- `BUILDINFO.txt`：ビルドのバージョン、コミット、作業ツリーの変更有無、Goバージョン、OS・CPU。
- `licenses/`：実行ファイルに組み込まれた依存物のライセンス文書。Goモジュールはフォルダー名にバージョンを含めています。

アーカイブには実設定やデータを含めていません。データディレクトリは初回起動時に作成され、設定内の相対的な`dataDir`は設定ファイルの所在ディレクトリを基準に解決されます。

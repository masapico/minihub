package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testAuthenticator bool

func TestOSNotificationSettingIsRendered(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		page := request(t, Handler(testAuthenticator(true), Options{OSNotificationsEnabled: &enabled}), "/").Body.String()
		want := `data-os-notifications-enabled="false"`
		if enabled {
			want = `data-os-notifications-enabled="true"`
		}
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %s", want)
		}
	}
	for _, options := range [][]Options{nil, {Options{WorkspaceTitle: "custom"}}} {
		page := request(t, Handler(testAuthenticator(true), options...), "/").Body.String()
		if !strings.Contains(page, `data-os-notifications-enabled="true"`) {
			t.Fatal("legacy notification default changed")
		}
	}
}

func TestChatThemeIsServedOnlyWithChatPage(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/")
	if !strings.Contains(page.Body.String(), `href="/css/chat.css"`) {
		t.Fatal("chat page does not load its dedicated theme")
	}
	stylesheet := request(t, handler, "/css/chat.css")
	if stylesheet.Code != http.StatusOK || !strings.Contains(stylesheet.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("chat theme is not served as CSS: status=%d", stylesheet.Code)
	}
	login := request(t, Handler(testAuthenticator(false)), "/login")
	if strings.Contains(login.Body.String(), `/css/chat.css`) {
		t.Fatal("chat theme leaks into the login page")
	}
}

func (authenticated testAuthenticator) Authenticate(*http.Request) (string, error) {
	if !authenticated {
		return "", errors.New("unauthenticated")
	}
	return "alice", nil
}

func TestHandlerSeparatesLoginAndApplication(t *testing.T) {
	t.Run("unauthenticated application redirects to login", func(t *testing.T) {
		recorder := request(t, Handler(testAuthenticator(false)), "/")
		if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/login" {
			t.Fatalf("status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
		}
	})

	t.Run("login page contains the only login form", func(t *testing.T) {
		handler := Handler(testAuthenticator(false))
		login := request(t, handler, "/login")
		if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `id="loginForm"`) || !strings.Contains(login.Body.String(), `autocomplete="current-password"`) {
			t.Fatalf("login page was not served: status=%d", login.Code)
		}
		if !strings.Contains(login.Body.String(), `/js/login.js`) || !strings.Contains(login.Body.String(), `bootstrap.bundle.min.js`) {
			t.Fatal("login assets were not linked")
		}
	})

	t.Run("authenticated login redirects to application", func(t *testing.T) {
		recorder := request(t, Handler(testAuthenticator(true)), "/login")
		if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/" {
			t.Fatalf("status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
		}
	})

	t.Run("application has no login form", func(t *testing.T) {
		recorder := request(t, Handler(testAuthenticator(true)), "/")
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK || !strings.Contains(body, "minihub") {
			t.Fatalf("application was not served: status=%d", recorder.Code)
		}
		if strings.Contains(body, `id="loginForm"`) || strings.Contains(body, `name="username"`) {
			t.Fatal("application HTML still contains the login form")
		}
		if !strings.Contains(body, `id="passwordForm"`) {
			t.Fatal("self-service password form is missing")
		}
		passwordButton := body[strings.Index(body, `id="changePassword"`):]
		passwordButton = passwordButton[:strings.Index(passwordButton, `</button>`)]
		if !strings.Contains(passwordButton, `viewBox="0 0 16 16"`) || strings.Contains(passwordButton, `<use`) {
			t.Fatal("password-change key icon is not embedded in its button")
		}
		for _, expected := range []string{"bootstrap.min.css", "bootstrap.bundle.min.js", "bootstrap-icons.svg", `id="toastContainer"`, `class="modal fade"`, `/favicon.svg`, `/favicon.ico`, `name="theme-color" content="#334155"`} {
			if !strings.Contains(body, expected) {
				t.Fatalf("application HTML does not contain %q", expected)
			}
		}
	})
}

func TestSchedulePagesAndSafeNewTabLinksAreEmbedded(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	for _, path := range []string{"/schedules", "/schedules/new", "/schedules/abc123"} {
		response := request(t, handler, path)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="scheduleList"`) || !strings.Contains(response.Body.String(), `/js/schedule.js`) {
			t.Fatalf("schedule page %s status=%d", path, response.Code)
		}
	}
	application := request(t, handler, "/").Body.String()
	if !strings.Contains(application, `id="createSchedule"`) || !strings.Contains(application, `id="activePollSchedules"`) || !strings.Contains(application, `id="createPoll"`) || !strings.Contains(application, `id="pollModal"`) {
		t.Fatal("poll and schedule navigation is missing")
	}
	for _, removed := range []string{`id="pollScheduleNav"`, `id="allPollSchedules"`, `id="pollListModal"`} {
		if strings.Contains(application, removed) {
			t.Fatalf("obsolete poll and schedule navigation remains: %s", removed)
		}
	}
	pollScript := request(t, handler, "/js/polls.js").Body.String()
	if !strings.Contains(pollScript, `noopener,noreferrer`) || !strings.Contains(pollScript, `pollRenderCard`) {
		t.Fatal("safe poll and schedule links are missing")
	}
	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{`link.target = "_blank"`, `link.rel = "noopener noreferrer"`, `scheduleReference`} {
		if !strings.Contains(script, expected) {
			t.Fatalf("chat schedule link behavior missing %q", expected)
		}
	}
}

func TestUnauthenticatedSchedulePreservesSafeReturnPath(t *testing.T) {
	recorder := request(t, Handler(testAuthenticator(false)), "/schedules/abc?tab=answers")
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/login?next=%2Fschedules%2Fabc%3Ftab%3Danswers" {
		t.Fatalf("status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestHandlerRendersConfiguredUIStringsSafely(t *testing.T) {
	handler := Handler(testAuthenticator(false), Options{
		WorkspaceTitle: `社内 <Chat>`,
		LoginMessage:   `案内 <script>alert("xss")</script>`,
	})
	login := request(t, handler, "/login").Body.String()
	for _, expected := range []string{`社内 &lt;Chat&gt;`, `案内 &lt;script&gt;alert(&#34;xss&#34;)&lt;/script&gt;`} {
		if !strings.Contains(login, expected) {
			t.Fatalf("login page does not contain escaped setting %q", expected)
		}
	}
	if strings.Contains(login, `<script>alert`) {
		t.Fatal("login setting was rendered as executable HTML")
	}

	application := request(t, Handler(testAuthenticator(true), Options{WorkspaceTitle: "社内チャット"}), "/").Body.String()
	if !strings.Contains(application, `<span id="channelNavTitle">社内チャット</span>`) || !strings.Contains(application, `<title>社内チャット</title>`) {
		t.Fatal("workspace title was not rendered in the application")
	}
}

func TestHandlerServesEmbeddedAssets(t *testing.T) {
	handler := Handler(testAuthenticator(false))
	for _, path := range []string{
		"/css/app.css",
		"/js/app.js",
		"/js/login.js",
		"/vendor/bootstrap/bootstrap.min.css",
		"/vendor/bootstrap/bootstrap.bundle.min.js",
		"/vendor/bootstrap-icons/bootstrap-icons.svg",
		"/favicon.svg",
		"/favicon.ico",
	} {
		if recorder := request(t, handler, path); recorder.Code != http.StatusOK {
			t.Errorf("GET %s: status=%d", path, recorder.Code)
		}
	}
	if contentType := request(t, handler, "/favicon.ico").Header().Get("Content-Type"); !strings.Contains(contentType, "image") {
		t.Fatalf("favicon content type=%q", contentType)
	}
	if script := request(t, handler, "/js/app.js").Body.String(); !strings.Contains(script, `viewBox="0 0 16 16"`) || !strings.Contains(script, `name === "lock-fill"`) {
		t.Fatal("private-channel lock icon is not embedded in the application script")
	}
	script := request(t, handler, "/js/app.js").Body.String()
	setupOverride := strings.LastIndex(script, "setup = function (me)")
	passwordVisibility := strings.LastIndex(script, `$("changePassword").classList.toggle(`)
	if setupOverride < 0 || passwordVisibility < setupOverride {
		t.Fatal("the active account setup does not update password-button visibility")
	}
	for _, path := range []string{
		"/vendor/bootstrap/bootstrap.min.css",
		"/vendor/bootstrap/bootstrap.bundle.min.js",
	} {
		if body := request(t, handler, path).Body.String(); strings.Contains(body, "sourceMappingURL=") {
			t.Errorf("GET %s: references an unavailable source map", path)
		}
	}
}

func TestApplicationPreservesContentEditableBlockBoundaries(t *testing.T) {
	script := request(t, Handler(testAuthenticator(true)), "/js/app.js").Body.String()
	for _, expected := range []string{
		`function serializeEditorChildren(parent)`,
		`["DIV", "P", "BLOCKQUOTE"].includes(node.tagName)`,
		`if (block && value && !value.endsWith("\n")) value += "\n"`,
		`return serializeEditorChildren(root).replace(/\n+$/, "")`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("contenteditable newline serialization does not contain %q", expected)
		}
	}
}

func TestSharedSlateThemeAndSemanticToastsAreEmbedded(t *testing.T) {
	handler := Handler(testAuthenticator(false))
	stylesheet := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{
		`--a: var(--ui-primary, #334155)`,
		`--focus: var(--ui-focus, #2563eb)`,
		`.tc-toast-success`,
		`.tc-toast-warning`,
		`.tc-toast-danger`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("shared slate theme does not contain %q", expected)
		}
	}
	favicon := request(t, handler, "/favicon.svg").Body.String()
	if !strings.Contains(favicon, `fill="#334155"`) {
		t.Fatal("SVG favicon does not use the shared slate color")
	}
}

func TestClientScriptsUseJapaneseRequestFailureMessages(t *testing.T) {
	handler := Handler(testAuthenticator(false))
	wantNetwork := "通信に失敗しました。ネットワーク接続を確認して、もう一度お試しください"
	wantResponse := "サーバーからの応答を読み取れませんでした"
	for _, path := range []string{"/js/app.js", "/js/login.js", "/js/schedule.js"} {
		script := request(t, handler, path).Body.String()
		for _, expected := range []string{wantNetwork, wantResponse} {
			if !strings.Contains(script, expected) {
				t.Errorf("%s does not contain %q", path, expected)
			}
		}
	}
}

func TestStylesheetsIncludeContrastAndFocusStates(t *testing.T) {
	handler := Handler(testAuthenticator(false))
	appStyles := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{
		"--control-line: var(--ui-control-line, #94a3b8)",
		".channel:focus-visible",
		"justify-content: center",
		".channel > .tc-badge",
		".user.selected",
		".group-row.selected",
		".message.mine.mentioned",
		".schedule-message-link:hover",
	} {
		if !strings.Contains(appStyles, expected) {
			t.Fatalf("application stylesheet does not contain contrast rule %q", expected)
		}
	}

	sharedStyles := request(t, handler, "/css/theme.css").Body.String()
	if !strings.Contains(sharedStyles, "--ui-control-line: #94a3b8") {
		t.Fatal("shared stylesheet does not contain control contrast color")
	}

	scheduleStyles := request(t, handler, "/css/schedule.css").Body.String()
	for _, expected := range []string{
		"--control-line:var(--ui-control-line)",
		".choice-buttons input:focus-visible + span",
		".schedule-card:focus-visible",
	} {
		if !strings.Contains(scheduleStyles, expected) {
			t.Fatalf("schedule stylesheet does not contain contrast rule %q", expected)
		}
	}
}

func TestApplicationIncludesDelayedHistoryLoadingStates(t *testing.T) {
	script := request(t, Handler(testAuthenticator(false)), "/js/app.js").Body.String()
	for _, expected := range []string{
		`}, 150);`,
		`aria-busy`,
		`履歴を読み込んでいます…`,
		`過去の履歴を読み込んでいます…`,
		`新着を確認しています…`,
		`履歴を取得できませんでした`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("application script does not contain loading state %q", expected)
		}
	}
	stylesheet := request(t, Handler(testAuthenticator(false)), "/css/app.css").Body.String()
	for _, expected := range []string{".history-status-initial", ".history-status-catchup", ".history-status-error"} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("application stylesheet does not contain %q", expected)
		}
	}
}

func TestApplicationIncludesRestrictedRichTextComposer(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	for _, expected := range []string{
		`data-network-path-mode="copy"`,
		`class="message-editor"`,
		`contenteditable="false"`,
		`data-format="bold"`,
		`data-format="underline"`,
		`data-format="strikeThrough"`,
		`data-format="quote"`,
		`id="linkButton"`,
		`aria-label="送信（Ctrl+Enter）"`,
		`id="linkModal"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("rich text composer does not contain %q", expected)
		}
	}
	if strings.Contains(page, "data-mark=\"`\"") || strings.Contains(page, `aria-label="コード"`) {
		t.Fatal("composer still offers the code formatting control")
	}
	if strings.Contains(page, `class="foot"`) || strings.Index(page, `id="send"`) > strings.Index(page, `id="typingStatus"`) {
		t.Fatal("send action is not the final control in the formatting toolbar")
	}

	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`function serializeEditorNode(node)`,
		`function safeLinkURL(value)`,
		`function linkDestinationLabel(href)`,
		`function networkLocationDetails(href)`,
		`function networkLocationIcon(extension)`,
		`function networkLocation(label, href)`,
		`function copyNetworkPath(path, pathElement)`,
		`networkPathMode === "open-and-copy"`,
		`networkPathMode === "disabled"`,
		`file-earmark-word`,
		`file-earmark-excel`,
		`file-earmark-pdf`,
		`["http:", "https:"]`,
		`url.protocol === "file:"`,
		`windowsPath.startsWith("\\\\")`,
		`value.replaceAll("¥", "\\")`,
		`anchor.target = "_blank"`,
		`anchor.rel = "noopener noreferrer"`,
		`anchor.title = `,
		`event.clipboardData.getData("text/plain")`,
		`const MAX_MESSAGE_BYTES = 16 * 1024`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("rich text behavior does not contain %q", expected)
		}
	}

	stylesheet := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{".message-editor {", ".text blockquote {", `.fmt[aria-pressed="true"]`, ".typing-status:empty {", ".tools #send {", ".composer .format-help {", "justify-content: center", ".fmt svg {", "display: block", ".network-location {", ".network-location-action"} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("rich text stylesheet does not contain %q", expected)
		}
	}
	if strings.Contains(stylesheet, "overflow-x: auto") {
		t.Fatal("formatting toolbar clips the mention menu")
	}
}

func TestThreadIncludesCompactRichTextComposer(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	for _, expected := range []string{
		`id="threadInput" class="message-editor thread-message-editor"`,
		`class="thread-tools"`,
		`id="threadLinkButton"`,
		`id="threadMentionButton"`,
		`id="threadEditorLimit"`,
		`id="threadTypingStatus" class="typing-status thread-typing-status muted" role="status" aria-live="polite"`,
		`aria-label="返信を送信（Ctrl+Enter）"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("thread rich text composer does not contain %q", expected)
		}
	}
	if strings.Contains(page, `<textarea id="threadInput"`) {
		t.Fatal("thread composer still uses a textarea")
	}

	threadScript := request(t, handler, "/js/threads.js").Body.String()
	for _, expected := range []string{
		`installRichEditor($("threadInput")`,
		`openEditorLink($("threadInput"))`,
		`applyEditorFormat(`,
	} {
		if !strings.Contains(threadScript, expected) {
			t.Fatalf("thread rich text behavior does not contain %q", expected)
		}
	}
	stylesheet := request(t, handler, "/css/threads.css").Body.String()
	for _, expected := range []string{
		`.thread-tools {`,
		`.thread-message-editor {`,
		`.thread-typing-status {`,
		`min-height:60px`,
		`max-height:120px`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("thread rich text stylesheet does not contain %q", expected)
		}
	}
}

func TestThreadPanelUsesCompactSummaryAndSharedCandidateStyle(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	for _, expected := range []string{
		`class="thread-summary-bar"`,
		`id="threadReadAll" type="button" class="btn btn-outline-secondary btn-sm mark-read-button"`,
		`<span>スレッドを既読にする</span>`,
		`id="threadMentionOptions" class="mention-options"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("thread panel does not contain %q", expected)
		}
	}
	threadScript := request(t, handler, "/js/threads.js").Body.String()
	if !strings.Contains(threadScript, `rootLabel.textContent = "元のメッセージ"`) {
		t.Fatal("thread root does not include an identifying label")
	}
	mentionScript := request(t, handler, "/js/thread-mentions.js").Body.String()
	for _, expected := range []string{
		`option.className = "mention-option thread-mention-option"`,
		`avatar.className = "mini"`,
	} {
		if !strings.Contains(mentionScript, expected) {
			t.Fatalf("thread mention candidates do not share the main style: %q", expected)
		}
	}
	stylesheet := request(t, handler, "/css/threads.css").Body.String()
	for _, expected := range []string{
		`.thread-summary-bar {`,
		`#threadRoot {`,
		`.thread-root-label {`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("thread panel stylesheet does not contain %q", expected)
		}
	}
	if strings.Contains(stylesheet, `.thread-mention-option[aria-selected="true"]`) {
		t.Fatal("thread mention candidate still has a selected-state style")
	}
	if strings.Contains(stylesheet, `box-shadow:inset 3px 0 #2563eb`) {
		t.Fatal("thread mention candidate still has its old left selection border")
	}
}

func TestReplyActionSharesTheReactionRow(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	appScript := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`bar.className = "reactions message-actions"`,
		`bar.setAttribute("aria-label", "メッセージの操作")`,
		`if (typeof replyButton === "function") actions.append(replyButton(m))`,
	} {
		if !strings.Contains(appScript, expected) {
			t.Fatalf("message action row does not contain %q", expected)
		}
	}
	if strings.Contains(appScript, `article.children[0].append(replyButton(m))`) {
		t.Fatal("reply action is still appended outside the reaction row")
	}
	stylesheet := request(t, handler, "/css/threads.css").Body.String()
	for _, expected := range []string{`.reply-button {`, `height:22px`, `margin:0`, `white-space:nowrap`} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("inline reply action stylesheet does not contain %q", expected)
		}
	}
}

func TestApplicationRendersConfiguredNetworkPathMode(t *testing.T) {
	for _, mode := range []string{"disabled", "copy", "open-and-copy"} {
		t.Run(mode, func(t *testing.T) {
			page := request(t, Handler(testAuthenticator(true), Options{NetworkPathMode: mode}), "/").Body.String()
			if !strings.Contains(page, `data-network-path-mode="`+mode+`"`) {
				t.Fatalf("application page does not contain network path mode %q", mode)
			}
		})
	}
}

func TestNetworkPathCopyDoesNotPersistNotificationCenterEntries(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`note("共有パスをコピーしました", false, false)`,
		`note("コピーできませんでした。選択したパスを手動でコピーしてください。", true, false)`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("network path copy notification is not toast-only: %q", expected)
		}
	}
}

func TestNotificationCenterDefaultsToMentionsAndReadSuccessIsSilent(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	if !strings.Contains(page, `class="nav-link active" id="mentionTab"`) ||
		!strings.Contains(page, `id="noticeList" class="notification-list hidden"`) ||
		!strings.Contains(page, `id="clearNotifications"`) {
		t.Fatal("notification center does not initially show the mention tab")
	}
	script := request(t, handler, "/js/app.js").Body.String()
	if !strings.Contains(script, `$("notificationCenter").addEventListener("show.bs.offcanvas"`) ||
		!strings.Contains(script, `$("mentionTab").click();`) ||
		!strings.Contains(script, `localStorage.removeItem(notificationStorageKey())`) ||
		!strings.Contains(script, `window.confirm("通知をすべてクリアしますか？")`) {
		t.Fatal("opening the notification center does not select the mention tab")
	}
	for _, unwanted := range []string{`note("既読にしました")`, `note("ここまで既読にしました")`} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("read success still creates a notification: %s", unwanted)
		}
	}
}

func TestReadActionIsIntegratedIntoStickyUnreadBoundary(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	if strings.Contains(page, `id="read"`) || strings.Contains(page, `ここまで既読`) {
		t.Fatal("the composer still contains the legacy read action")
	}

	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`function createUnreadLine(label, count)`,
		`className = "btn btn-outline-secondary btn-sm mark-read-button"`,
		`未読${count}件をすべて既読にする`,
		`const channelID = S.channel?.id`,
		`encodeURIComponent(channelID)}/read`,
		`if (S.channel?.id !== channelID) return;`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("application script does not contain integrated read behavior %q", expected)
		}
	}
	if strings.Contains(script, `$("read")`) {
		t.Fatal("application script still references the legacy read button")
	}

	stylesheet := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{
		`.unread-line {`,
		`position: sticky`,
		`.mark-read-button {`,
		`.unread-copy {`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("application stylesheet does not contain sticky unread control %q", expected)
		}
	}
	if strings.Contains(stylesheet, `.read-action`) {
		t.Fatal("application stylesheet still contains the legacy read-action rules")
	}
}

func TestChannelReadStatusesAreLoadedInOneRequest(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	script := request(t, handler, "/js/app.js").Body.String()
	if !strings.Contains(script, `api("/api/channels/read-statuses")`) {
		t.Fatal("application does not load channel read statuses in one request")
	}
	if strings.Contains(script, "S.channels.map(async") {
		t.Fatal("application still loads read status once per channel")
	}
}

func TestChannelActionRenderingDoesNotDependOnMessageHistory(t *testing.T) {
	script := request(t, Handler(testAuthenticator(false)), "/js/app.js").Body.String()
	functionStart := strings.Index(script, "function renderChannelAction(channel = S.channel)")
	activeRender := strings.LastIndex(script, "render = function ()")
	if functionStart < 0 || activeRender < 0 {
		t.Fatal("shared channel action renderer is missing")
	}
	emptyHistory := strings.Index(script[activeRender:], "if (!S.msgs.length)")
	actionRender := strings.Index(script[activeRender:], "renderChannelAction();")
	if emptyHistory < 0 || actionRender < 0 {
		t.Fatal("channel action is not rendered through the shared history-independent path")
	}
	if actionRender > emptyHistory {
		t.Fatal("channel action is updated after the empty-history early return")
	}

	activeSelect := strings.LastIndex(script, "select = async function (id)")
	if activeSelect < 0 {
		t.Fatal("active channel selection handler is missing")
	}
	selectedAssignment := strings.Index(script[activeSelect:], "S.channel = selectedChannel;")
	selectedActionRender := -1
	if selectedAssignment >= 0 {
		selectedActionRender = strings.Index(script[activeSelect+selectedAssignment:], "renderChannelAction();")
	}
	messageRequest := strings.Index(script[activeSelect:], "/messages?limit=100")
	if selectedAssignment < 0 || selectedActionRender < 0 || messageRequest < 0 || selectedAssignment+selectedActionRender > messageRequest {
		t.Fatal("selected channel action is not rendered before message history is requested")
	}
}

func TestMentionNavigationTemporarilyHighlightsTargetMessage(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`focusMessage(mention.message.seq, mention.channelId, true)`,
		`S.highlightedMessage = { channelID, seq };`,
		`setTimeout(clearMessageHighlight, 3000)`,
		`messageIsHighlighted(S.channel?.id, m.seq)`,
		`" notification-target"`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("mention target highlight behavior is missing %q", expected)
		}
	}

	stylesheet := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{
		`.message.notification-target`,
		`@keyframes notification-target-pulse`,
		`@media (prefers-reduced-motion: reduce)`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("mention target highlight style is missing %q", expected)
		}
	}
}

func TestApplicationIncludesCollapsibleChannelInfoAndAdminMenu(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	adminActionIDs := []string{`id="groups"`, `id="users"`, `id="addChannel"`, `id="manageChannels"`}
	previousPosition := -1
	for _, id := range adminActionIDs {
		position := strings.Index(page, id)
		if position < 0 || position <= previousPosition {
			t.Fatalf("admin actions are not ordered from top-left to bottom-right as %v", adminActionIDs)
		}
		previousPosition = position
	}
	for _, expected := range []string{
		`id="channelInfoToggle"`,
		`aria-controls="channelInfoPanel"`,
		`id="channelInfoPanel"`,
		`class="right offcanvas offcanvas-end"`,
		`id="adminToggle"`,
		`aria-controls="adminActions"`,
		`id="adminActions"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("application page does not contain collapsible UI hook %q", expected)
		}
	}

	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`window.matchMedia("(min-width: 981px)")`,
		`minihub:ui:${S.me?.id || "anonymous"}:${name}`,
		`bootstrap.Offcanvas.getOrCreateInstance(channelInfoPanel).show()`,
		`channelInfoPanel.addEventListener("hidden.bs.offcanvas"`,
		`setAdminMenuCollapsed(`,
		`readUIPreference("channel-info-collapsed")`,
		`readUIPreference("admin-menu-collapsed")`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("application script does not contain collapsible UI behavior %q", expected)
		}
	}

	stylesheet := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{
		`.app.channel-info-collapsed`,
		`--bs-offcanvas-width: min(320px, calc(100vw - 24px))`,
		`.admin-menu.collapsed .admin-toggle svg`,
		`.admin-toggle:focus-visible`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("application stylesheet does not contain collapsible UI rule %q", expected)
		}
	}
}

func TestApplicationIncludesCompactCollapsibleChannelNavigation(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	for _, expected := range []string{
		`class="channel-nav"`,
		`id="publicToggle"`,
		`aria-controls="public"`,
		`id="privateToggle"`,
		`id="publicUnread"`,
		`id="privateUnread"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("application page does not contain channel navigation hook %q", expected)
		}
	}

	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`function setChannelSectionCollapsed(type, collapsed, persist = true)`,
		"readUIPreference(`${type}-channels-collapsed`)",
		`function updateChannelSectionSummary(type, channels)`,
		`Number(member(right)) - Number(member(left))`,
		`"グループ経由で参加"`,
		`"未参加"`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("application script does not contain channel navigation behavior %q", expected)
		}
	}

	stylesheet := request(t, handler, "/css/app.css").Body.String()
	for _, expected := range []string{
		`.channel-nav {`,
		`.channel-section.collapsed .channel-section-chevron`,
		`.channel-section.collapsed.contains-active`,
		`min-height: 32px`,
		`.channel-name {`,
	} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("application stylesheet does not contain channel navigation rule %q", expected)
		}
	}
}

func TestMentionMenuUsesEffectiveChannelMembers(t *testing.T) {
	script := request(t, Handler(testAuthenticator(false)), "/js/app.js").Body.String()
	if !strings.Contains(script, `channel?.effectiveMembers || []`) {
		t.Fatal("mention menu does not include group-derived effective channel members")
	}
	if strings.Contains(script, `const allowed = new Set(S.channel?.members || []);`) {
		t.Fatal("mention menu still limits individual mentions to direct channel members")
	}
}

func TestGroupManagerSupportsDetailMembershipAndReadOnlyUserGroups(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	for _, expected := range []string{
		`class="modal-body p-0 group-manager"`,
		`id="groupSearch"`,
		`id="groupMembers"`,
		`id="groupCandidates"`,
		`id="deleteGroup"`,
		`id="editgroups" class="readonly-groups"`,
		`所属グループの変更はグループ管理から行ってください。`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("group manager page does not contain %q", expected)
		}
	}
	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{
		`method: "PATCH"`,
		`changeManagedGroupMembers(add, remove)`,
		`window.confirm(message)`,
		`renderReadOnlyGroups(u.groups)`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("group manager script does not contain %q", expected)
		}
	}
	if strings.Contains(script, `groups: selectedGroups("editgroups")`) {
		t.Fatal("user editor still submits group membership changes")
	}
}

func TestChannelManagerUIIncludesDelegationAndArchiveControls(t *testing.T) {
	handler := Handler(testAuthenticator(true))
	page := request(t, handler, "/").Body.String()
	for _, expected := range []string{`id="manageChannels"`, `id="manageCurrentChannel"`, `id="channelManagementModal"`, `id="channelMembershipPanel"`, `id="channelMembershipSearch"`, `id="channelMembershipSort"`, `data-membership-tab="members"`, `data-membership-tab="groups"`, `data-membership-tab="managers"`, `id="archiveChannel"`, `id="restoreChannel"`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("channel manager page does not contain %q", expected)
		}
	}
	script := request(t, handler, "/js/app.js").Body.String()
	for _, expected := range []string{`management=true`, `/members`, `addManagers`, `channels_changed`, `clearSelectedChannel()`} {
		if !strings.Contains(script, expected) {
			t.Fatalf("channel manager script does not contain %q", expected)
		}
	}
}

func TestReactionPickerInteractionRules(t *testing.T) {
	script := request(t, Handler(testAuthenticator(false)), "/js/app.js").Body.String()
	for _, expected := range []string{
		`["eyes", "hourglass-split", "確認中"]`,
		`reactionBar(m.seq, m.userId !== S.me.id)`,
		`document.addEventListener("click", () => closeReactionPickers())`,
		`closeReactionPickers(picker);`,
		`setReactionPickerOpen(picker, false);`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("application script does not enforce reaction picker rule %q", expected)
		}
	}
	if strings.Contains(script, `["eyes", "eye", "確認中"]`) {
		t.Fatal("確認中リアクションが従来の目のアイコンのままです")
	}
}

func TestReactionUsersAreLoadedOnDemand(t *testing.T) {
	script := request(t, Handler(testAuthenticator(false)), "/js/app.js").Body.String()
	for _, expected := range []string{
		`/reactions/${encodeURIComponent(key)}/users`,
		`wrap.addEventListener("mouseenter", loadUsers)`,
		`button.addEventListener("focus", loadUsers)`,
		`S.reactionUsers.set(cacheKey, pending)`,
		`invalidateReactionUsers(message.channelId, message.messageSeq)`,
		`S.directory.get(userID) || `,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("application script does not implement reaction-user behavior %q", expected)
		}
	}
	stylesheet := request(t, Handler(testAuthenticator(false)), "/css/app.css").Body.String()
	for _, expected := range []string{".reaction-users-tooltip", ".reaction-users-list"} {
		if !strings.Contains(stylesheet, expected) {
			t.Fatalf("application stylesheet does not contain %q", expected)
		}
	}
}

func TestDirectLoginHTMLRedirectsToCanonicalURL(t *testing.T) {
	recorder := request(t, Handler(testAuthenticator(false)), "/login.html")
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestPagesRejectUnsupportedMethods(t *testing.T) {
	handler := Handler(testAuthenticator(false))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", nil))
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func request(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

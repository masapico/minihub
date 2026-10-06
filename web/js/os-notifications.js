// OS notifications are optional. The WebSocket and in-app notification paths do not depend on them.
const osNotificationSetting = document.createElement("div");
osNotificationSetting.className = "os-notification-setting";
osNotificationSetting.innerHTML = '<button id="osNotificationToggle" class="btn btn-outline-secondary btn-sm" type="button">OS通知を有効にする</button><span id="osNotificationStatus" class="muted small" role="status"></span>';
$("notificationCenter").querySelector(".offcanvas-body").prepend(osNotificationSetting);

let osFocusRelease = null;
let osFocusPending = false;

function osNotificationsAllowed() {
    return document.body.dataset.osNotificationsEnabled !== "false";
}

function osNotificationStorageAvailable() {
    try {
        const key = "minihub:os-notification-storage-check";
        localStorage.setItem(key, "1");
        localStorage.removeItem(key);
        return true;
    } catch (_) {
        return false;
    }
}

function osNotificationSupported() {
    return osNotificationsAllowed() && window.isSecureContext && typeof Notification !== "undefined" &&
        typeof navigator.locks?.request === "function" &&
        typeof navigator.locks?.query === "function" && osNotificationStorageAvailable();
}

function osNotificationEnabled() {
    return !!S.me && osNotificationSupported() &&
        Notification.permission === "granted" && readUIPreference("os-notifications");
}

function renderOSNotificationSetting() {
    const button = $("osNotificationToggle"), status = $("osNotificationStatus");
    if (!osNotificationsAllowed()) {
        button.disabled = true;
        button.textContent = "OS通知を有効にする";
        status.textContent = "管理者の設定によりOS通知は利用できません";
    } else if (!S.me) {
        button.disabled = true;
        status.textContent = "ログイン後に設定できます";
    } else if (!osNotificationSupported()) {
        button.disabled = true;
        button.textContent = "OS通知を有効にする";
        status.textContent = "この接続では利用できません（HTTPSまたはEdgeの管理設定が必要です）";
    } else if (Notification.permission === "denied") {
        button.disabled = true;
        button.textContent = "OS通知を有効にする";
        status.textContent = "ブラウザのサイト設定で通知を許可してください";
    } else {
        const enabled = osNotificationEnabled();
        button.disabled = false;
        button.textContent = enabled ? "OS通知を停止する" : "OS通知を有効にする";
        status.textContent = enabled ? "別の画面を見ている間に通知します" : "OS通知は停止中です";
    }
}

$("osNotificationToggle").onclick = async () => {
    if (!S.me || !osNotificationSupported()) return;
    if (osNotificationEnabled()) {
        saveUIPreference("os-notifications", false);
    } else {
        try {
            const permission = Notification.permission === "default"
                ? await Notification.requestPermission() : Notification.permission;
            if (permission === "granted" && osNotificationsAllowed()) saveUIPreference("os-notifications", true);
        } catch (_) {
            // A rejected permission request must not affect chat.
        }
    }
    renderOSNotificationSetting();
};

function osFocusLockName() {
    return Minihub.storageKey(`minihub:os-notifications:focused:${S.me.id}`);
}

function updateOSFocusLock() {
    if (!osNotificationsAllowed()) {
        if (osFocusRelease) osFocusRelease();
        osFocusRelease = null;
        return;
    }
    if (!S.me || !osNotificationSupported()) return;
    if (document.visibilityState !== "visible" || !document.hasFocus()) {
        if (osFocusRelease) osFocusRelease();
        osFocusRelease = null;
        return;
    }
    if (osFocusRelease || osFocusPending) return;
    osFocusPending = true;
    navigator.locks.request(osFocusLockName(), {mode: "shared"}, async () => {
        osFocusPending = false;
        if (!osNotificationsAllowed() || document.visibilityState !== "visible" || !document.hasFocus()) return;
        let release;
        await new Promise((resolve) => { release = resolve; osFocusRelease = resolve; });
        if (osFocusRelease === release) osFocusRelease = null;
    }).catch(() => { osFocusPending = false; });
}

window.addEventListener("focus", updateOSFocusLock);
window.addEventListener("blur", updateOSFocusLock);
document.addEventListener("visibilitychange", updateOSFocusLock);
window.addEventListener("storage", (event) => {
    if (event.key === uiStorageKey("os-notifications")) renderOSNotificationSetting();
});
$("notificationCenter").addEventListener("show.bs.offcanvas", renderOSNotificationSetting);
window.osNotificationsSetup = () => {
    renderOSNotificationSetting();
    updateOSFocusLock();
};
if (S.me) window.osNotificationsSetup();
else renderOSNotificationSetting();

async function openOSNotificationTarget(target) {
    try {
        if (target.mention) {
            await loadMentions();
            const mention = S.mentions.find((item) => item.channelId === target.channelId &&
                item.message.seq === target.seq);
            if (mention) {
                await openMention(mention);
                return;
            }
        }
        if (target.threadRootSeq) {
            if (!await openThread(target.channelId, target.threadRootSeq, target.seq))
                throw Error("返信を開けませんでした");
            return;
        }
        await select(target.channelId);
        if (S.channel?.id !== target.channelId) return;
        const page = await api(`/api/channels/${encodeURIComponent(target.channelId)}/messages?around=${target.seq}&limit=100`);
        if (S.channel?.id !== target.channelId) return;
        S.msgs = page.messages;
        S.nextBefore = page.nextBefore;
        render();
        focusMessage(target.seq, target.channelId, true);
    } catch (error) {
        note(`通知を開けません: ${error.message}`, true);
    }
}

async function showOSNotification(target) {
    if (!osNotificationEnabled() || !target.channelId || !Number.isSafeInteger(target.seq) || target.seq < 1) return;
    const userID = S.me.id;
    const entry = `${target.channelId}:${target.seq}`;
    try {
        await navigator.locks.request(Minihub.storageKey(`minihub:os-notifications:display:${userID}`), async () => {
            if (S.me?.id !== userID || !osNotificationEnabled()) return;
            if (document.visibilityState === "visible" && document.hasFocus()) return;
            const focus = await navigator.locks.query();
            if (S.me?.id !== userID || !osNotificationEnabled()) return;
            if (focus.held.some((lock) => lock.name === osFocusLockName())) return;
            const storageKey = Minihub.storageKey(`minihub:os-notifications:shown:${userID}`);
            const now = Date.now();
            let shown = [];
            try {
                const stored = JSON.parse(localStorage.getItem(storageKey) || "[]");
                if (Array.isArray(stored)) shown = stored.filter((item) =>
                    Array.isArray(item) && typeof item[0] === "string" &&
                    Number.isFinite(item[1]) && now - item[1] < 86400000);
            } catch (_) {}
            if (shown.some((item) => item[0] === entry)) return;
            const notification = new Notification(`# ${target.channelName}`, {
                body: target.body,
                tag: Minihub.storageKey(`minihub:${userID}:${entry}`),
            });
            notification.onclick = () => {
                notification.close();
                try { window.focus(); } catch (_) {}
                void openOSNotificationTarget(target);
            };
            shown.push([entry, now]);
            localStorage.setItem(storageKey, JSON.stringify(shown.slice(-256)));
        });
    } catch (_) {
        // Permission, storage, and platform errors are confined to OS notifications.
    }
}

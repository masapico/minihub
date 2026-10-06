// A picker instance owns its thread, text range and async request. Closing it
// invalidates any in-flight response without disturbing the reply draft.
let threadMention = null;
let threadMentionComposing = false;
let threadMentionCompositionEnd = 0;

function closeThreadMention(restoreFocus = false) {
    const picker = threadMention;
    threadMention = null;
    if (picker?.presenceChannel) unwatchMentionPresence(picker.presenceChannel);
    $("threadMentionMenu").classList.add("hidden");
    $("threadMentionButton").setAttribute("aria-expanded", "false");
    $("threadMentionSearch").setAttribute("aria-expanded", "false");
    $("threadMentionOptions").replaceChildren();
    for (const id of ["threadInput", "threadMentionSearch"]) $(id).removeAttribute("aria-activedescendant");
    if (restoreFocus && picker?.thread === T.current) $("threadInput").focus({preventScroll: true});
}

function threadMentionBlocksSubmit() {
    return !!threadMention || threadMentionComposing || performance.now() < threadMentionCompositionEnd;
}

function typedThreadMention() {
    const input = $("threadInput");
    const caret = currentRichEditorRange(input);
    if (!caret?.collapsed || caret.endContainer.nodeType !== Node.TEXT_NODE) return null;
    const before = caret.endContainer.nodeValue.slice(0, caret.endOffset);
    const match = before.match(/(?:^|\s)@([^\s@（）]*)$/u);
    if (!match) return null;
    const range = caret.cloneRange();
    range.setStart(caret.endContainer, caret.endOffset - match[1].length - 1);
    return { range, query: match[1] };
}

function openThreadMentionPicker(mode = "button", typed = null) {
    if (!T.current || $("threadInput").disabled || threadMentionComposing) return;
    closeThreadMention();
    const input = $("threadInput");
    let range = typed?.range || currentRichEditorRange(input);
    if (!range) {
        range = document.createRange();
        range.selectNodeContents(input);
        range.collapse(false);
    }
    const picker = {
        thread: T.current, mode,
        range,
        value: input.value, query: typed?.query || "",
        channel: null, loading: true, error: "", matches: [], active: 0,
    };
    threadMention = picker;
    $("threadMentionMenu").classList.remove("hidden");
    $("threadMentionButton").setAttribute("aria-expanded", "true");
    $("threadMentionSearch").setAttribute("aria-expanded", "true");
    $("threadMentionSearch").value = picker.query;
    renderThreadMention(picker);
    if (mode === "button") $("threadMentionSearch").focus({preventScroll: true});
    refreshThreadMention(picker);
}

async function refreshThreadMention(picker) {
    if (threadMention !== picker) return;
    picker.loading = true;
    picker.error = "";
    picker.channel = null;
    renderThreadMention(picker);
    try {
        const channel = await api(`/api/channels/${encodeURIComponent(picker.thread.channel)}`);
        if (threadMention !== picker || T.current !== picker.thread) return;
        if (channel.id !== picker.thread.channel || channel.archivedAt || !member(channel)) {
            closeThreadMention();
            $("threadMentionButton").disabled = true;
            $("threadInput").disabled = true;
            $("threadSend").disabled = true;
            $("threadInputHint").textContent = "返信する権限が変更されました。チャンネルを開き直してください";
            return;
        }
        picker.channel = channel;
        picker.presenceChannel = channel.id;
        watchMentionPresence(channel.id);
    } catch (error) {
        if (threadMention !== picker || T.current !== picker.thread) return;
        picker.error = `参加者を取得できません: ${error.message}`;
    } finally {
        if (threadMention === picker && T.current === picker.thread) {
            picker.loading = false;
            renderThreadMention(picker);
        }
    }
}

function renderThreadMention(picker) {
    if (threadMention !== picker || picker.thread !== T.current) return;
    const host = $("threadMentionOptions");
    host.replaceChildren();
    picker.matches = picker.channel ? channelMentionCandidates(picker.channel, picker.query) : [];
    picker.active = Math.max(0, Math.min(picker.active, picker.matches.length - 1));
    $("threadMentionRetry").classList.toggle("hidden", !picker.error);
    host.setAttribute("aria-busy", String(picker.loading));
    $("threadMentionStatus").textContent = picker.loading ? "参加者を読み込んでいます…" : picker.error ||
        (picker.matches.length ? `${picker.matches.length}人の候補 · ↑↓で選択、Enterで確定` :
            picker.query ? "名前に一致する参加者はいません" : "メンションできる参加者はいません");
    for (const [index, user] of picker.matches.entries()) {
        const option = document.createElement("button");
        option.type = "button";
        option.id = `threadMentionOption${index}`;
        option.className = "mention-option thread-mention-option";
        option.tabIndex = -1;
        option.setAttribute("role", "option");
        const avatar = document.createElement("span"), copy = document.createElement("span"),
            name = document.createElement("b"), id = document.createElement("small");
        avatar.className = "mini";
        avatar.textContent = ini(user.name);
        name.textContent = user.name;
        id.textContent = `@${user.id}`;
        copy.append(name, id);
        option.append(avatar, copy);
        option.dataset.userId = user.id;
        option.dataset.presenceChannel = picker.channel.id;
        updateMentionPresence(option);
        // Preserve the editor selection; touch scrolling is left to the browser.
        option.addEventListener("mousedown", event => event.preventDefault());
        option.onclick = () => chooseThreadMention(picker, user);
        host.append(option);
    }
    highlightThreadMention(picker);
}

function highlightThreadMention(picker) {
    const options = $("threadMentionOptions").children;
    for (let i = 0; i < options.length; i++) options[i].setAttribute("aria-selected", String(i === picker.active));
    const selected = options[picker.active];
    for (const id of ["threadInput", "threadMentionSearch"]) {
        if (selected) $(id).setAttribute("aria-activedescendant", selected.id);
        else $(id).removeAttribute("aria-activedescendant");
    }
    if (selected) {
        const host = $("threadMentionOptions");
        if (selected.offsetTop < host.scrollTop) host.scrollTop = selected.offsetTop;
        else if (selected.offsetTop + selected.offsetHeight > host.scrollTop + host.clientHeight)
            host.scrollTop = selected.offsetTop + selected.offsetHeight - host.clientHeight;
    }
}

function chooseThreadMention(picker, user) {
    const input = $("threadInput");
    if (threadMention !== picker || picker.thread !== T.current || input.disabled ||
        picker.loading || picker.error || threadMentionComposing) return;
    // A range captured before an edit is never applied to a different draft.
    if (input.value !== picker.value) { closeThreadMention(true); return; }
    const before = picker.range.cloneRange();
    before.selectNodeContents(input);
    before.setEnd(picker.range.startContainer, picker.range.startOffset);
    const prefix = before.toString() && !/[ \t\r\n\v\f]$/.test(before.toString()) ? " " : "";
    const mention = document.createElement("span");
    mention.className = "mention";
    mention.contentEditable = "false";
    mention.dataset.mentionToken = mentionToken(user.id, user.name);
    mention.textContent = `@${user.name}`;
    mention.title = `@${user.id}`;
    closeThreadMention();
    input.focus({preventScroll: true});
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(picker.range);
    const wrapper = document.createElement("div");
    wrapper.append(document.createTextNode(prefix), mention, document.createTextNode(" "));
    if (!document.execCommand("insertHTML", false, wrapper.innerHTML)) {
        picker.range.deleteContents();
        const space = document.createTextNode(" ");
        picker.range.insertNode(space);
        picker.range.insertNode(mention);
        if (prefix) picker.range.insertNode(document.createTextNode(prefix));
        picker.range.setStartAfter(space);
        picker.range.collapse(true);
        selection.removeAllRanges();
        selection.addRange(picker.range);
    }
    input.dispatchEvent(new Event("input", {bubbles: true}));
}

function updateTypedThreadMention() {
    if (threadMentionComposing || !T.current || $("threadInput").disabled) return;
    const typed = typedThreadMention();
    if (!typed) { if (threadMention) closeThreadMention(); return; }
    const picker = threadMention;
    if (!picker || picker.mode !== "typed" || picker.thread !== T.current) {
        openThreadMentionPicker("typed", typed);
        return;
    }
    picker.range = typed.range;
    picker.value = $("threadInput").value;
    picker.query = typed.query;
    picker.active = 0;
    $("threadMentionSearch").value = typed.query;
    renderThreadMention(picker);
}

function handleThreadMentionKey(event) {
    if (event.isComposing || event.keyCode === 229 || threadMentionComposing ||
        event.key === "Enter" && performance.now() < threadMentionCompositionEnd) {
        event.stopPropagation();
        return true;
    }
    const picker = threadMention;
    if (!picker) return false;
    if (event.key === "Escape") {
        event.preventDefault(); event.stopPropagation(); closeThreadMention(true); return true;
    }
    if (event.key === "Enter") {
        event.preventDefault(); event.stopPropagation();
        if (!event.ctrlKey && !event.metaKey && !event.altKey && picker.matches[picker.active])
            chooseThreadMention(picker, picker.matches[picker.active]);
        return true;
    }
    if (["ArrowDown", "ArrowUp"].includes(event.key)) {
        event.preventDefault(); event.stopPropagation();
        if (picker.matches.length) {
            const direction = event.key === "ArrowDown" ? 1 : -1;
            picker.active = (picker.active + direction + picker.matches.length) % picker.matches.length;
            highlightThreadMention(picker);
        }
        return true;
    }
    if (event.key === "Tab") closeThreadMention();
    return false;
}

$("threadMentionButton").onclick = () => {
    if (threadMention) closeThreadMention(true);
    else openThreadMentionPicker();
};
$("threadMentionSearch").oninput = () => {
    if (!threadMention || threadMentionComposing) return;
    threadMention.query = $("threadMentionSearch").value;
    threadMention.active = 0;
    renderThreadMention(threadMention);
};
$("threadMentionSearch").onkeydown = handleThreadMentionKey;
$("threadMentionRetry").onclick = () => { if (threadMention) refreshThreadMention(threadMention); };
$("threadInput").addEventListener("input", updateTypedThreadMention);
$("threadInput").addEventListener("click", () => {
    if (threadMention?.mode === "button") closeThreadMention();
    else updateTypedThreadMention();
});
$("threadInput").addEventListener("keyup", event => {
    if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) updateTypedThreadMention();
});
for (const id of ["threadInput", "threadMentionSearch"]) {
    $(id).addEventListener("blur", () => { threadMentionComposing = false; });
    $(id).addEventListener("compositionstart", () => { threadMentionComposing = true; });
    $(id).addEventListener("compositionend", () => {
        threadMentionComposing = false;
        threadMentionCompositionEnd = performance.now() + 100;
        if (id === "threadInput") updateTypedThreadMention();
        else $("threadMentionSearch").dispatchEvent(new Event("input"));
    });
}
document.addEventListener("pointerdown", event => {
    if (!$("threadMentionMenu").contains(event.target) && event.target !== $("threadMentionButton") && event.target !== $("threadInput"))
        closeThreadMention();
});
$("threadForm").addEventListener("focusout", () => {
    queueMicrotask(() => {
        if (!$("threadForm").contains(document.activeElement)) closeThreadMention();
    });
});

// Keep the composer and picker above the on-screen keyboard on narrow screens.
function positionThreadViewport() {
    const panel = $("threadPanel"), viewport = window.visualViewport;
    if (viewport && window.matchMedia("(max-width:1050px)").matches) {
        panel.style.setProperty("--thread-viewport-height", `${viewport.height}px`);
        panel.style.setProperty("--thread-viewport-top", `${viewport.offsetTop}px`);
    } else {
        panel.style.removeProperty("--thread-viewport-height");
        panel.style.removeProperty("--thread-viewport-top");
    }
}
window.visualViewport?.addEventListener("resize", positionThreadViewport);
window.visualViewport?.addEventListener("scroll", positionThreadViewport);
window.addEventListener("resize", positionThreadViewport);
positionThreadViewport();

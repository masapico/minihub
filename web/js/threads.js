// Thread bodies have their own cursors and are fetched only while the panel is open.
const T = {
    current: null, drafts: new Map(), mainDrafts: new Map(), summaries: new Map(),
    list: [], listCursor: "", listChannel: "", listToken: 0, participatingCount: 0,
    refreshTimer: null, events: new Map(), returnFocus: null,
    stagedAttachments: [],
};
const threadKey = (channel, root) => `${channel}:${root}`;
const threadURL = (t) => `/api/channels/${encodeURIComponent(t.channel)}/threads/${t.root}`;
let updateThreadEditorAppearance = () => {};
function renderThreadStagedAttachments() {
    const host = $("threadAttachedFiles");
    if (!host) return;
    host.replaceChildren();
    if (!T.stagedAttachments || !T.stagedAttachments.length) {
        host.classList.add("hidden");
        return;
    }
    host.classList.remove("hidden");
    for (let i = 0; i < T.stagedAttachments.length; i++) {
        const att = T.stagedAttachments[i];
        const chip = document.createElement("div");
        chip.className = "attached-file-chip";
        chip.innerHTML = `<span class="chip-icon">${icon("paperclip")}</span><span class="chip-name"></span><span class="chip-size"></span><button type="button" class="chip-remove" aria-label="添付を解除">&times;</button>`;
        chip.querySelector(".chip-name").textContent = att.name;
        chip.querySelector(".chip-size").textContent = formatFileSize(att.size);
        chip.querySelector(".chip-remove").onclick = () => {
            T.stagedAttachments.splice(i, 1);
            renderThreadStagedAttachments();
            syncThreadEditorState();
        };
        host.append(chip);
    }
}
function syncThreadEditorState() {
    updateThreadEditorAppearance();
    const text = $("threadInput").value;
    const hasAI = textHasAIMention(text);
    const hasAttachments = (T.stagedAttachments || []).length > 0;
    const canPost = S.channel?.id === T.current?.channel && member(S.channel) && !S.channel.archivedAt && !T.current?.parent?.withdrawnAt;
    const attachBtn = $("threadAttachButton");
    if (attachBtn) {
        attachBtn.disabled = !canPost || $("threadInput").disabled || !hasAI;
        attachBtn.title = hasAI ? "ファイルを添付" : "ファイルを添付 (@ai:メンション時のみ有効)";
    }
    const attachWithoutAI = hasAttachments && !hasAI;
    if (attachWithoutAI) {
        $("threadInputHint").textContent = "添付ファイルはAI宛てメッセージ（@ai:...）にのみ送信できます";
        $("threadSend").disabled = true;
    } else if (canPost) {
        $("threadInputHint").textContent = "";
        $("threadSend").disabled = !canPost || T.current?.sending || T.current?.loading;
    }
}
updateThreadEditorAppearance = installRichEditor($("threadInput"), {
    toolbar: document.querySelector(".thread-tools"),
    limit: $("threadEditorLimit"),
    sendButton: $("threadSend"),
    sync: syncThreadEditorState,
});
$("threadLinkButton").onclick = () => openEditorLink($("threadInput"));
if ($("threadAttachButton") && $("threadAttachFileInput")) {
    $("threadAttachButton").onclick = () => $("threadAttachFileInput").click();
    $("threadAttachFileInput").onchange = async (event) => {
        const files = Array.from(event.target.files || []);
        event.target.value = "";
        if (!files.length) return;
        if (!T.current) {
            note("スレッドを開いてください", true);
            return;
        }
        if ((T.stagedAttachments.length + files.length) > 5) {
            note("添付できるファイルは最大5件までです", true);
            return;
        }
        for (const f of files) {
            if (!isAllowedAttachmentFile(f.name)) {
                note(`未対応のファイル形式です: ${f.name}（Office文書、PDF、テキスト/データ形式のみ対応）`, true);
                return;
            }
            if (f.size > 20 * 1024 * 1024) {
                note(`ファイルサイズが20MBを超えています: ${f.name}`, true);
                return;
            }
        }
        const currentTotal = T.stagedAttachments.reduce((sum, a) => sum + (a.size || 0), 0);
        const newTotal = files.reduce((sum, f) => sum + f.size, 0);
        if (currentTotal + newTotal > 50 * 1024 * 1024) {
            note("添付ファイルの合計サイズは50MB以下にしてください", true);
            return;
        }
        $("threadAttachButton").disabled = true;
        try {
            for (const f of files) {
                const att = await uploadAttachment(T.current.channel, f);
                T.stagedAttachments.push(att);
            }
        } catch (err) {
            note(err.message, true);
        } finally {
            $("threadAttachButton").disabled = false;
            renderThreadStagedAttachments();
            syncThreadEditorState();
        }
    };
}

function absorbThreadSummaries(path, page) {
    const match = path.match(/^\/api\/channels\/([^/]+)\/(messages|thread-summaries)(?:\?|$)/);
    if (!match || !page?.threads) return;
    const channel = decodeURIComponent(match[1]);
    for (const summary of Object.values(page.threads)) T.summaries.set(threadKey(channel, summary.rootSeq), summary);
    updateReplyButtons();
}
function replyLabel(summary) {
    if (!summary?.replyCount) return "返信";
    return `返信${summary.replyCount}件 · ${new Date(summary.updatedAt).toLocaleTimeString("ja-JP", {hour:"2-digit",minute:"2-digit"})}${summary.unreadCount ? " · 未読あり" : ""}`;
}
function replyButton(message) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "btn btn-outline-secondary reply-button";
    button.dataset.threadRoot = String(message.seq);
    const channel = S.channel.id, summary = T.summaries.get(threadKey(channel, message.seq));
    button.textContent = replyLabel(summary);
    button.classList.toggle("has-updates", !!summary?.unreadCount);
    button.onclick = () => openThread(channel, message.seq).catch(error=>note(error.message,true));
    return button;
}
function updateReplyButtons() {
    document.querySelectorAll("#messages [data-thread-root]").forEach((button) => {
        const summary = T.summaries.get(threadKey(S.channel?.id, button.dataset.threadRoot));
        button.textContent = replyLabel(summary);
        button.classList.toggle("has-updates", !!summary?.unreadCount);
    });
}
function updateThreadControls() {
    updateThreadScope();
    if (T.current) {
        const canPost = S.channel?.id === T.current.channel && member(S.channel) && !S.channel.archivedAt && !T.current.parent?.withdrawnAt;
        const text = $("threadInput").value;
        const hasAI = textHasAIMention(text);
        const hasAttachments = (T.stagedAttachments || []).length > 0;
        const attachWithoutAI = hasAttachments && !hasAI;
        $("threadSend").disabled = !canPost || T.current.sending || T.current.loading || attachWithoutAI;
        $("threadInput").disabled = !canPost || T.current.loading || T.current.sending;
        $("threadMentionButton").disabled = $("threadInput").disabled;
        $("threadAIButton").disabled = $("threadInput").disabled;
        const attachBtn = $("threadAttachButton");
        if (attachBtn) {
            attachBtn.disabled = $("threadInput").disabled || !hasAI;
            attachBtn.title = hasAI ? "ファイルを添付" : "ファイルを添付 (@ai:メンション時のみ有効)";
        }
        if (!canPost || T.current.sending) {
            if (typeof closeThreadMention === "function") closeThreadMention();
        }
        if (attachWithoutAI) {
            $("threadInputHint").textContent = "添付ファイルはAI宛てメッセージ（@ai:...）にのみ送信できます";
        } else {
            $("threadInputHint").textContent = canPost ? "" : "返信するにはチャンネルへ参加してください";
        }
    }
}
function threadChannelChanging(id) {
    if (typeof closeThreadMention === "function") closeThreadMention();
    if (S.channel) T.mainDrafts.set(S.channel.id, $("input").value);
    if (S.channel?.id !== id) {
        closeThread(false);
        $("input").value = T.mainDrafts.get(id) || "";
    }
}
function closeThread(restoreFocus = true) {
    if (typeof closeThreadMention === "function") closeThreadMention();
    if (S.typingScope?.threadRootSeq) stopTyping();
    T.stagedAttachments = [];
    renderThreadStagedAttachments();
    if (T.current) {
        T.drafts.set(threadKey(T.current.channel, T.current.root), $("threadInput").value);
        clearTimeout(T.current.readTimer);
    }
    T.current = null;
    renderTypingStatus();
    updateThreadBackground();
    $("threadPanel").classList.add("hidden");
    document.querySelector(".app").classList.remove("thread-open");
    $("threadReplies").replaceChildren(); $("threadRoot").replaceChildren();
    if (T.returnScroll != null) $("messages").scrollTop = T.returnScroll;
    T.returnScroll = null;
    if (restoreFocus) {
        const element = T.returnFocus?.isConnected ? T.returnFocus : $("messages");
        element.focus({preventScroll:true});
    }
}
async function openThread(channel, root, around = null) {
    if (S.channel?.id !== channel) await select(channel);
    if (S.channel?.id !== channel) throw Error("チャンネルを開けませんでした");
    closeThread(false);
    T.returnFocus = document.activeElement;
    T.returnScroll = $("messages").scrollTop;
    const t = {channel, root, messages:[], summary:null, before:null, after:null, seen:new Set(), loading:false, sending:false, reading:false, readTimer:null};
    T.current = t;
    renderTypingStatus();
    updateThreadBackground();
    $("threadPanel").classList.remove("hidden");
    document.querySelector(".app").classList.add("thread-open");
    $("threadChannel").textContent = `# ${S.channel.name}`;
    $("threadInput").value = T.drafts.get(threadKey(channel,root)) || "";
    $("threadClose").focus({preventScroll:true});
    const ok = await loadThreadPage(t, around ? `around=${around}` : "", "replace", around);
    return ok;
}
function threadMessage(message) {
    const article = document.createElement("article");
    article.className = "message" + (message.userId === S.me.id ? " mine" : "") + (mentionsMe(message.text, message.ai) ? " mentioned" : "") + (message.withdrawnAt ? " withdrawn" : "");
    article.dataset.seq = String(message.seq); article.tabIndex = -1;
    const head = document.createElement("div"), text = document.createElement("div");
    head.className = "head"; text.className = "text";
    head.textContent = messageAuthor(message);
    if (message.userId === S.me.id) {
        const mine = document.createElement("em");
        mine.className = "mine-label";
        mine.textContent = "自分";
        head.append(mine);
    }
    head.append(document.createTextNode(` · ${new Date(message.ts).toLocaleString("ja-JP")}`));
    text.append(message.withdrawnAt ? withdrawnNotice(message) : fragment(messageDisplayText(message)));
    if (!message.withdrawnAt && message.attachments && message.attachments.length) {
        const atts = renderAttachments(message.attachments);
        if (atts) text.append(atts);
    }
    article.append(head,text);
    if (message.threadRootSeq && !message.withdrawnAt) article.append(reactionBar(message.seq, message.userId !== S.me.id));
    if ((message.withdrawnKind==="message" || !message.withdrawnAt) && (message.userId === S.me.id || S.me.role === "admin" || (S.channel?.managers || []).includes(S.me.id) && member(S.channel))) {
        const restoring=!!message.withdrawnAt;
        const button=document.createElement("button");button.type="button";button.className=restoring?"btn btn-sm message-withdraw-action message-restore-action":"btn btn-sm message-withdraw-action";button.textContent=restoring?"取り消しを戻す":"取り消す";
        button.onclick=async()=>{if(!confirm(restoring?"取り消しを戻し、元のメッセージを再表示しますか？":"この投稿を取り消しますか？ 元の内容は保存されます。"))return;button.disabled=true;try{const updated=await api(`/api/channels/${encodeURIComponent(S.channel.id)}/messages/${message.seq}/${restoring?"restore":"withdraw"}`,{method:"POST"});if(T.current){if(T.current.parent?.seq===message.seq)T.current.parent=updated;T.current.messages=T.current.messages.map(m=>m.seq===message.seq?updated:m);renderThread(T.current)}}catch(e){button.disabled=false;note(e.message,true)}};
        article.append(button);
    }
    return article;
}
function refreshThreadReaction(messageSeq) {
    const t = T.current;
    if (!t || !t.messages.some(m => m.seq === messageSeq)) return;
    const message = t.messages.find(m => m.seq === messageSeq);
    const article = [...$("threadReplies").querySelectorAll(".message[data-seq]")]
        .find(el => Number(el.dataset.seq) === messageSeq);
    if (!article || message.withdrawnAt) return;
    const current = article.querySelector(".reactions");
    const updated = reactionBar(messageSeq, message.userId !== S.me.id);
    const focused = current?.contains(document.activeElement);
    const focusedKey = focused ? document.activeElement.dataset.reaction : null;
    if (current) current.replaceWith(updated);
    else article.querySelector(".text")?.after(updated);
    if (focused) {
        const replacement = [...updated.querySelectorAll("[data-reaction]")]
            .find(el => el.dataset.reaction === focusedKey);
        (replacement || updated.querySelector(".reaction-add-button") || article).focus({preventScroll:true});
    }
}
async function syncThreadReactions(t, messages = t.messages) {
    const seqs = messages.map(m => m.seq);
    if (!seqs.length) return;
    const views = [];
    for (let i = 0; i < seqs.length; i += 101) {
        const batch = await api(`/api/channels/${encodeURIComponent(t.channel)}/reactions?messageSeqs=${seqs.slice(i, i + 101).join(",")}`);
        if (T.current !== t || S.channel?.id !== t.channel) return;
        views.push(...batch);
    }
    if (T.current !== t || S.channel?.id !== t.channel) return;
    for (const seq of seqs) S.reactions.delete(seq);
    for (const view of views) applyReactionView(view);
}
function renderThread(t, focusSeq = null, keepScroll = true) {
    if (T.current !== t) return;
    const scroll = $("threadMessages"), previous = scroll.scrollTop;
    const rootLabel = document.createElement("div");
    rootLabel.className = "thread-root-label";
    rootLabel.textContent = "元のメッセージ";
    $("threadRoot").replaceChildren(rootLabel, threadMessage(t.parent));
    $("threadReplies").replaceChildren();
    let boundary = false;
    for (const m of t.messages) {
        if (!boundary && m.seq > t.summary.lastReadSeq && m.userId !== S.me.id) {
            const line=document.createElement("div");line.className="thread-unread-line";line.textContent="ここから未読";$("threadReplies").append(line);boundary=true;
        }
        $("threadReplies").append(threadMessage(m));
    }
    if (!t.messages.length) $("threadReplies").textContent="返信はまだありません";
    $("threadOlder").classList.toggle("hidden", !t.before);
    $("threadNewer").classList.toggle("hidden", !t.after && !t.pending);
    $("threadNewer").textContent = t.pending ? "新しい返信を読む" : "続きの返信を読む";
    $("threadReadAll").disabled = !t.summary.latestSeq || t.reading;
    if (focusSeq) {
        const target=$("threadReplies").querySelector(`[data-seq="${focusSeq}"]`);
        if (target) { target.classList.add("notification-target"); target.scrollIntoView({block:"center"}); target.focus({preventScroll:true}); }
    } else if (keepScroll) scroll.scrollTop=previous;
    updateThreadControls(); scheduleThreadRead(t);
}
async function loadThreadPage(t, query = "", mode = "replace", focusSeq = null) {
    if (T.current !== t || t.loading) return false;
    t.loading = true; updateThreadControls();
    $("threadStatus").textContent="返信を読み込んでいます…";$("threadRetry").classList.add("hidden");
    const scroll=$("threadMessages"), oldHeight=scroll.scrollHeight, oldTop=scroll.scrollTop;
    try {
        const page=await api(`${threadURL(t)}/messages?limit=50${query ? "&"+query : ""}`);
        if (T.current !== t) return false;
        t.parent=page.root;t.summary=page.summary;
        T.summaries.set(threadKey(t.channel,t.root),page.summary);updateReplyButtons();
        if(mode==="replace") { t.messages=page.messages;t.before=page.nextBefore;t.after=page.nextAfter; }
        else {
            const merged=new Map(t.messages.map(m=>[m.seq,m]));for(const m of page.messages)merged.set(m.seq,m);
            t.messages=[...merged.values()].sort((a,b)=>a.seq-b.seq);
            if(mode==="older")t.before=page.nextBefore;else t.after=page.nextAfter;
        }
        t.pending=Math.max(0, t.summary.replyCount && t.summary.latestSeq>(t.messages.at(-1)?.seq||0) ? 1 : 0);
        await syncThreadReactions(t, page.messages);
        if (T.current !== t) return false;
        renderThread(t,focusSeq || (mode==="replace" ? t.summary.firstUnreadSeq : null),mode!=="replace");
        if(mode==="older")scroll.scrollTop=oldTop+scroll.scrollHeight-oldHeight;
        if(mode==="replace" && !focusSeq && !t.summary.firstUnreadSeq)scroll.scrollTop=scroll.scrollHeight;
        $("threadStatus").textContent=`返信${t.summary.replyCount}件${t.summary.unreadCount ? ` · 未読${t.summary.unreadCount}件` : ""}`;
        if(focusSeq&&!t.messages.some(m=>m.seq===focusSeq))throw Error("対象の返信を表示できませんでした");
        return true;
    } catch(error) {
        if(T.current===t){$("threadStatus").textContent=error.message;$("threadRetry").classList.remove("hidden");t.retry={query,mode,focusSeq};}
        return false;
    } finally { if(T.current===t){t.loading=false;updateThreadControls();scheduleThreadRead(t);} }
}
function scheduleThreadRead(t) {
    clearTimeout(t?.readTimer);
    if (t && T.current===t) t.readTimer=setTimeout(()=>markVisibleThreadRead(t),700);
}
async function markVisibleThreadRead(t) {
    if(T.current!==t || t.loading || t.reading || !t.summary || document.visibilityState!=="visible" || document.querySelector(".offcanvas.show, .modal.show"))return;
    const viewport=$("threadMessages").getBoundingClientRect();
    for(const el of $("threadReplies").querySelectorAll("[data-seq]")){
        const rect=el.getBoundingClientRect();
        // A long message is read after its end has appeared, rather than merely its heading.
        if(rect.bottom<=viewport.bottom && rect.bottom>viewport.top && rect.top<viewport.bottom)t.seen.add(Number(el.dataset.seq));
    }
    let seq=t.summary.lastReadSeq, first=t.summary.firstUnreadSeq;
    if(first && !t.messages.some(m=>m.seq===first))return;
    for(const m of t.messages){
        if(m.seq<=seq)continue;
        if(first && m.seq<first)continue;
        if(m.userId!==S.me.id&&!t.seen.has(m.seq))break;
        seq=m.seq;
    }
    if(seq>t.summary.lastReadSeq) await writeThreadRead(t,seq);
}
async function writeThreadRead(t,seq) {
    if(t.reading)return;t.reading=true;
    try{
        await api(`${threadURL(t)}/read`,{method:"PUT",body:JSON.stringify({seq})});
        if(T.current!==t)return;
        t.summary.lastReadSeq=Math.max(t.summary.lastReadSeq,seq);
        const summaries=await api(`/api/channels/${encodeURIComponent(t.channel)}/thread-summaries?roots=${t.root}`);
        if(T.current!==t)return;
        if(summaries.threads[t.root])t.summary=summaries.threads[t.root];
        $("threadStatus").textContent=`返信${t.summary.replyCount}件${t.summary.unreadCount ? ` · 未読${t.summary.unreadCount}件` : ""}`;
        scheduleThreadSync();
    }catch(error){if(T.current===t)$("threadStatus").textContent=`既読を更新できません: ${error.message}`;}
    finally{t.reading=false;}
}
async function refreshParticipatingThreads() {
    if(!S.me)return;
    try {
        const page=await api("/api/threads?participating=true&unread=true&limit=1");
        T.participatingCount=page.unreadCount;
		$("threadBadge").textContent=page.unreadCount ? String(page.unreadCount) : "";
	}catch(error){$("threadTab").title=error.message;}
}
function updateThreadScope() {
    const channel = S.channel;
    const channelButton = $("threadScopeChannel");
    channelButton.disabled = !channel;
    channelButton.textContent = channel ? `このチャンネル: #${channel.name}` : "選択中チャンネル";
    channelButton.title = channel ? `#${channel.name} の未読返信` : "チャンネルを選択してください";
    $("threadScopeAll").setAttribute("aria-pressed", String(!T.listChannel));
    channelButton.setAttribute("aria-pressed", String(!!T.listChannel));
}
async function showThreadList(channel = "", append = false) {
    if(!append){T.listChannel=channel;T.list=[];T.listCursor="";$("moreThreads").classList.add("hidden");}
    const token=++T.listToken;
    const host=$("threadList");host.classList.remove("hidden");
    $("threadScope").classList.remove("hidden");
    updateThreadScope();
    $("noticeActions").classList.add("hidden");
    for(const id of ["noticeList","mentionList","moreMentions"])$(id).classList.add("hidden");
    for(const id of ["noticeTab","mentionTab"])$(id).classList.remove("active");
    $("threadTab").classList.add("active");
    if(!append)host.textContent="返信の更新を読み込んでいます…";
    try{
        const base=channel ? `/api/channels/${encodeURIComponent(channel)}/threads?` : "/api/threads?participating=true&";
        const page=await api(`${base}unread=true&limit=50${append&&T.listCursor ? "&cursor="+encodeURIComponent(T.listCursor) : ""}`);
        if(token!==T.listToken)return;
        const items=new Map(T.list.map(t=>[threadKey(t.channelId,t.rootSeq),t]));
        for(const t of page.threads)items.set(threadKey(t.channelId,t.rootSeq),t);
        T.list=[...items.values()];T.listCursor=page.nextCursor||"";host.replaceChildren();
        const heading=document.createElement("h3");heading.className="thread-list-heading";
        heading.textContent=channel ? `# ${S.channels.find(c=>c.id===channel)?.name||channel} の返信更新` : "参加中スレッドの未読更新";host.append(heading);
        for(const t of T.list){
            const row=document.createElement("button");row.type="button";row.className="thread-list-row";
            const name=document.createElement("b"),preview=document.createElement("span"),meta=document.createElement("small");
            name.textContent=`# ${S.channels.find(c=>c.id===t.channelId)?.name||t.channelId}`;
            preview.textContent=t.root?.text||"元の発言";meta.textContent=`返信${t.replyCount}件 · 未読${t.unreadCount}件 · ${new Date(t.updatedAt).toLocaleString("ja-JP")}`;
            row.append(name,preview,meta);row.onclick=async()=>{bootstrap.Offcanvas.getOrCreateInstance($("notificationCenter")).hide();try{await openThread(t.channelId,t.rootSeq);}catch(e){note(e.message,true);}};
            host.append(row);
        }
        if(!T.list.length){const empty=document.createElement("p");empty.textContent="未読の返信はありません";host.append(empty);}
        $("moreThreads").classList.toggle("hidden",!T.listCursor);
    }catch(error){if(token===T.listToken){host.textContent=error.message;const retry=document.createElement("button");retry.className="btn btn-outline-secondary";retry.textContent="再読み込み";retry.onclick=()=>showThreadList(channel);host.append(retry);}}
}
function scheduleThreadSync(full = true) {
    T.refreshAll = T.refreshAll || full;
    if (!T.refreshTimer) T.refreshTimer=setTimeout(()=>{const all=T.refreshAll;T.refreshAll=false;T.refreshTimer=null;syncThreads(all);},250);
}
async function syncThreads(full = true) {
    if(!S.me)return;
    if(T.syncing){T.syncAgain=true;T.refreshAll=T.refreshAll||full;return;}
    T.syncing=true;
    const events=[...T.events.values()];T.events.clear();
    const t=T.current, channel=S.channel?.id;
    try {
        await Promise.all([...(full ? [loadChannels()] : []),refreshParticipatingThreads(),loadMentions()]);
        updateThreadControls();
        if(t && !S.channels.some(c=>c.id===t.channel))closeThread(false);
        if(channel && S.channel?.id===channel && S.msgs.length){
            // Cap the request even after a long history scroll; only rendered rows need summaries.
            const viewport=$("messages").getBoundingClientRect();
            const visible=[...document.querySelectorAll("#messages [data-thread-root]")].filter(el=>{const r=el.getBoundingClientRect();return r.bottom>=viewport.top&&r.top<=viewport.bottom;}).map(el=>Number(el.dataset.threadRoot));
            const roots=[...new Set([...visible,...S.msgs.slice(-450).map(m=>m.seq)])].slice(0,500);
            await api(`/api/channels/${encodeURIComponent(channel)}/thread-summaries?roots=${roots.join(",")}`);
        }
        if(t && T.current===t && !t.loading){
            const scroll=$("threadMessages"),nearEnd=scroll.scrollHeight-scroll.scrollTop-scroll.clientHeight<100;
            const summaries=await api(`/api/channels/${encodeURIComponent(t.channel)}/thread-summaries?roots=${t.root}`);
            if(T.current!==t)return;
            const summary=summaries.threads[t.root];
            if(summary){t.summary=summary;if(summary.latestSeq>(t.messages.at(-1)?.seq||0)){
                t.pending=1;
                if(nearEnd && !t.after){await loadThreadPage(t,`after=${t.messages.at(-1)?.seq||0}`,"newer");if(T.current===t)scroll.scrollTop=scroll.scrollHeight;}
                else renderThread(t);
            }}
        }
        if(!$("threadList").classList.contains("hidden") && $("notificationCenter").classList.contains("show"))await showThreadList(T.listChannel);
        for(const event of events){
            await api(`/api/channels/${encodeURIComponent(event.channelId)}/thread-summaries?roots=${event.threadRootSeq}`).catch(()=>null);
            const summary=T.summaries.get(threadKey(event.channelId,event.threadRootSeq));
            if(summary?.unreadCount) {
                const status=S.read.get(event.channelId);if(status)status.threadUpdates=true;
                const channelButton=[...document.querySelectorAll(".channel")].find(b=>b.dataset.channelId===event.channelId);
                if(channelButton&&!channelButton.querySelector(".channel-thread-update")){
                    const badge=document.createElement("span");badge.className="channel-thread-update";badge.textContent="返信";channelButton.append(badge);
                    channelButton.setAttribute("aria-label",channelButton.getAttribute("aria-label")+"、返信更新あり");
                }
            }
            if(event.userId===S.me.id && !event.includesOthers)continue;
            const mention=S.mentions.find(m=>m.channelId===event.channelId&&m.message.threadRootSeq===event.threadRootSeq&&m.message.seq>=(event.firstSeq||event.seq)&&m.message.seq<=event.seq);
            if(mention&&!mention.readAt)note("スレッドであなた宛てのメンションがあります",false,false);
            else if(summary?.participating && summary.unreadCount)note("参加中スレッドに新しい返信があります",false,false);
            const channelInfo=S.channels.find(c=>c.id===event.channelId);
            if(channelInfo&&member(channelInfo)&&typeof showOSNotification==="function"){
                const mentioned=!!mention&&!mention.readAt;
                if(mentioned||summary?.participating&&summary.unreadCount)
                    void showOSNotification({channelId:event.channelId,channelName:channelInfo.name,
                        seq:mentioned?mention.message.seq:event.lastOtherSeq||event.seq,
                        threadRootSeq:event.threadRootSeq,mention:mentioned,
                        body:mentioned?"スレッドであなた宛てのメンションがあります":"参加中スレッドに新しい返信があります"});
            }
        }
    } catch(error) { if(T.current===t&&t)$("threadStatus").textContent=`更新を取得できません: ${error.message}`; }
    finally {T.syncing=false;if(T.syncAgain){T.syncAgain=false;scheduleThreadSync(!!T.refreshAll);}}
}
function handleThreadEvent(event) {
    const key=threadKey(event.channelId,event.threadRootSeq), previous=T.events.get(key);
    T.events.set(key,{...event, firstSeq:Math.min(previous?.firstSeq||event.seq,event.seq),
        includesOthers:previous?.includesOthers||event.userId!==S.me.id,
        lastOtherSeq:event.userId!==S.me.id?event.seq:previous?.lastOtherSeq});
    scheduleThreadSync(false);
}
async function openThreadMention(mention) {
    bootstrap.Offcanvas.getOrCreateInstance($("notificationCenter")).hide();
    try{
        if(!await openThread(mention.channelId,mention.message.threadRootSeq,mention.message.seq))return;
        if(!mention.readAt){await api(`/api/mentions/${encodeURIComponent(mention.message.id)}/read`,{method:"PUT"});await loadMentions();}
    }catch(error){note(`メンションを開けません: ${error.message}`,true);}
}
$("threadClose").onclick=()=>closeThread();
$("threadPanel").addEventListener("keydown",e=>{if(e.key==="Escape"&&!e.isComposing&&e.keyCode!==229){e.stopPropagation();closeThread();}});
$("threadMessages").addEventListener("scroll",()=>scheduleThreadRead(T.current));
document.addEventListener("visibilitychange",()=>{if(document.visibilityState==="visible"){scheduleThreadRead(T.current);scheduleThreadSync();}});
$("threadOlder").onclick=()=>{const t=T.current;if(t?.before)loadThreadPage(t,`before=${t.before}`,"older");};
$("threadNewer").onclick=()=>{const t=T.current;if(t)loadThreadPage(t,`after=${t.messages.at(-1)?.seq||0}`,"newer");};
$("threadRetry").onclick=()=>{const t=T.current;if(t)loadThreadPage(t,t.retry?.query||"",t.retry?.mode||"replace",t.retry?.focusSeq);};
$("threadReadAll").onclick=async()=>{const t=T.current;if(t?.summary?.latestSeq){await writeThreadRead(t,t.summary.latestSeq);if(T.current===t)renderThread(t);}};
$("threadInput").oninput=()=>{const t=T.current;if(t)T.drafts.set(threadKey(t.channel,t.root),$("threadInput").value);updateOwnTyping();};
$("threadInput").onfocus=updateOwnTyping;
$("threadInput").onblur=()=>{if(S.typingScope?.threadRootSeq)stopTyping();};
$("threadInput").onkeydown=e=>{
    if (typeof handleThreadMentionKey === "function" && handleThreadMentionKey(e)) return;
    if((e.ctrlKey||e.metaKey)&&e.key==="Enter"&&!e.isComposing){e.preventDefault();$("threadForm").requestSubmit();return;}
    if((e.ctrlKey||e.metaKey)&&["b","u"].includes(e.key.toLowerCase())){
        e.preventDefault();applyEditorFormat(e.key.toLowerCase()==="b"?"bold":"underline",$("threadInput"),document.querySelector(".thread-tools"),syncThreadEditorState);
    }
};
$("threadForm").onsubmit=async event=>{
    event.preventDefault();const t=T.current,text=$("threadInput").value.trim();
    if(!t||t.sending||t.loading||!text)return;
    if (typeof threadMentionBlocksSubmit === "function" && threadMentionBlocksSubmit()) return;
    if(new TextEncoder().encode(text).length>MAX_MESSAGE_BYTES){note("メッセージは16 KiB以内にしてください",true);return;}
    const attachmentIds = (T.stagedAttachments || []).map((a) => a.id);
    if (attachmentIds.length && !textHasAIMention(text)) {
        note("添付ファイルはAI宛てメッセージ（@ai:...）にのみ送信できます", true);
        return;
    }
    if (S.typingScope?.channelID===t.channel && S.typingScope?.threadRootSeq===t.root) stopTyping();
    t.sending=true;updateThreadControls();
    try{
        await api(`${threadURL(t)}/messages`,{method:"POST",body:JSON.stringify({text, attachmentIds})});
        if(T.drafts.get(threadKey(t.channel,t.root))?.trim()===text)T.drafts.delete(threadKey(t.channel,t.root));
        if(T.current===t){$("threadInput").value="";T.stagedAttachments=[];renderThreadStagedAttachments();await loadThreadPage(t,`after=${t.messages.at(-1)?.seq||0}`,"newer");}
        scheduleThreadSync();
    }catch(error){if(T.current===t)$("threadStatus").textContent=`送信できません: ${error.message}`;}
    finally{t.sending=false;if(T.current===t){updateThreadControls();$("threadInput").focus();updateOwnTyping();}}
};
for(const id of ["noticeTab","mentionTab"]){$(id).addEventListener("click",()=>{++T.listToken;$("threadScope").classList.add("hidden");$("threadList").classList.add("hidden");$("moreThreads").classList.add("hidden");$("threadTab").classList.remove("active");});}
$("threadTab").onclick=()=>showThreadList();
$("threadScopeAll").onclick=()=>showThreadList();
$("threadScopeChannel").onclick=()=>{if(S.channel)showThreadList(S.channel.id);};
$("moreThreads").onclick=()=>showThreadList(T.listChannel,true);
if(S.me)refreshParticipatingThreads();

$("channelInfoToggle").addEventListener("click",()=>closeThread(false));

function updateThreadBackground() {
    syncMobileChannelNav();
}
window.matchMedia("(max-width:1050px)").addEventListener("change",updateThreadBackground);

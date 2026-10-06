const $ = (id) => document.getElementById(id);
const state = { me:null, csrf:"", channels:[], directory:new Map(), detail:null, editing:null, cursor:"", ws:null };

async function api(path, options={}) {
    const method=options.method||"GET", headers={...(options.body?{"Content-Type":"application/json"}:{}),...(state.csrf&&!['GET','HEAD'].includes(method)?{"X-CSRF-Token":state.csrf}:{})};
    let response;
    try {
        response=await fetch(path,{...options,headers});
    } catch (_) {
        throw new Error('通信に失敗しました。ネットワーク接続を確認して、もう一度お試しください');
    }
    if(response.status===204)return;
    const body=await response.json().catch(()=>({error:"サーバーからの応答を読み取れませんでした"}));
    if(response.status===401){ location.replace('/login?next='+encodeURIComponent(location.pathname+location.search)); throw new Error('セッションの有効期限が切れました'); }
    if(!response.ok)throw new Error(body.error||'操作に失敗しました');
    return body;
}
function notice(message,type='danger'){
    const toast=document.createElement('div');toast.className=`toast align-items-center text-bg-${type} border-0`;toast.innerHTML='<div class="d-flex"><div class="toast-body"></div><button class="btn-close btn-close-white me-2 m-auto" data-bs-dismiss="toast"></button></div>';toast.querySelector('.toast-body').textContent=message;$('toastContainer').append(toast);new bootstrap.Toast(toast,{delay:5000}).show();toast.addEventListener('hidden.bs.toast',()=>toast.remove());
}
const statusLabel={draft:'下書き',open:'回答受付中',closed:'締切済み',finalized:'日程確定',withdrawn:'取り消されました'};
function candidateDate(candidate) {
    const date = new Date(candidate.date + 'T00:00:00');
    return candidate.date + '（' + new Intl.DateTimeFormat('ja-JP', { weekday: 'short' }).format(date) + '）';
}
function candidateTime(candidate) { return candidate.startTime ? candidate.startTime + (candidate.endTime ? '〜' + candidate.endTime : '') : '終日'; }
function candidateLabel(candidate) { return candidateDate(candidate) + ' ' + candidateTime(candidate); }
function candidateDisplay(candidate) {
    const date = document.createElement('span'), time = document.createElement('span');
    date.className = 'candidate-date'; date.textContent = candidateDate(candidate);
    time.className = 'candidate-time'; time.textContent = candidateTime(candidate);
    return [date, time];
}
function localInput(value){if(!value)return '';const d=new Date(value),pad=n=>String(n).padStart(2,'0');return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;}
function deadlineValue(){return $('deadline').value?new Date($('deadline').value).toISOString():null;}
function setView(id){for(const view of ['listView','editorView','detailView'])$(view).classList.toggle('hidden',view!==id);}

function candidateRow(candidate={}) {
    const row = document.createElement('div');
    row.className = 'candidate-row';
    row.dataset.id = candidate.id || '';
    row.innerHTML = '<label class="candidate-date-field">日付<input class="form-control date" type="date" required aria-describedby="candidateHelp"></label><label>開始<input class="form-control start" type="time" aria-describedby="candidateHelp"></label><label>終了<input class="form-control end" type="time" aria-describedby="candidateHelp"></label><button class="btn remove" type="button" aria-label="候補を削除">削除</button>';
    row.querySelector('.date').value = candidate.date || '';
    row.querySelector('.start').value = candidate.startTime || '';
    row.querySelector('.end').value = candidate.endTime || '';
    row.querySelector('.remove').onclick = () => {
        if ($('candidates').children.length <= 2) return notice('候補は2件以上必要です', 'warning');
        row.remove();
    };
    return row;
}
function candidateValues(){return [...$('candidates').children].map(row=>({...(row.dataset.id?{id:row.dataset.id}:{}),date:row.querySelector('.date').value,startTime:row.querySelector('.start').value,endTime:row.querySelector('.end').value}));}
function addInitialCandidates(){const start=new Date(),pad=n=>String(n).padStart(2,'0');start.setDate(start.getDate()+1);for(let i=0;i<2;i++){const d=new Date(start);d.setDate(d.getDate()+i);$('candidates').append(candidateRow({date:`${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}`}));}}

let listRequest = 0;
function listPeriod() { return new URLSearchParams(location.search).get('period') === 'past' ? 'past' : 'upcoming'; }
async function showList(append=false) {
    setView('listView');
    const request = ++listRequest, period = listPeriod(), host = $('scheduleList');
    if (!append) { state.cursor = ''; host.replaceChildren(); }
    for (const button of document.querySelectorAll('[data-period]')) {
        const selected = button.dataset.period === period;
        button.classList.toggle('active', selected);
        button.setAttribute('aria-pressed', String(selected));
    }
    $('listError').classList.add('hidden');
    $('more').disabled = true;
    if (!append) $('more').classList.add('hidden');
    host.setAttribute('aria-busy', 'true');
    try {
        const page = await api('/api/schedules?limit=50&period='+period+(state.cursor?'&cursor='+encodeURIComponent(state.cursor):''));
        if (request !== listRequest) return;
        for(const item of page.schedules){const card=document.createElement('a');card.className='schedule-card';card.href='/schedules/'+encodeURIComponent(item.id);const badge=document.createElement('span');badge.className='status '+item.status;badge.textContent=statusLabel[item.status]||item.status;const title=document.createElement('h2');title.textContent=item.title;const channel=document.createElement('p');channel.textContent='# '+(state.channels.find(c=>c.id===item.channelId)?.name||item.channelId);const updated=document.createElement('p');updated.textContent='更新 '+new Date(item.updatedAt).toLocaleString('ja-JP');card.append(badge,title,channel,updated);host.append(card);}
        state.cursor = page.nextCursor || '';
        $('more').classList.toggle('hidden', !state.cursor);
        if (!host.children.length) {
            const empty = document.createElement('div');
            empty.className = 'panel';
            empty.textContent = period === 'past' ? '終了した予定はありません。' : 'これから・当日の予定はありません。';
            host.append(empty);
        }
    } catch (error) {
        if (request !== listRequest) return;
        $('listErrorText').textContent = '予定一覧を取得できませんでした。'+error.message;
        $('listError').classList.remove('hidden');
        $('retryList').onclick = () => showList(append);
    } finally {
        if (request === listRequest) { $('more').disabled = false; host.setAttribute('aria-busy', 'false'); }
    }
}
for (const button of document.querySelectorAll('[data-period]')) {
    button.onclick = () => {
        if (button.dataset.period === listPeriod()) return;
        const url = new URL(location.href);
        url.searchParams.set('period', button.dataset.period);
        history.pushState(null, '', url);
        showList();
    };
}
window.addEventListener('popstate', () => { if (location.pathname === '/schedules') showList(); });

async function showEditor(){setView('editorView');const params=new URLSearchParams(location.search),editID=params.get('edit'),postable=state.channels.filter(c=>(c.members||[]).includes(state.me.id)||(c.groups||[]).some(g=>(state.me.groups||[]).includes(g)));$('channel').replaceChildren(...postable.map(c=>{const o=document.createElement('option');o.value=c.id;o.textContent=c.name;return o;}));$('channel').value=params.get('channelId')||postable[0]?.id||'';$('candidates').replaceChildren();if(!editID&&!postable.length){$('scheduleForm').classList.add('hidden');throw new Error('予定調整を作成できる参加済みチャンネルがありません');}
    if(editID){const detail=await api('/api/schedules/'+encodeURIComponent(editID));state.editing=detail;if(detail.schedule.status==='withdrawn'){location.replace('/schedules/'+encodeURIComponent(editID));return;}if(![...$('channel').options].some(o=>o.value===detail.schedule.channelId)){const o=document.createElement('option');o.value=detail.schedule.channelId;o.textContent=state.channels.find(c=>c.id===detail.schedule.channelId)?.name||detail.schedule.channelId;$('channel').append(o);}$('editorTitle').textContent='予定調整を編集';$('title').value=detail.schedule.title;$('description').value=detail.schedule.description||'';$('channel').value=detail.schedule.channelId;$('channel').disabled=true;$('deadline').value=localInput(detail.schedule.responseDeadline);for(const c of detail.schedule.candidates)$('candidates').append(candidateRow(c));const locked=detail.responses.length>0;for(const control of $('candidates').querySelectorAll('input,button'))control.disabled=locked;$('addCandidate').disabled=locked;$('saveDraft').textContent='変更を保存';$('publish').classList.toggle('hidden',detail.schedule.status!=='draft');}
    else{state.editing=null;addInitialCandidates();$('publish').classList.remove('hidden');}
}
async function saveEditor(publishAfter=false){const input={channelId:$('channel').value,title:$('title').value,description:$('description').value,candidates:candidateValues(),responseDeadline:deadlineValue()};let schedule;if(state.editing){input.revision=state.editing.schedule.revision;schedule=await api('/api/schedules/'+encodeURIComponent(state.editing.schedule.id),{method:'PUT',body:JSON.stringify(input)});}else{schedule=await api('/api/schedules',{method:'POST',body:JSON.stringify(input)});}if(publishAfter){await api(`/api/schedules/${encodeURIComponent(schedule.id)}/publish`,{method:'POST',body:JSON.stringify({revision:schedule.revision})});}location.replace('/schedules/'+schedule.id);}
async function publish(id,revision){await api(`/api/schedules/${encodeURIComponent(id)}/publish`,{method:'POST',body:JSON.stringify({revision})});notice('予定調整を公開し、チャンネルに投稿しました','success');await showDetail(id);}

function renderDetail(){const d=state.detail,s=d.schedule;$('detailTitle').textContent=s.status==='withdrawn'?'予定調整は取り消されました':s.title;$('detailDescription').textContent=s.description||'';$('resultsPanel').classList.toggle('hidden',s.status==='withdrawn');$('status').textContent=statusLabel[s.status]||s.status;$('status').className='status '+s.status;const channel=state.channels.find(c=>c.id===s.channelId);$('meta').replaceChildren();
    for (const [label, value] of [['チャンネル', '# ' + (channel?.name || s.channelId)], ['作成', new Date(s.createdAt).toLocaleString('ja-JP')], ...(s.status==='withdrawn'?[]:[['回答期限', s.responseDeadline ? new Date(s.responseDeadline).toLocaleString('ja-JP') : 'なし']])]) {
        const item = document.createElement('div'), name = document.createElement('span'), content = document.createElement('span');
        name.className = 'meta-label'; name.textContent = label;
        content.textContent = value; item.append(name, content); $('meta').append(item);
    }
    const final=(s.candidates||[]).find(c=>c.id===s.finalCandidateId);$('finalResult').classList.toggle('hidden',!final);if(final)$('finalResult').textContent='確定日時: '+candidateLabel(final);
    const thead=$('results').tHead,tbody=$('results').tBodies[0];thead.replaceChildren();tbody.replaceChildren();const head=document.createElement('tr');const first=document.createElement('th');first.textContent='回答者';head.append(first);for(const c of (s.candidates||[])){const th=document.createElement('th');th.append(...candidateDisplay(c));head.append(th);}const comment=document.createElement('th');comment.textContent='コメント';head.append(comment);thead.append(head);const counts=document.createElement('tr');const ct=document.createElement('th');ct.textContent='集計';counts.append(ct);for(const c of (s.candidates||[])){const a=(d.aggregates||[]).find(x=>x.candidateId===c.id)||{yes:0,maybe:0,no:0},td=document.createElement('td');td.textContent=`○${a.yes} △${a.maybe} ×${a.no}`;counts.append(td);}counts.append(document.createElement('td'));tbody.append(counts);for(const response of (d.responses||[])){const tr=document.createElement('tr'),name=document.createElement('th');name.textContent=state.directory.get(response.userId)||response.userId;tr.append(name);for(const c of (s.candidates||[])){const td=document.createElement('td');td.textContent={yes:'○',maybe:'△',no:'×'}[response.choices[c.id]]||'—';tr.append(td);}const cm=document.createElement('td');cm.textContent=response.comment||'';tr.append(cm);tbody.append(tr);}
    renderResponse();renderManage();
}
function renderResponse(){const d=state.detail,s=d.schedule,host=$('responseChoices');host.replaceChildren();const mine=(d.responses||[]).find(r=>r.userId===state.me.id);for(const c of (s.candidates||[])){const row=document.createElement('div');row.className='choice-group';const label=document.createElement('strong');label.append(...candidateDisplay(c));row.setAttribute('role','group');row.setAttribute('aria-label',candidateLabel(c));const buttons=document.createElement('div');buttons.className='choice-buttons';for(const [value,text] of [['yes','○'],['maybe','△'],['no','×']]){const wrap=document.createElement('label'),radio=document.createElement('input'),span=document.createElement('span');radio.type='radio';radio.name='choice-'+c.id;radio.value=value;radio.checked=mine?.choices[c.id]===value;radio.disabled=!d.acceptingResponses;span.textContent=text;const meaning=document.createElement('small');meaning.textContent={yes:'参加できる',maybe:'未定',no:'参加できない'}[value];span.append(meaning);wrap.append(radio,span);buttons.append(wrap);}row.append(label,buttons);host.append(row);}$('comment').value=mine?.comment||'';$('comment').disabled=!d.acceptingResponses;$('responseForm').querySelector('button').disabled=!d.acceptingResponses;$('responseForm').classList.toggle('hidden',s.status==='draft'||s.status==='withdrawn');}
function renderManage(){const d=state.detail,s=d.schedule;$('manage').classList.toggle('hidden',!d.canManage&&!d.canWithdraw&&!d.canRestore);$('restore').classList.toggle('hidden',!d.canRestore);$('withdraw').classList.toggle('hidden',!d.canWithdraw||s.status==='draft'||s.status==='withdrawn');$('edit').classList.toggle('hidden',!d.canManage);if(!d.canManage){$('publishDetail').classList.add('hidden');$('close').classList.add('hidden');$('reopen').classList.add('hidden');$('finalizeBox').classList.add('hidden');return;}$('edit').disabled=s.status==='finalized';$('publishDetail').classList.toggle('hidden',!(s.status==='draft'||(s.status==='open'&&!s.announcementSeq)));$('publishDetail').textContent=s.status==='draft'?'チャンネルに公開':'公開メッセージを再投稿';$('close').classList.toggle('hidden',s.status!=='open');$('reopen').classList.toggle('hidden',s.status!=='closed');$('finalizeBox').classList.toggle('hidden',!['open','closed'].includes(s.status)&&!(s.status==='finalized'&&!s.finalAnnouncementSeq));$('finalize').textContent=s.status==='finalized'?'確定メッセージを再投稿':'この日程で確定して投稿';$('finalCandidate').replaceChildren(...s.candidates.map(c=>{const o=document.createElement('option');o.value=c.id;o.textContent=candidateLabel(c);o.selected=c.id===s.finalCandidateId;return o;}));$('finalCandidate').disabled=s.status==='finalized';}
async function showDetail(id){setView('detailView');state.detail=await api('/api/schedules/'+encodeURIComponent(id));renderDetail();connectRealtime(id);}
function connectRealtime(id){if(state.ws)return;const ws=new WebSocket(`${location.protocol==='https:'?'wss':'ws'}://${location.host}/api/realtime`);state.ws=ws;ws.onmessage=e=>{try{const event=JSON.parse(e.data);if(event.type==='schedule_changed'&&event.scheduleId===id)showDetail(id).catch(()=>{});}catch{}};ws.onclose=()=>{if(state.ws===ws){state.ws=null;setTimeout(()=>connectRealtime(id),2000);}};}
async function action(name,extra={}){const s=state.detail.schedule;await api(`/api/schedules/${encodeURIComponent(s.id)}/${name}`,{method:'POST',body:JSON.stringify({revision:s.revision,...extra})});await showDetail(s.id);}

$('addCandidate').onclick=()=>{if($('candidates').children.length>=50)return notice('候補は50件までです','warning');$('candidates').append(candidateRow());};
$('scheduleForm').onsubmit=e=>{e.preventDefault();saveEditor(false).catch(e=>notice(e.message));};
$('publish').onclick=()=>saveEditor(true).catch(e=>notice(e.message));
$('more').onclick=()=>showList(true).catch(e=>notice(e.message));
$('responseForm').onsubmit=e=>{e.preventDefault();const s=state.detail.schedule,choices={};for(const c of (s.candidates||[])){const selected=document.querySelector(`input[name="choice-${c.id}"]:checked`);if(!selected)return notice('すべての候補に回答してください','warning');choices[c.id]=selected.value;}api(`/api/schedules/${encodeURIComponent(s.id)}/responses/me`,{method:'PUT',body:JSON.stringify({revision:s.revision,choices,comment:$('comment').value})}).then(()=>showDetail(s.id)).catch(e=>notice(e.message));};
$('restore').onclick=()=>{if(confirm('取り消しを戻し、元の内容を再表示しますか？'))action('restore').catch(e=>notice(e.message));};$('withdraw').onclick=()=>{if(confirm('予定調整を取り消しますか？ 回答の受付を終了し、元の内容は保存されます。'))action('withdraw').catch(e=>notice(e.message));};$('edit').onclick=()=>location.href='/schedules/new?edit='+encodeURIComponent(state.detail.schedule.id);$('publishDetail').onclick=()=>publish(state.detail.schedule.id,state.detail.schedule.revision).catch(e=>notice(e.message));$('close').onclick=()=>action('close').catch(e=>notice(e.message));$('reopen').onclick=()=>action('reopen',{responseDeadline:null}).catch(e=>notice(e.message));$('finalize').onclick=()=>{if(confirm('選択した日程で確定し、チャンネルへ投稿しますか？'))action('finalize',{candidateId:$('finalCandidate').value}).catch(e=>notice(e.message));};

(async()=>{state.me=await api('/api/me');state.csrf=state.me.csrfToken;$('me').textContent=state.me.name;[state.channels]=await Promise.all([api('/api/channels')]);const users=await api('/api/users/directory');state.directory=new Map(users.map(u=>[u.id,u.name]));const path=location.pathname;if(path==='/schedules')await showList();else if(path==='/schedules/new')await showEditor();else await showDetail(decodeURIComponent(path.slice('/schedules/'.length)));})().catch(error=>{$('alert').textContent=error.message;$('alert').className='alert alert-danger';});

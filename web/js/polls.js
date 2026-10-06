(() => {
const createModal=bootstrap.Modal.getOrCreateInstance($("pollModal"));
const optionHost=$("pollOptions");
let loadToken=0,createRequestID="",sidebarChannelID="",expandedPollID="";
function closeCreateModal(){const id=createRequestID;createModal.hide();setTimeout(()=>{if(createRequestID===id)createModal.hide()},800)}
function optionInput(value=""){
  const row=document.createElement("div");row.className="poll-option-edit input-group mt-2";
  const input=document.createElement("input");input.className="form-control";input.maxLength=100;input.required=true;input.placeholder=`選択肢 ${optionHost.children.length+1}`;input.value=value;
  const remove=document.createElement("button");remove.type="button";remove.className="btn btn-outline-secondary";remove.textContent="削除";remove.onclick=()=>{if(optionHost.children.length>2)row.remove();};row.append(input,remove);optionHost.append(row);
}
function canCreate(){return !!S.channel&&member(S.channel)}
function openCreate(){if(!canCreate())return;createRequestID=[...crypto.getRandomValues(new Uint8Array(16))].map(x=>x.toString(16).padStart(2,"0")).join("");$("pollForm").reset();optionHost.replaceChildren();optionInput();optionInput();$("pollCreateError").textContent="";createModal.show();$("pollQuestion").focus();}
$("createPoll").onclick=openCreate;
$("createSchedule").onclick=()=>{if(canCreate())window.open("/schedules/new?channelId="+encodeURIComponent(S.channel.id),"_blank","noopener,noreferrer")};
$("addPollOption").onclick=()=>{if(optionHost.children.length<10)optionInput()};
$("pollForm").onsubmit=async event=>{
  event.preventDefault();if(!canCreate())return;
  const button=$("pollPublish");button.disabled=true;$("pollCreateError").textContent="";
  try{const deadline=$("pollDeadline").value;await api("/api/polls",{method:"POST",body:JSON.stringify({requestId:createRequestID,channelId:S.channel.id,question:$("pollQuestion").value,description:$("pollDescription").value,options:[...optionHost.querySelectorAll("input")].map(x=>x.value),multiple:$("pollMultiple").checked,deadline:deadline?new Date(deadline).toISOString():null})});closeCreateModal();note("アンケートを公開しました");refreshSidebar();}
  catch(e){$("pollCreateError").textContent=e.message;}finally{button.disabled=false;}
};
function text(tag,content,className=""){const node=document.createElement(tag);node.textContent=content;if(className)node.className=className;return node}
function pollOpen(d){return d.acceptingResponses}
// A loaded poll can be much taller than its placeholder in the channel history.
function updateChannelCard(host,update){
  const messages=host.closest("#messages");
  const keepBottom=messages&&(S.read.get(S.channel?.id)?.unreadCount||0)===0&&messages.scrollHeight-messages.scrollTop-messages.clientHeight<=24;
  update();
  if(keepBottom)messages.scrollTop=messages.scrollHeight;
}
async function renderCard(host,id){
  if(!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(id))return;
  host.dataset.pollId=id;const token=String(Number(host.dataset.pollLoad||0)+1);host.dataset.pollLoad=token;
  const previous=host.querySelector(".poll-vote-form");const checked=previous?[...previous.querySelectorAll("input:checked")].map(x=>x.value):[];
  const dirty=previous&&JSON.stringify(checked)!==previous.dataset.saved;const focus=host.contains(document.activeElement)?document.activeElement?.value||"submit":"";
  if(!host.querySelector(".poll-card"))host.replaceChildren(text("div","アンケートを読み込み中…","muted"));
  try{const d=await api("/api/polls/"+encodeURIComponent(id));if(!host.isConnected||host.dataset.pollLoad!==token)return;updateChannelCard(host,()=>drawCard(host,d));if(dirty&&d.acceptingResponses){for(const input of host.querySelectorAll(".poll-vote-form input"))input.checked=checked.includes(input.value)}if(focus){const target=focus==="submit"?host.querySelector(".poll-vote-form button"):[...host.querySelectorAll(".poll-vote-form input")].find(input=>input.value===focus);target?.focus()}}
  catch(e){if(host.isConnected&&host.dataset.pollLoad===token)updateChannelCard(host,()=>host.replaceChildren(text("div",e.message,"text-danger")))}
}
function drawCard(host,d){
  clearTimeout(host.pollDeadlineTimer);const p=d.poll;if(d.acceptingResponses&&p.deadline){host.pollDeadlineTimer=setTimeout(()=>{if(host.isConnected)renderCard(host,p.id)},Math.min(Math.max(Date.parse(p.deadline)-Date.now()+100,100),2147483647))}host.replaceChildren();const card=document.createElement("section");card.className="poll-card";card.setAttribute("aria-label","アンケート: "+p.question);
  if(p.status==="withdrawn"){
    card.classList.add("poll-withdrawn");
    card.append(text("div","アンケートは取り消されました","poll-withdrawn-label"));
    if(d.canRestore){const button=text("button","取り消しを戻す","btn btn-outline-secondary btn-sm");button.type="button";button.onclick=async()=>{if(!confirm("取り消しを戻し、アンケートを再表示しますか？"))return;button.disabled=true;try{drawCard(host,await api("/api/polls/"+encodeURIComponent(p.id)+"/restore",{method:"POST"}));refreshSidebar()}catch(e){button.disabled=false;note(e.message,true)}};card.append(button)}
    host.append(card);return;
  }
  const head=document.createElement("div");head.className="poll-card-head";head.append(text("strong",p.question),text("span",pollOpen(d)?"受付中":"終了","poll-status"));card.append(head);
  if(p.description)card.append(text("p",p.description,"poll-description"));
  const form=document.createElement("form");form.className="poll-vote-form";
  const selected=new Set(d.myResponse?.optionIds||[]);form.dataset.saved=JSON.stringify(p.options.filter(o=>selected.has(o.id)).map(o=>o.id));for(const o of p.options){const label=document.createElement("label");label.className="poll-choice";const input=document.createElement("input");input.type=p.multiple?"checkbox":"radio";input.name="pollChoice";input.value=o.id;input.checked=selected.has(o.id);input.disabled=!d.canVote||!pollOpen(d);label.append(input,text("span",o.text));form.append(label)}
  const submit=document.createElement("button");submit.className="btn btn-success btn-sm mt-2";submit.type="submit";submit.textContent=d.myResponse?"回答を変更":"回答する";submit.disabled=!d.canVote||!pollOpen(d);if(d.canVote&&pollOpen(d))form.append(submit);
  const error=text("div","","text-danger small");form.append(error);form.onsubmit=async e=>{e.preventDefault();const ids=[...form.querySelectorAll("input:checked")].map(x=>x.value);submit.disabled=true;error.textContent="";try{const next=await api("/api/polls/"+encodeURIComponent(p.id)+"/vote",{method:"PUT",body:JSON.stringify({revision:d.myResponse?.seq||0,optionIds:ids})});drawCard(host,next)}catch(err){error.textContent=err.message;submit.disabled=false;if(err.message.includes("更新"))renderCard(host,p.id)}};card.append(form);
  const summary=text("div",`回答者 ${d.respondents}人${p.multiple?` · 総選択数 ${d.totalVotes}票`:""}`,"poll-summary");card.append(summary);
  const result=document.createElement("div");result.className="poll-results";for(const [i,o] of p.options.entries()){const c=d.counts[i];const row=document.createElement("div");row.className="poll-result";const line=document.createElement("div");line.className="poll-result-line";line.append(text("span",o.text),text("span",`${c.votes}票 · ${c.percent}%`));const bar=document.createElement("div");bar.className="poll-bar";const fill=document.createElement("div");fill.className="poll-bar-fill";fill.style.width=Math.max(0,Math.min(100,c.percent))+"%";bar.append(fill);row.append(line,bar);result.append(row)}card.append(result);
  if(p.multiple)card.append(text("div","複数選択のため割合の合計は100%を超えることがあります。","muted small"));
  if(p.deadline)card.append(text("div","締切: "+new Date(p.deadline).toLocaleString("ja-JP"),"muted small mt-2"));
  const actions=document.createElement("div");actions.className="poll-manage-actions";
  if(d.canManage&&pollOpen(d)){const close=text("button","アンケートを締め切る","btn btn-outline-secondary btn-sm");close.type="button";close.onclick=async()=>{close.disabled=true;try{const next=await api("/api/polls/"+encodeURIComponent(p.id)+"/close",{method:"POST",body:JSON.stringify({revision:p.revision})});drawCard(host,next)}catch(e){error.textContent=e.message;close.disabled=false}};actions.append(close)}
  if(d.canWithdraw){const button=text("button","アンケートを取り消す","btn btn-outline-danger btn-sm");button.type="button";button.onclick=async()=>{if(!confirm("アンケートを取り消しますか？ 回答の受付を終了し、元の内容は保存されます。"))return;button.disabled=true;try{drawCard(host,await api("/api/polls/"+encodeURIComponent(p.id)+"/withdraw",{method:"POST"}));refreshSidebar()}catch(e){error.textContent=e.message;button.disabled=false}};actions.append(button)}
  if(actions.childElementCount)card.append(actions);
  host.append(card);
}
window.pollRenderCard=renderCard;
async function refreshSidebar(){
  const ch=S.channel,host=$("activePollSchedules");
  $("createMenuButton").disabled=!canCreate();
  if(sidebarChannelID!==ch?.id){sidebarChannelID=ch?.id||"";expandedPollID="";host.textContent="";}
  const token=++loadToken;
  if(!ch){host.textContent="チャンネルを選択してください";return;}
  if(!host.children.length)host.textContent="読み込み中…";
  try{
    const [polls,schedules]=await Promise.all([api("/api/polls?active=true&channelId="+encodeURIComponent(ch.id)),api("/api/schedules?period=upcoming&limit=50")]);
    if(token!==loadToken||S.channel?.id!==ch.id)return;
    const items=[...polls.filter(d=>d.acceptingResponses).map(d=>({kind:"poll",deadline:d.poll.deadline,title:d.poll.question,data:d})),...(schedules.schedules||[]).filter(s=>s.channelId===ch.id&&s.status==="open").map(s=>({kind:"schedule",deadline:s.responseDeadline,title:s.title,data:s}))];
    items.sort((a,b)=>(a.deadline?Date.parse(a.deadline):Infinity)-(b.deadline?Date.parse(b.deadline):Infinity));
    const existingCard=expandedPollID?host.querySelector(`.poll-side-entry[data-poll-id="${expandedPollID}"] .poll-card-host`):null;
    const rows=document.createDocumentFragment();
    let expandedCard=null;
    for(const item of items.slice(0,5)){
      if(item.kind==="schedule"){
        const button=text("button","予定 · "+item.title,"poll-side-item");
        button.type="button";
        button.onclick=()=>window.open("/schedules/"+encodeURIComponent(item.data.id),"_blank","noopener,noreferrer");
        rows.append(button);
        continue;
      }
      const id=item.data.poll.id;
      const entry=document.createElement("div");entry.className="poll-side-entry";entry.dataset.pollId=id;
      const button=text("button",`アンケート · ${item.title}${item.data.myResponse?" · 回答済":" · 未回答"}`,"poll-side-item");
      button.type="button";button.setAttribute("aria-controls","active-poll-"+id);
      const open=expandedPollID===id;
      button.setAttribute("aria-expanded",String(open));
      const card=open&&existingCard?existingCard:document.createElement("div");
      card.id="active-poll-"+id;card.className="poll-card-host poll-side-card";
      card.classList.toggle("hidden",!open);
      button.onclick=()=>{
        const next=button.getAttribute("aria-expanded")!=="true";
        for(const other of host.querySelectorAll(".poll-side-entry")){
          other.querySelector(".poll-side-item").setAttribute("aria-expanded","false");
          other.querySelector(".poll-card-host").classList.add("hidden");
        }
        expandedPollID=next?id:"";
        button.setAttribute("aria-expanded",String(next));
        card.classList.toggle("hidden",!next);
        if(next)renderCard(card,id);
      };
      entry.append(button,card);rows.append(entry);
      if(open)expandedCard=card;
    }
    if(!items.length)rows.append(text("span","受付中のアンケート・予定はありません"));
    host.replaceChildren(rows);
    if(!expandedCard)expandedPollID="";
    else renderCard(expandedCard,expandedPollID);
  }catch(e){if(token===loadToken&&S.channel?.id===ch.id)host.textContent=e.message;}
}
window.pollChannelSelected=refreshSidebar;
document.addEventListener("poll-changed",e=>{for(const host of document.querySelectorAll(`.poll-card-host[data-poll-id="${e.detail.pollId}"]`))renderCard(host,e.detail.pollId);refreshSidebar()});
document.addEventListener("poll-reconnect",()=>{refreshSidebar();for(const host of document.querySelectorAll(".poll-card-host[data-poll-id]"))renderCard(host,host.dataset.pollId)});
document.addEventListener("visibilitychange",()=>{if(document.visibilityState==="visible"){refreshSidebar();for(const host of document.querySelectorAll(".poll-card-host[data-poll-id]"))renderCard(host,host.dataset.pollId)}});
refreshSidebar();setInterval(()=>{if(S.channel)refreshSidebar()},60000);
})();

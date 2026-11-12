"use strict";
const oneBotDrafts = new Map();
function resetOneBotSettings() { oneBotDrafts.clear(); const f=document.getElementById("onebot-config-form"); if(f)f.reset();document.querySelectorAll(".onebot-endpoints").forEach(n=>n.replaceChildren()); }
function oneBotDraft() {
  if (!app.sessionID) return null;
  if (!oneBotDrafts.has(app.sessionID)) oneBotDrafts.set(app.sessionID,{config:null,dirty:false,busy:false,error:"",endpoints:[]});
  return oneBotDrafts.get(app.sessionID);
}
async function loadOneBotSettings(force=false) {
  const sid=app.sessionID, epoch=app.epoch, gen=app.authGeneration, state=oneBotDraft();
  if(!app.authenticated||!state||state.busy||(!force&&state.config))return;
  state.busy=true;renderOneBotSettings();
  try {
    const value=await controlApi("onebot",{session:sid});
    if(!app.authenticated||gen!==app.authGeneration||epoch!==app.epoch||sid!==app.sessionID)return;
    state.endpoints=value.endpoints||[];
    if(!state.dirty) state.config=value.config;
    state.error="";
  }catch(error){if(app.authenticated&&gen===app.authGeneration)state.error=error.message;}
  finally{state.busy=false;if(gen===app.authGeneration&&sid===app.sessionID)renderOneBotSettings();}
}
function oneBotInput(label,value,type,onChange,placeholder="") {
  const wrap=document.createElement("label");wrap.className="field-label";wrap.append(document.createTextNode(label));
  const input=document.createElement("input");input.type=type;input.value=value??"";input.placeholder=placeholder;if(type==="password")input.autocomplete="new-password";
  input.addEventListener("input",()=>onChange(input.value));wrap.append(input);return wrap;
}
function renderOneBotSettings() {
  const form=$("onebot-config-form");if(!form)return;const state=oneBotDraft();
  $("onebot-config-account").textContent=app.sessionID?`当前账号 ${currentSession()?.account||"待登录"} · 会话 ${app.sessionID}`:"请先选择账号";
  const error=$("onebot-config-error");error.textContent=state?.error||"";error.hidden=!state?.error;
  const config=state?.config;form.hidden=!config;$("onebot-refresh").disabled=!state||state.busy;
  if(!config)return;
  $("onebot-message-format").value=config.message_format;$("onebot-heartbeat").value=config.heartbeat_interval_ms;$("onebot-rate").value=config.rate_limit_interval_ms;
  $("onebot-save").disabled=state.busy||!state.dirty;$("onebot-discard").disabled=state.busy||!state.dirty;
  $("onebot-dirty-state").textContent=state.busy?"正在同步…":state.dirty?"当前账号有未应用的更改":"当前账号配置已同步";
  for(const [kind,container] of [["http_post","onebot-post-list"],["reverse_ws","onebot-reverse-list"]]) {
    const root=$(container);root.replaceChildren();const list=config[kind]||[];
    if(!list.length){const p=document.createElement("p");p.className="field-hint";p.textContent="未配置，当前不主动连接外部服务。";root.append(p);continue;}
    list.forEach((item,index)=>{
      const row=document.createElement("div");row.className="onebot-endpoint";
      const dirty=(key,v)=>{item[key]=v;state.dirty=true;$("onebot-save").disabled=false;$("onebot-discard").disabled=false;$("onebot-dirty-state").textContent="当前账号有未应用的更改";};
      row.append(oneBotInput(kind==="http_post"?"上报 URL":"连接 URL",item.url,"url",v=>dirty("url",v),kind==="http_post"?"https://example.com/onebot":"ws://127.0.0.1:6700/onebot/v11/ws"));
      if(kind==="http_post") {
        row.append(oneBotInput("签名 Secret",item.secret,"password",v=>dirty("secret",v),item.secret_configured?"已设置，留空沿用":"可选，HMAC-SHA1"));
        row.append(oneBotInput("超时（毫秒）",item.timeout_ms,"number",v=>dirty("timeout_ms",Number(v))));
      } else {
        row.append(oneBotInput("Access Token",item.token,"password",v=>dirty("token",v),item.token_configured?"已设置，留空沿用":"可选，Bearer 鉴权"));
        const label=document.createElement("label");label.className="field-label";label.textContent="连接类型";const select=document.createElement("select");for(const role of ["Universal","API","Event"]){const option=document.createElement("option");option.value=role;option.textContent={Universal:"Universal · API + 事件",API:"API · 仅调用",Event:"Event · 仅事件"}[role];select.append(option);}select.value=item.role;select.addEventListener("change",()=>dirty("role",select.value));label.append(select);row.append(label);
        row.append(oneBotInput("重连间隔（毫秒）",item.reconnect_interval_ms,"number",v=>dirty("reconnect_interval_ms",Number(v))));
      }
      const foot=document.createElement("div");foot.className="onebot-endpoint-foot";const status=state.endpoints.find(v=>v.kind===kind&&v.index===index);const caption=document.createElement("span");caption.textContent=state.dirty?"配置尚未应用":status?`${status.connected?"已连接 / 最近送达正常":"等待连接 / 尚未送达"} · 成功 ${status.sent} · 失败 ${status.failures}${status.error?" · "+status.error:""}`:"等待首次连接";foot.append(caption);
      const remove=document.createElement("button");remove.type="button";remove.className="button ghost compact";remove.textContent="移除";remove.disabled=state.busy;remove.addEventListener("click",()=>{list.splice(index,1);state.dirty=true;renderOneBotSettings();});foot.append(remove);row.append(foot);root.append(row);
    });
  }
  for(const el of form.querySelectorAll("input,select,button")){if(el.id!=="onebot-save"&&el.id!=="onebot-discard")el.disabled=state.busy;}
}
function bindOneBotSettings() {
  window.setInterval(()=>{const d=oneBotDraft();if(app.authenticated&&app.booted&&app.view==="settings"&&d&&!d.dirty&&!document.getElementById("onebot-config-form").contains(document.activeElement))loadOneBotSettings(true);},12000);
  $("onebot-refresh").addEventListener("click",()=>loadOneBotSettings(true));
  $("onebot-discard").addEventListener("click",()=>{const d=oneBotDraft();if(d){d.dirty=false;d.config=null;loadOneBotSettings(true);}});
  for(const [id,key,initial] of [["onebot-add-post","http_post",{url:"",secret:"",timeout_ms:5000}],["onebot-add-reverse","reverse_ws",{url:"",token:"",role:"Universal",reconnect_interval_ms:3000}]]) {
    $(id).addEventListener("click",()=>{const d=oneBotDraft();if(!d?.config||d.busy)return;const list=d.config[key]||(d.config[key]=[]);if(list.length>=8){showToast("每类最多 8 个地址","error");return;}list.push({...initial});d.dirty=true;renderOneBotSettings();});
  }
  for(const [id,key] of [["onebot-message-format","message_format"],["onebot-heartbeat","heartbeat_interval_ms"],["onebot-rate","rate_limit_interval_ms"]]) {
    $(id).addEventListener("change",()=>{const d=oneBotDraft();if(!d?.config)return;d.config[key]=key==="message_format"?$(id).value:Number($(id).value);d.dirty=true;renderOneBotSettings();});
  }
  $("onebot-config-form").addEventListener("submit",async event=>{
    event.preventDefault();const d=oneBotDraft(),sid=app.sessionID,epoch=app.epoch,gen=app.authGeneration;if(!d?.config||d.busy||!d.dirty)return;
    const config=d.config,body={message_format:config.message_format,heartbeat_interval_ms:config.heartbeat_interval_ms,rate_limit_interval_ms:config.rate_limit_interval_ms,http_post:(config.http_post||[]).map(p=>({url:p.url,secret:p.secret||"",timeout_ms:p.timeout_ms})),reverse_ws:(config.reverse_ws||[]).map(p=>({url:p.url,token:p.token||"",role:p.role,reconnect_interval_ms:p.reconnect_interval_ms}))};
    d.busy=true;d.error="";renderOneBotSettings();
    try{const value=await controlApi("onebot",{method:"POST",body,session:sid});if(gen!==app.authGeneration)return;d.config=value.config;d.endpoints=value.endpoints||[];d.dirty=false;if(sid===app.sessionID&&epoch===app.epoch)showToast("当前账号 OneBot 配置已应用","success");}
    catch(error){if(gen===app.authGeneration)d.error=error.message;}
    finally{d.busy=false;if(gen===app.authGeneration&&sid===app.sessionID)renderOneBotSettings();}
  });
}

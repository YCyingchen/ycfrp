"use strict";
const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
let META=null, STATUS=null, HOST=null, SERIES=[], TUNNELS=[], CHANNELS=[];
let logSrc=null, evtSrc=null, logBuf=[], analyzing=false;

/* ---------------- 基础工具 ---------------- */
async function api(path,opt={}){
  const o=Object.assign({credentials:'same-origin',headers:{}},opt);
  if(o.body&&typeof o.body!=='string'){o.headers['Content-Type']='application/json';o.body=JSON.stringify(o.body);}
  const res=await fetch(path,o);
  const ct=res.headers.get('content-type')||'';
  if(!ct.includes('application/json')){
    if(res.status===401){showLogin();throw new Error('请先登录');}
    return res;
  }
  const data=await res.json();
  if(!res.ok||data.ok===false){
    if(res.status===401)showLogin();
    throw new Error(data.error||('请求失败 '+res.status));
  }
  return data.data!==undefined?data.data:data;
}
function toast(msg,kind){
  const d=document.createElement('div');
  d.textContent=msg;
  if(kind==='bad')d.className='bad'; else if(kind==='ok')d.className='ok';
  $('#toast').appendChild(d);
  setTimeout(()=>{d.style.opacity='0';d.style.transform='translateX(20px)';
    d.style.transition='.25s';setTimeout(()=>d.remove(),260);},3200);
}
function bytes(n){
  n=Number(n)||0;
  if(n<1024)return n+' B';
  const u=['KB','MB','GB','TB','PB'];let i=-1;
  do{n/=1024;i++;}while(n>=1024&&i<u.length-1);
  return n.toFixed(n<10?2:1)+' '+u[i];
}
function rate(n){return bytes(n)+'/s';}
function dur(sec){
  sec=Math.max(0,Math.floor(sec||0));
  const d=Math.floor(sec/86400),h=Math.floor(sec%86400/3600),m=Math.floor(sec%3600/60),s=sec%60;
  if(d>0)return d+'天 '+h+'小时';
  if(h>0)return h+'小时 '+m+'分';
  if(m>0)return m+'分 '+s+'秒';
  return s+'秒';
}
function esc(s){return String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));}
function cssv(name){return getComputedStyle(document.documentElement).getPropertyValue(name).trim();}
// 请求/响应头在界面上按「每行 K:V」编辑，这里与对象互转。
function headersToText(h){
  if(!h)return '';
  return Object.keys(h).map(k=>k+': '+h[k]).join('\n');
}
function parseHeaders(text){
  const out={};
  (text||'').split('\n').forEach(line=>{
    const i=line.indexOf(':');
    if(i<=0)return;
    const k=line.slice(0,i).trim();
    if(!k)return;
    out[k]=line.slice(i+1).trim();
  });
  return out;
}
function phaseTag(st,err){
  const map={online:['运行中','ok'],offline:['已离线','idle'],running:['运行中','ok'],
    'start error':['启动失败','bad'],'check failed':['检测失败','bad'],'wait start':['等待中','warn'],
    waiting:['等待中','warn'],new:['新建','idle'],closed:['已停止','idle'],'not running':['未启动','idle']};
  const [txt,cls]=map[st]||[st||'未知','idle'];
  return `<span class="tag ${cls}" title="${esc(err||'')}">${esc(txt)}</span>`;
}
function typeTag(t){return `<span class="tag idle">${esc((t||'').toUpperCase())}</span>`;}

// instTag 显示隧道归属的客户端实例名。隧道按实例区分，多实例下不看这一列
// 就分不清同名隧道分别跑在哪个客户端上。
function instTag(id){
  if(!id)return '<span class="hint">未归属</span>';
  const i=INSTANCES.find(x=>x.id===id);
  return `<span class="tag idle">${esc(i?i.name:id)}</span>`;
}

// UPTIME_BASE 是推算出的服务端启动时刻（毫秒时间戳）。设为 0 表示尚未取得。
let UPTIME_BASE=0;
// tickUptime 按本地时钟刷新「已运行」时长。每秒执行，显示随秒递增。
function tickUptime(){
  if(!UPTIME_BASE)return;
  const sec=Math.max(0,Math.floor((Date.now()-UPTIME_BASE)/1000));
  $('#uptimeTag').textContent='已运行 '+dur(sec);
}
setInterval(tickUptime,1000);

/* ---------------- 登录 ---------------- */
async function boot(){
  try{
    const st=await api('/api/auth/status');
    if(st.loggedIn){ await start(); } else { showLogin(); }
  }catch(e){ showLogin(); }
  try{
    const m=await api('/api/meta');
    META=m;
    $('#siteName').textContent=m.ui.siteName||m.product;
    $('#siteDesc').textContent=m.description||'';
    $('#verTag').textContent=m.version+' · 内核 '+m.kernelVersion;
    $('#loginTitle').textContent=(m.ui.siteName||m.product)+' 管理面板';
    const fm=$('#footMeta');
    if(fm)fm.textContent=m.version+' · '+m.goVersion+' · '+m.mode;
    // 建立运行时间基准，首屏就显示正确的时长。
    if(m.startedAt){
      const t=Date.parse(m.startedAt);
      if(!isNaN(t))UPTIME_BASE=t;
    }
    applyUI(m.ui,false);
    $('#aboutBox').innerHTML=
      `产品：${esc(m.product)}<br>面板版本：${esc(m.version)}<br>frp 内核：${esc(m.kernelVersion)}<br>`+
      `运行模式：${esc(m.mode)}<br>运行环境：${esc(m.goVersion)}<br>数据目录：${esc(m.ui&&m.ui.dataDir||'')}`;
  }catch(e){}
}
function showLogin(){
  $('#login').classList.remove('hidden');
  $('#app').style.display='none';
  stopStreams();
}
async function start(){
  $('#login').classList.add('hidden');
  $('#app').style.display='';
  await loadAll();
  startStreams();
}
$('#btnLogin').onclick=async()=>{
  $('#loginErr').textContent='';
  const b=$('#btnLogin');b.disabled=true;b.textContent='登录中...';
  try{
    await api('/api/auth/login',{method:'POST',body:{
      username:$('#l_user').value.trim(),password:$('#l_pass').value,remember:$('#l_remember').checked}});
    await boot();
  }catch(e){ $('#loginErr').textContent=e.message; }
  b.disabled=false;b.textContent='登录面板';
};
$('#l_pass').addEventListener('keydown',e=>{if(e.key==='Enter')$('#btnLogin').click();});
$('#l_user').addEventListener('keydown',e=>{if(e.key==='Enter')$('#l_pass').focus();});
$('#btnLogout').onclick=async()=>{
  await api('/api/auth/logout',{method:'POST'}).catch(()=>{});
  location.reload();
};

/* ---------------- 导航 ---------------- */
const PAGE_META={
  dash:['总览','服务端与客户端的实时状态'],
  instances:['实例管理','服务端与客户端各管各的隧道，互不影响'],
  traffic:['流量监控','网卡速率、累计流量与月度配额'],
  logs:['运行日志','内核日志已中文化，可按来源与级别筛选'],
  settings:['设置','通知、界面、更新与账号']
};
function setPageTitle(page){
  const m=PAGE_META[page]||PAGE_META.dash;
  const t=$('#pageTitle'),s=$('#pageSub');
  if(t)t.textContent=m[0];
  if(s)s.textContent=m[1];
}
$('#nav').addEventListener('click',e=>{
  const a=e.target.closest('a[data-page]');if(!a)return;
  $$('#nav a').forEach(x=>x.classList.toggle('on',x===a));
  const page=a.dataset.page;
  setPageTitle(page);
  $$('section[data-view]').forEach(s=>s.classList.toggle('hidden',s.dataset.view!==page));
  if(page==='logs')renderLogs();
  if(page==='traffic')drawTraffic();
  if(page==='dash')drawRate();
  if(page==='instances')loadInstances();
  // 设置页默认落在上次看过的子页（记忆在本地），进入「更新」子页时刷新检查。
  if(page==='settings'){
    const last=localStorage.getItem('ycfrp_settings_tab')||'notify';
    showSettingsTab(last);
  }
});

/* ---------------- 设置页子标签 ---------------- */
function showSettingsTab(tab){
  const btn=document.querySelector('#setTabs [data-set="'+tab+'"]');
  if(!btn)tab='notify';
  $$('#setTabs [data-set]').forEach(b=>b.classList.toggle('on',b.dataset.set===tab));
  $$('[data-setpage]').forEach(p=>p.classList.toggle('hidden',p.dataset.setpage!==tab));
  localStorage.setItem('ycfrp_settings_tab',tab);
  if(tab==='update')loadUpdateInfo();
}
$('#setTabs').addEventListener('click',e=>{
  const b=e.target.closest('[data-set]');if(!b)return;
  showSettingsTab(b.dataset.set);
});

/* ---------------- 总览卡片拖拽布局 ---------------- */
// 布局存 localStorage：顺序即卡片在网格里的排列。
// KPI 概览条固定在最上（信息密度高、宽度占满，不适合参与拖拽），
// 其余卡片（速率/主机/隧道等）都可以按住标题栏拖动换位。
const DASH_LAYOUT_KEY='ycfrp_dash_layout';

function initDashDrag(){
  const board=$('#dashBoard');if(!board)return;
  // 恢复已保存的顺序
  try{
    const saved=JSON.parse(localStorage.getItem(DASH_LAYOUT_KEY)||'[]');
    if(Array.isArray(saved)&&saved.length){
      const cards={};
      board.querySelectorAll('.dashcard[data-card]').forEach(c=>cards[c.dataset.card]=c);
      saved.forEach(id=>{
        if(cards[id]){board.appendChild(cards[id]);delete cards[id];}
      });
      // 新版本可能新增了卡片，补到末尾
      Object.values(cards).forEach(c=>board.appendChild(c));
    }
  }catch(e){}

  // 判断是否在默认位置（显示/隐藏「恢复默认布局」按钮）
  const resetBtn=$('#btnResetLayout');
  const checkCustom=()=>{
    if(!resetBtn)return;
    const saved=localStorage.getItem(DASH_LAYOUT_KEY);
    resetBtn.classList.toggle('hidden',!saved);
  };
  checkCustom();
  if(resetBtn)resetBtn.onclick=()=>{
    localStorage.removeItem(DASH_LAYOUT_KEY);
    toast('已恢复默认布局，刷新页面生效','ok');
    setTimeout(()=>location.reload(),600);
  };

  let dragEl=null;
  board.querySelectorAll('.dashcard').forEach(card=>{
    card.draggable=false;
    const handle=card.querySelector('.drag-handle');
    if(!handle)return;
    // 只有按住标题栏才启用拖拽，避免影响卡片内表格的文本选择。
    handle.addEventListener('mousedown',()=>{card.draggable=true;});
    handle.addEventListener('touchstart',()=>{card.draggable=true;},{passive:true});
    card.addEventListener('dragstart',e=>{
      dragEl=card;card.style.opacity='.45';
      e.dataTransfer.effectAllowed='move';
      try{e.dataTransfer.setData('text/plain',card.dataset.card);}catch(_){/* IE 需要 */}
    });
    card.addEventListener('dragend',()=>{
      card.style.opacity='';card.draggable=false;
      dragEl=null;
      // 保存当前顺序
      const order=[...board.querySelectorAll('.dashcard[data-card]')].map(c=>c.dataset.card);
      localStorage.setItem(DASH_LAYOUT_KEY,JSON.stringify(order));
      checkCustom();
      // 图表容器尺寸可能变了，重画一次。
      if(typeof redrawActiveChart==='function')redrawActiveChart();
    });
    card.addEventListener('dragover',e=>{
      e.preventDefault();
      if(!dragEl||dragEl===card)return;
      const rect=card.getBoundingClientRect();
      // 网格布局：按鼠标相对中线的位置决定插到前还是后。
      const before=(e.clientX<rect.left+rect.width/2)||(e.clientY<rect.top+rect.height/2&&Math.abs(e.clientY-rect.top)<24);
      board.insertBefore(dragEl,before?card:card.nextSibling);
    });
  });
}
initDashDrag();

/* ---------------- 数据加载 ---------------- */
async function loadAll(){
  // 实例要先于配置加载：服务端/客户端两页的编辑目标是默认实例。
  await loadInstances();
  await Promise.all([loadOverview(),loadConfig(),loadTunnels()]);
  loadAutostart();
}
async function loadOverview(){
  try{
    const d=await api('/api/overview');
    STATUS=d; HOST=d.host;
    renderOverview();
  }catch(e){toast('加载总览失败：'+e.message,'bad');}
}
function renderOverview(){
  if(!STATUS)return;
  const d=STATUS, s=d.stats||{};
  $('#kServer').textContent=d.serverRunning?'运行中':'已停止';
  $('#kServer').style.color=d.serverRunning?'var(--ok)':'var(--bad)';
  const si=d.server||{};
  $('#kServerSub').textContent=d.serverRunning
    ? `绑定端口 ${si.bindPort||'--'} · 连接 ${si.curConns||0}`
    : '服务端未启动';
  $('#kTunnel').textContent=(s.tunnelOnline||0)+' / '+(s.tunnelTotal||0);
  $('#kTunnelSub').textContent=s.tunnelError?`${s.tunnelError} 条异常`:'全部正常';
  $('#kClient').textContent=s.clientCount||0;
  const tin=si.totalTrafficIn||0,tout=si.totalTrafficOut||0;
  $('#kTraffic').textContent=bytes(tin+tout);
  $('#kTrafficSub').textContent=`下行 ${bytes(tin)} · 上行 ${bytes(tout)}`;
  // 优先用 meta 里的精确启动时刻作为基准；overview 的 uptime 是整秒，
  // 只能作为拿不到 startedAt 时的退路，否则会出现几秒的跳变。
  if(!UPTIME_BASE){
    if(META&&META.startedAt){
      const t=Date.parse(META.startedAt);
      if(!isNaN(t))UPTIME_BASE=t;
    }
    if(!UPTIME_BASE){
      const up=d.uptime||0;
      if(up>0)UPTIME_BASE=Date.now()-up*1000;
    }
  }
  tickUptime();
  $('#rxNow').textContent=rate(HOST?HOST.rxRate:0);
  $('#txNow').textContent=rate(HOST?HOST.txRate:0);
  renderHost();
  renderDashTunnels();
  renderClients();
  drawRate();
}
function renderHost(){
  if(!HOST)return;
  const set=(bar,txt,v,pct)=>{
    $(bar).style.width=Math.min(100,pct||0)+'%';
    $(bar).className=(pct>90?'bad':pct>70?'warn':'');
    $(txt).textContent=(v||'-')+(pct!=null?` · ${pct.toFixed(1)}%`:'');
  };
  set('#cpuBar','#cpuTxt',(HOST.cpuPercent||0).toFixed(1)+' %',HOST.cpuPercent);
  set('#memBar','#memTxt',bytes(HOST.memUsed)+' / '+bytes(HOST.memTotal),HOST.memPercent);
  set('#diskBar','#diskTxt',bytes(HOST.diskUsed)+' / '+bytes(HOST.diskTotal),HOST.diskPercent);
  $('#hostInfo').textContent=`主机 ${HOST.hostname||'-'} · 系统 ${HOST.platform||'-'} · 架构 ${HOST.arch||'-'} · 已开机 ${dur(HOST.uptime)}`;
  $('#tRx').textContent=rate(HOST.rxRate);
  $('#tTx').textContent=rate(HOST.txRate);
  $('#tRxTotal').textContent=bytes(HOST.totalRx);
  $('#tTxTotal').textContent=bytes(HOST.totalTx);
  renderIfaces();
}
function renderIfaces(){
  const rows=HOST&&HOST.interfaces||[];
  $('#ifaceRows').innerHTML=rows.map(i=>`<tr>
    <td class="mono">${esc(i.name)}</td>
    <td class="mono" style="color:var(--data-rx)">${rate(i.rxRate)}</td>
    <td class="mono" style="color:var(--data-tx)">${rate(i.txRate)}</td>
    <td class="mono">${bytes(i.rxTotal)}</td>
    <td class="mono">${bytes(i.txTotal)}</td>
    <td>${i.isDefault?'<span class="tag ok">主用</span>':'<span class="tag idle">备用</span>'}</td>
  </tr>`).join('')||'<tr><td colspan="6" class="empty">未采集到网卡数据</td></tr>';
}
function renderDashTunnels(){
  const rows=(STATUS&&STATUS.tunnels)||[];
  $('#dashTunnels').innerHTML=rows.slice(0,12).map(t=>{
    const acc=tunnelAccessURL(t);
    const accCell=acc
      ? `<a href="${esc(acc)}" target="_blank" rel="noopener" style="text-decoration:none;padding:4px 10px;font-size:12px;border-radius:8px;background:var(--accent);color:#fff;display:inline-block">访问</a>`
      : '<span class="tag idle">—</span>';
    return `<tr>
    <td>${esc(t.name)}</td><td>${typeTag(t.type)}</td>
    <td class="mono">${t.localPort?`${esc(t.localIP||'127.0.0.1')}:${t.localPort}`:'--'}</td>
    <td class="mono">${esc(remoteOf(t)||'-')}</td>
    <td>${accCell}</td>
    <td>${phaseTag(t.status,t.err)}</td>
    <td class="mono">${t.curConns||0}</td>
    <td>${esc(tunnelClientName(t))}</td>
    <td class="mono">${esc(tunnelServerAddr(t))}</td>
    <td class="mono">${bytes(t.todayIn)} / ${bytes(t.todayOut)}</td>
  </tr>`;}).join('')||'<tr><td colspan="10" class="empty">暂无隧道</td></tr>';
}
function remoteOf(t){
  if(t.type==='http'||t.type==='https'||t.type==='tcpmux')
    return (t.customDomains&&t.customDomains[0])||t.subdomain||'';
  return t.remoteAddr||(t.remotePort?(':'+t.remotePort):'');
}
function renderClients(){
  const rows=(STATUS&&STATUS.clients)||[];
  $('#clientRows').innerHTML=rows.map(c=>`<tr>
    <td>${esc(c.hostname||'-')}</td><td>${esc(c.user||'-')}</td>
    <td class="mono">${esc(c.version||'-')}</td><td class="mono">${esc(c.ip||'-')}</td>
    <td class="mono">${esc(c.firstConnectedAt||'-')}</td>
    <td>${c.online?'<span class="tag ok">在线</span>':'<span class="tag idle">离线</span>'}</td>
  </tr>`).join('');
  $('#clientEmpty').classList.toggle('hidden',rows.length>0);
}
async function loadTunnels(){
  try{
    const d=await api('/api/tunnels');
    TUNNELS=d.tunnels||[];
    renderInstances();
  }catch(e){toast('加载隧道失败：'+e.message,'bad');}
}

/* ---------------- 配置 ---------------- */
async function loadConfig(){
  try{
    const c=await api('/api/config');

    fill('#n_enable',c.notify.enable);fill('#n_err',c.notify.monitorErrors);
    fill('#n_off',c.notify.monitorOffline);fill('#n_traffic',c.notify.monitorTraffic);
    fill('#n_keywords',(c.notify.keywords||[]).join(','));
    fill('#n_quiet',c.notify.quietMinutes);
    fill('#q_enable',c.notify.qq.enable);fill('#q_proto',c.notify.qq.protocol);
    fill('#q_host',c.notify.qq.host);fill('#q_port',c.notify.qq.port);
    // 密钥类字段不回显明文：已配置时显示占位状态，留空提交即保持原值。
    fill('#q_admin',c.notify.qq.adminAdmin||c.notify.qq.adminQQ);
    fill('#q_groups',(c.notify.qq.groupIds||[]).join(','));
    markSecret('#q_token', c.notify.qq&&c.notify.qq.hasAccessToken);

    const qo=c.notify.qqOfficial||{};
    fill('#qo_enable',qo.enable);fill('#qo_appid',qo.appId);
    fill('#qo_groups',qo.groupOpenIds);fill('#qo_user',qo.userOpenId);
    fill('#qo_msgid',qo.msgId);fill('#qo_sandbox',qo.sandbox);
    fill('#qo_proxy',qo.proxy);fill('#qo_listen',qo.listen);
    markSecret('#qo_secret', qo.hasAppSecret);

    const wc=c.notify.wecom||{};
    fill('#wc_enable',wc.enable);
    fill('#wc_mention',wc.mentionList);fill('#wc_all',wc.mentionAll);
    markSecret('#wc_key', wc.hasKey);

    const dt=c.notify.dingtalk||{};
    fill('#dt_enable',dt.enable);fill('#dt_hook',dt.webhook);
    fill('#dt_mobiles',dt.atMobiles);fill('#dt_all',dt.atAll);
    markSecret('#dt_secret', dt.hasSecret);

    const fs=c.notify.feishu||{};
    fill('#fs_enable',fs.enable);fill('#fs_hook',fs.webhook);
    markSecret('#fs_secret', fs.hasSecret);

    const sc=c.notify.serverChan||{};
    fill('#sc_enable',sc.enable);fill('#sc_channel',sc.channel);
    markSecret('#sc_key', sc.hasSendKey);

    const bk=c.notify.bark||{};
    fill('#bk_enable',bk.enable);fill('#bk_server',bk.server);fill('#bk_group',bk.group);
    markSecret('#bk_key', bk.hasDeviceKey);

    const tg=c.notify.telegram||{};
    fill('#tg_enable',tg.enable);fill('#tg_chat',tg.chatId);fill('#tg_proxy',tg.proxy);
    markSecret('#tg_token', tg.hasBotToken);

    qqOfficialHint();
    loadQQBotStatus();
    qqBindRestore();
    CHANNELS=c.notify.channels||[];
    renderChannels();

    fill('#u_siteName',c.ui.siteName);fill('#u_accent',c.ui.accentColor);
    fill('#u_accentText',c.ui.accentColor);fill('#u_footer',c.ui.siteFooter);
    fill('#u_theme',c.ui.theme);
    applyUI(c.ui,true);
  }catch(e){toast('加载配置失败：'+e.message,'bad');}
}
function fill(sel,v){
  const el=$(sel);if(!el)return;
  if(el.type==='checkbox')el.checked=!!v; else el.value=v==null?'':v;
}
// markSecret 处理密钥输入框的「已配置」状态。
//
// 密钥不回显明文，因此输入框留空。为让用户知道已经配好了，用 placeholder
// 提示；提交时留空表示保持原值（后端 setSecret 也只接受非空值）。
function markSecret(sel,configured){
  const el=$(sel);if(!el)return;
  if(configured){
    el.value='';
    el.placeholder='已配置（留空保持不变）';
  }else{
    el.value='';
    el.placeholder=el.dataset.ph||'';
  }
}
function num(sel){const v=$(sel).value.trim();return v===''?undefined:Number(v);}
function str(sel){return $(sel).value;}

async function pingTo(host,port){
  toast(`正在测试 ${host}:${port} ...`);
  try{
    const r=await api(`/api/tools/ping?host=${encodeURIComponent(host)}&port=${port}`);
    toast(r.ok?`连接成功，耗时 ${r.costMs} 毫秒`:'连接失败：'+r.error,r.ok?'ok':'bad');
  }catch(e){toast(e.message,'bad');}
}
$('#btnRestart').onclick=async()=>{
  if(!confirm('确定重启 frp 内核吗？现有隧道会短暂断开。'))return;
  try{ await api('/api/kernel',{method:'POST',body:{action:'restart'}});
    toast('内核已重启','ok'); await loadOverview();
  }catch(e){toast(e.message,'bad');}
};
$('#btnResetTraffic').onclick=async()=>{
  if(!confirm('确定重置流量统计吗？'))return;
  try{ await api('/api/kernel',{method:'POST',body:{action:'reset-traffic'}});
    toast('流量统计已重置','ok');
  }catch(e){toast(e.message,'bad');}
};

/* ---------------- 隧道弹窗 ---------------- */
function modal(title,html){
  $('#modalBody').innerHTML=`<h3>${esc(title)}</h3>${html}`;
  $('#modal').classList.remove('hidden');
}
function closeModal(){$('#modal').classList.add('hidden');}
$('#modal').addEventListener('click',e=>{if(e.target.id==='modal')closeModal();});

const TYPES=[['tcp','TCP 端口转发'],['udp','UDP 端口转发'],['http','HTTP 域名转发'],
  ['https','HTTPS 域名转发'],['tcpmux','TCPMux'],['stcp','STCP 加密隧道'],
  ['xtcp','XTCP 点对点'],['sudp','SUDP']];

// instOptions 渲染客户端实例下拉框。没有客户端实例时给出明确指引，
// 否则用户会卡在「创建隧道却不知道为何失败」。
function instOptions(selected){
  const clients=INSTANCES.filter(i=>i.kind==='client');
  if(!clients.length)return '<option value="">（请先创建客户端实例）</option>';
  return clients.map(i=>{
    const label=`${i.name}（连 ${(i.client||{}).serverAddr||'--'}:${(i.client||{}).serverPort||'--'}）`;
    const on=selected?i.id===selected:false;
    return `<option value="${esc(i.id)}" ${on?'selected':''}>${esc(label)}</option>`;
  }).join('');
}
// clientInstance 按标识取出客户端实例，找不到返回 undefined。
function clientInstance(id){
  return INSTANCES.find(i=>i.kind==='client'&&i.id===id);
}
// clientServerLabel 根据客户端实例拼接「客户端名 → 服务端地址」的说明文字。
function clientServerLabel(instId){
  const ci=clientInstance(instId);
  if(!ci)return '（请先选择所属客户端实例）';
  const c=ci.client||{};
  return `${ci.name} → ${c.serverAddr||'--'}:${c.serverPort||'--'}`;
}
// tunnelClientName / tunnelServerAddr 供隧道列表展示「客户端 / 服务端」两列。
function tunnelClientName(t){
  const ci=clientInstance(t.instanceId);
  return ci?ci.name:'--';
}
function tunnelServerAddr(t){
  const ci=clientInstance(t.instanceId);
  if(!ci)return '--';
  const c=ci.client||{};
  return `${c.serverAddr||'--'}:${c.serverPort||'--'}`;
}
// tunnelAccessURL 拼出隧道对外可访问的地址：HTTP/HTTPS 用域名，端口型用「公网IP:远程端口」。
// 公网地址取自承载客户端实例填写的服务端地址（frpc 连的就是 frps 的公网地址）。
function tunnelAccessURL(t){
  if(!t)return '';
  if(t.type==='http'||t.type==='https'){
    const d=(t.customDomains&&t.customDomains[0])||t.subdomain||'';
    if(d)return (t.type==='https'?'https://':'http://')+d;
    return '';
  }
  const ci=clientInstance(t.instanceId);
  const host=(ci&&ci.client&&ci.client.serverAddr)||'';
  const port=t.remotePort||'';
  if(!host||!port)return '';
  return 'http://'+host+':'+port;
}

function tunnelModal(t){
  // 新增隧道时调用方只传 {instanceId:xxx}，没有 id；编辑时才带完整隧道对象。
  // 因此 isEdit 必须以「是否有 id」为准，不能用 !!t（新增的 {instanceId} 也是真值）。
  const isEdit=!!(t&&t.id);
  t=t||{type:'tcp',localIP:'127.0.0.1',enabled:true,healthCheckInterval:10,healthCheckTimeout:3,healthCheckMaxFailed:1};
  // 新建隧道时，从承载客户端实例取默认加密/压缩值预填。
  const defCli = (!isEdit && t.instanceId) ? clientInstance(t.instanceId) : null;
  const defEnc = isEdit ? (t.useEncryption?true:false) : !!(defCli && defCli.client && defCli.client.useEncryption);
  const defComp = isEdit ? (t.useCompression?true:false) : !!(defCli && defCli.client && defCli.client.useCompression);
  const srvLabel = clientServerLabel(t.instanceId);
  modal(isEdit?'编辑隧道':'新增隧道',`
    <div class="fgroup">基础设置</div>
    <div class="frow two">
      <div><label>隧道名称</label><input id="t_name" value="${esc(t.name||'')}" placeholder="例如 web-1"></div>
      <div><label>所属客户端实例</label><select id="t_inst">${instOptions(t.instanceId)}</select></div>
    </div>
    <div class="frow"><label>连接的服务端</label>
      <div class="hint" id="t_serverInfo" style="padding:8px 10px;background:var(--control);border:1px solid var(--line);border-radius:8px">${srvLabel}</div>
    </div>
    <div class="frow two">
      <div><label>隧道类型</label><select id="t_type">${TYPES.map(([v,l])=>
        `<option value="${v}" ${t.type===v?'selected':''}>${l}</option>`).join('')}</select></div>
      <div></div>
    </div>
    <div class="frow two">
      <div><label>本地地址</label><input id="t_lip" value="${esc(t.localIP||'127.0.0.1')}"></div>
      <div><label>本地端口</label><input id="t_lport" type="number" value="${t.localPort||''}" placeholder="例如 8080"></div>
    </div>
    <div class="frow" id="rowRemote"><label>远程端口</label>
      <input id="t_rport" type="number" value="${t.remotePort||''}" placeholder="例如 28080">
      <div class="hint">需落在服务端允许的端口区间内</div></div>
    <div class="frow hidden" id="rowDomain">
      <label>自定义域名（多个用逗号分隔）</label>
      <input id="t_domains" value="${esc((t.customDomains||[]).join(','))}" placeholder="例如 web.example.com">
      <div class="hint" style="margin-top:8px">子域名（可选，配合服务端子域名后缀）</div>
      <input id="t_subdomain" value="${esc(t.subdomain||'')}" placeholder="例如 web">
    </div>
    <div class="frow hidden" id="rowSecret">
      <label>加密密钥 secretKey</label><input id="t_secret" value="${esc(t.secretKey||'')}">
      <div class="hint" style="margin-top:8px">允许访问的用户（多个用逗号分隔，留空不限制）</div>
      <input id="t_allow" value="${esc(t.group||'')}">
    </div>
    <div class="frow two">
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="t_enc" ${defEnc?'checked':''}>
          <span class="track"></span><span>加密传输</span></label>
      </div>
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="t_comp" ${defComp?'checked':''}>
          <span class="track"></span><span>压缩传输</span></label>
      </div>
    </div>
    <div class="frow two">
      <div><label>带宽限制（留空不限制）</label><input id="t_bw" value="${esc(t.bandwidthLimit||'')}" placeholder="例如 1MB"></div>
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="t_enabled" ${t.enabled?'checked':''}>
          <span class="track"></span><span>启用该隧道</span></label>
      </div>
    </div>

    <div class="fgroup">高级设置</div>
    <div class="frow two">
      <div><label>代理协议版本</label><select id="t_ppv">
        <option value="">自动</option><option value="v1" ${t.proxyProtocolVersion==='v1'?'selected':''}>v1</option>
        <option value="v2" ${t.proxyProtocolVersion==='v2'?'selected':''}>v2</option></select></div>
      <div></div>
    </div>
    <div class="frow three">
      <div><label>健康检查</label><select id="t_hc">
        <option value="">不启用</option><option value="tcp" ${t.healthCheckType==='tcp'?'selected':''}>TCP</option>
        <option value="http" ${t.healthCheckType==='http'?'selected':''}>HTTP</option></select></div>
      <div><label>检查地址 / 路径</label><input id="t_hcurl" value="${esc(t.healthCheckUrl||'')}" placeholder="/health"></div>
      <div><label>检查间隔（秒）</label><input id="t_hcint" type="number" value="${t.healthCheckInterval||10}"></div>
    </div>
    <div class="frow two">
      <div><label>负载均衡分组（可选）</label>
        <input id="t_lbgroup" value="${esc(t.loadBalanceGroup||'')}" placeholder="例如 web-cluster"></div>
      <div><label>负载均衡组标识</label>
        <input id="t_lbkey" value="${esc(t.loadBalanceKey||'')}" placeholder="同组需一致，留空不启用">
        <div class="hint">同组内名称相同的多条隧道由服务端轮询分发</div></div>
    </div>

    <div id="rowHTTP" class="hidden">
      <div class="frow"><label>路径路由（多个用逗号分隔，留空匹配全部）</label>
        <input id="t_locations" value="${esc((t.locations||[]).join(','))}" placeholder="例如 /api,/static">
        <div class="hint">仅 HTTP 类型；按路径前缀把请求分给不同隧道</div></div>
      <div class="frow two">
        <div><label>BasicAuth 用户名（可选）</label><input id="t_httpuser" value="${esc(t.httpUser||'')}"></div>
        <div><label>BasicAuth 密码</label><input id="t_httppwd" type="password" value="${esc(t.httpPassword||'')}"></div>
      </div>
      <div class="frow two">
        <div><label>Host 头改写（可选）</label>
          <input id="t_hostrewrite" value="${esc(t.hostHeaderRewrite||'')}" placeholder="例如 web.internal">
          <div class="hint">转发时替换 Host，便于后端按域名分流</div></div>
        <div><label>按用户路由（可选）</label>
          <input id="t_routeuser" value="${esc(t.routeByHTTPUser||'')}" placeholder="HTTP 用户名"></div>
      </div>
      <div class="frow two">
        <div><label>请求头改写（每行 K:V）</label>
          <textarea id="t_reqhdrs" style="min-height:76px" placeholder="X-Real-IP: 1.2.3.4">${esc(headersToText(t.requestHeaders))}</textarea></div>
        <div><label>响应头改写（每行 K:V）</label>
          <textarea id="t_resphdrs" style="min-height:76px" placeholder="Cache-Control: no-store">${esc(headersToText(t.responseHeaders))}</textarea></div>
      </div>
    </div>

    <div class="frow"><label>备注</label><input id="t_note" value="${esc(t.note||'')}"></div>
    <div class="row" style="margin-top:16px;justify-content:flex-end">
      <button id="t_cancel">取消</button>
      <button class="primary" id="t_save">${isEdit?'保存修改':'创建隧道'}</button>
    </div>`);
  const syncRows=()=>{
    const ty=$('#t_type').value;
    const domain=ty==='http'||ty==='https'||ty==='tcpmux';
    const secret=ty==='stcp'||ty==='xtcp'||ty==='sudp';
    $('#rowRemote').classList.toggle('hidden',domain||secret);
    $('#rowDomain').classList.toggle('hidden',!domain);
    $('#rowSecret').classList.toggle('hidden',!secret);
    $('#rowHTTP').classList.toggle('hidden',ty!=='http');
  };
  // 切换客户端实例时，实时刷新「连接的服务端」提示。
  $('#t_inst').onchange=()=>{
    const info=$('#t_serverInfo');
    if(info)info.textContent=clientServerLabel($('#t_inst').value);
  };
  $('#t_type').onchange=syncRows; syncRows();
  $('#t_cancel').onclick=closeModal;
  $('#t_save').onclick=async()=>{
    const domains=str('#t_domains').split(',').map(s=>s.trim()).filter(Boolean);
    const payload={
      name:str('#t_name'),type:str('#t_type'),localIP:str('#t_lip')||'127.0.0.1',
      instanceId:str('#t_inst'),
      localPort:num('#t_lport')||0,remotePort:num('#t_rport')||0,
      customDomains:domains,subdomain:str('#t_subdomain'),secretKey:str('#t_secret'),
      group:str('#t_allow'),bandwidthLimit:str('#t_bw'),
      proxyProtocolVersion:str('#t_ppv'),healthCheckType:str('#t_hc'),
      healthCheckUrl:str('#t_hcurl'),healthCheckInterval:num('#t_hcint')||0,
      healthCheckTimeout:t.healthCheckTimeout||3,healthCheckMaxFailed:t.healthCheckMaxFailed||1,
      loadBalanceGroup:str('#t_lbgroup').trim(),loadBalanceKey:str('#t_lbkey').trim(),
      locations:str('#t_locations').split(',').map(s=>s.trim()).filter(Boolean),
      httpUser:str('#t_httpuser').trim(),httpPassword:str('#t_httppwd'),
      hostHeaderRewrite:str('#t_hostrewrite').trim(),routeByHTTPUser:str('#t_routeuser').trim(),
      requestHeaders:parseHeaders(str('#t_reqhdrs')),responseHeaders:parseHeaders(str('#t_resphdrs')),
      useEncryption:$('#t_enc').checked,useCompression:$('#t_comp').checked,
      note:str('#t_note'),enabled:$('#t_enabled').checked,id:t.id||''
    };
    try{
      if(isEdit) await api('/api/tunnels/'+encodeURIComponent(t.id),{method:'PUT',body:payload});
      else await api('/api/tunnels',{method:'POST',body:payload});
      closeModal();toast(isEdit?'隧道已更新':'隧道已创建','ok');
      await Promise.all([loadTunnels(),loadOverview()]);
    }catch(e){toast(e.message,'bad');}
  };
}

/* ---------------- 导入 / 导出 ---------------- */
$('#btnImport').onclick=()=>{
  modal('粘贴 frpc 配置导入',`
    <div class="hint" style="margin-bottom:10px">
      支持 frp v1 的 TOML / YAML / JSON 配置，也兼容旧版 INI（[common] 开头）格式。
      导入会为这份配置新建一个独立的客户端实例（服务端地址与 token 取自配置），
      与其它实例互不影响。
    </div>
    <div class="frow"><textarea id="im_text" style="min-height:250px" placeholder="serverAddr = &quot;1.2.3.4&quot;
serverPort = 7000
auth.token = &quot;xxx&quot;

[[proxies]]
name = &quot;web&quot;
type = &quot;tcp&quot;
localIP = &quot;127.0.0.1&quot;
localPort = 8080
remotePort = 28080"></textarea></div>
    <div class="frow"><label class="switch"><input type="checkbox" id="im_over">
      <span class="track"></span><span>覆盖同名隧道</span></label></div>
    <div class="row" style="margin-top:14px;justify-content:flex-end">
      <button id="im_cancel">取消</button>
      <button class="primary" id="im_ok">解析并导入</button>
    </div>
    <div id="im_result" class="hint" style="margin-top:12px"></div>`);
  $('#im_cancel').onclick=closeModal;
  $('#im_ok').onclick=async()=>{
    try{
      const r=await api('/api/tunnels/import',{method:'POST',body:{
        content:str('#im_text'),overwrite:$('#im_over').checked}});
      let html=`<div style="color:var(--ok)">导入完成：新增 ${r.added} 条，跳过 ${r.skipped} 条（格式：${esc(r.format)}）</div>`;
      if(r.warnings&&r.warnings.length)
        html+='<div style="margin-top:8px">'+r.warnings.map(w=>'· '+esc(w)).join('<br>')+'</div>';
      $('#im_result').innerHTML=html;
      toast(`导入完成，新增 ${r.added} 条隧道`,'ok');
      await Promise.all([loadInstances(),loadTunnels(),loadOverview(),loadConfig()]);
    }catch(e){$('#im_result').innerHTML=`<span style="color:var(--bad-txt)">${esc(e.message)}</span>`;}
  };
};
$('#btnExport').onclick=()=>{
  modal('导出 frpc 配置',`
    <div class="hint" style="margin-bottom:10px">
      导出的配置文件可直接配合官方 frpc 二进制使用：frpc -c frpc.toml
    </div>
    <div class="frow two">
      <div><label>格式</label><select id="ex_fmt">
        <option value="toml">TOML（推荐）</option><option value="json">JSON</option></select></div>
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="ex_only"><span class="track"></span>
        <span>仅导出已启用隧道</span></label></div>
    </div>
    <div class="row" style="margin-top:12px">
      <button class="primary" id="ex_view">生成预览</button>
      <button id="ex_dl">下载文件</button>
    </div>
    <div class="frow" style="margin-top:12px"><textarea id="ex_text" style="min-height:230px" readonly></textarea></div>`);
  const build=async()=>{
    const q=`?format=${str('#ex_fmt')}&enabledOnly=${$('#ex_only').checked?1:0}`;
    const r=await api('/api/tunnels/export'+q);
    $('#ex_text').value=r.content;
    return q;
  };
  $('#ex_view').onclick=()=>build().catch(e=>toast(e.message,'bad'));
  $('#ex_dl').onclick=async()=>{
    try{
      const q=await build();
      const res=await fetch('/api/tunnels/export'+q+'&download=1',{credentials:'same-origin'});
      const blob=await res.blob();
      const a=document.createElement('a');
      a.href=URL.createObjectURL(blob);
      a.download=str('#ex_fmt')==='json'?'frpc.json':'frpc.toml';
      a.click();URL.revokeObjectURL(a.href);
    }catch(e){toast(e.message,'bad');}
  };
  build().catch(()=>{});
};

/* ---------------- 实例管理 ---------------- */
let INSTANCES=[];

async function loadInstances(){
  try{
    const d=await api('/api/instances');
    INSTANCES=d.instances||[];
    renderInstances();
  }catch(e){toast('加载实例失败：'+e.message,'bad');}
}

function renderInstances(){
  const servers=INSTANCES.filter(i=>i.kind==='server');
  const clients=INSTANCES.filter(i=>i.kind==='client');
  $('#instSummary').textContent=`共 ${INSTANCES.length} 个实例：服务端 ${servers.length} 个，客户端 ${clients.length} 个`;
  $('#instEmpty').classList.toggle('hidden',INSTANCES.length>0);
  $('#instList').innerHTML=INSTANCES.map(i=>instCard(i)).join('');
}

// instCard 渲染单个实例卡片：名称、运行状态、关键参数与操作按钮。
function instCard(i){
  const isServer=i.kind==='server';
  const st=i.running
    ? '<span class="tag ok">运行中</span>'
    : (i.enabled?'<span class="tag bad">已停止</span>':'<span class="tag idle">已停用</span>');
  const bad=i.err?`<div class="hint" style="color:var(--bad-txt);margin-top:6px">${esc(i.err)}</div>`:'';

  // 关键参数按类型分别展示，避免把无关字段铺满卡片。
  let params=[];
  if(isServer){
    const s=i.server||{};
    params.push(['监听',`${s.bindAddr||'0.0.0.0'}:${s.bindPort||'--'}`]);
    if(s.dashboardPort)params.push(['仪表盘',`端口 ${s.dashboardPort}`]);
    if(s.vhostHttpPort||s.vhostHttpsPort)
      params.push(['虚拟主机',`HTTP ${s.vhostHttpPort||'--'} / HTTPS ${s.vhostHttpsPort||'--'}`]);
    if(s.subdomainHost)params.push(['子域名后缀',s.subdomainHost]);
    params.push(['令牌',s.hasToken?'已配置':'未配置']);
  }else{
    const c=i.client||{};
    params.push(['连接',`${c.serverAddr||'--'}:${c.serverPort||'--'}`]);
    params.push(['协议',(c.transportProtocol||'tcp')+(c.transportTLS?' + TLS':'')]);
    if(c.user)params.push(['用户名',c.user]);
    params.push(['令牌',c.hasToken?'已配置':'未配置']);
    params.push(['隧道',`${i.tunnelCount||0} 条`]);
  }

  const detail=params.map(([k,v])=>
    `<div style="display:flex;gap:8px;font-size:12.5px;padding:2px 0">
       <span class="hint" style="min-width:74px">${esc(k)}</span>
       <span class="mono" style="word-break:break-all">${esc(v)}</span>
     </div>`).join('');

  const ports=(i.ports||[]).length
    ? `<div class="hint" style="margin-top:6px">占用端口：${i.ports.join('、')}</div>`:'';

  // 客户端实例卡片内嵌其名下隧道列表，隧道管理合并进实例页。
  const tunnels=isServer?[]:TUNNELS.filter(t=>t.instanceId===i.id);
  const tunnelBlock=isServer?'':
    `<div style="margin-top:12px;border-top:1px solid var(--line);padding-top:10px">
       <div class="spread" style="margin-bottom:6px">
         <span class="hint">名下隧道（${tunnels.length}）</span>
         <button class="sm primary" data-inst-addtunnel="${esc(i.id)}">＋ 新增隧道</button>
       </div>
       ${tunnels.length?`<div style="overflow:auto;-webkit-overflow-scrolling:touch"><table><thead><tr>
         <th>名称</th><th>类型</th><th>本地</th><th>远程</th><th>访问</th><th>状态</th><th>连接</th><th>客户端</th><th>服务端</th><th>启用</th><th></th>
       </tr></thead><tbody>${tunnels.map(t=>instTunnelRow(t)).join('')}</tbody></table></div>`
       :'<div class="empty" style="padding:10px">暂无隧道</div>'}
     </div>`;

  return `<div class="card" style="margin-bottom:12px">
    <div class="spread" style="align-items:flex-start">
      <div style="min-width:0">
        <div class="row" style="gap:8px;flex-wrap:wrap">
          <b style="font-size:14.5px">${esc(i.name)}</b>
          <span class="tag idle">${esc(i.kindLabel||(isServer?'服务端':'客户端'))}</span>
          ${st}
        </div>
        ${i.note?`<div class="hint" style="margin-top:4px">${esc(i.note)}</div>`:''}
        <div style="margin-top:9px">${detail}</div>
        ${ports}
        ${bad}
      </div>
      <div class="row" style="gap:6px;flex:0 0 auto">
        <label class="switch" title="启用/停用该实例">
          <input type="checkbox" data-inst-toggle="${esc(i.id)}" ${i.enabled?'checked':''}>
          <span class="track"></span>
        </label>
        <button class="sm" data-inst-edit="${esc(i.id)}">编辑</button>
        <button class="sm danger" data-inst-del="${esc(i.id)}">删除</button>
      </div>
    </div>
    ${tunnelBlock}
  </div>`;
}

// instTunnelRow 渲染实例卡片里的隧道行（精简版，操作同隧道管理页）。
function instTunnelRow(t){
  const acc=tunnelAccessURL(t);
  const accCell=acc
    ? `<a href="${esc(acc)}" target="_blank" rel="noopener" class="sm" style="text-decoration:none;padding:4px 10px;font-size:12px;border-radius:8px;background:var(--accent);color:#fff;display:inline-block">访问</a>`
    : '<span class="tag idle">—</span>';
  const ops=t.external
    ? `<td colspan="2"><span class="tag idle">只读</span></td>`
    : `<td><label class="switch"><input type="checkbox" data-enable="${esc(t.id)}" ${t.enabled?'checked':''}>
       <span class="track"></span></label></td>
    <td><div class="row" style="gap:5px">
      <button class="sm" data-edit="${esc(t.id)}">编辑</button>
      <button class="sm danger" data-del="${esc(t.id)}">删除</button>
    </div></td>`;
  return `<tr>
    <td>${esc(t.name)}</td>
    <td>${typeTag(t.type)}</td>
    <td class="mono">${t.localPort?`${esc(t.localIP||'127.0.0.1')}:${t.localPort}`:'--'}</td>
    <td class="mono">${esc(remoteOf(t)||'-')}</td>
    <td>${accCell}</td>
    <td>${phaseTag(t.status,t.err)}</td>
    <td class="mono">${t.curConns||0}</td>
    <td>${esc(tunnelClientName(t))}</td>
    <td class="mono">${esc(tunnelServerAddr(t))}</td>
    ${ops}
  </tr>`;
}

$('#instList').addEventListener('change',async e=>{
  const id=e.target.dataset.instToggle;
  const tunnelID=e.target.dataset.enable;
  if(tunnelID){
    try{ await api('/api/tunnels/'+encodeURIComponent(tunnelID)+'/toggle',{method:'POST'});
      toast('隧道状态已更新','ok'); await Promise.all([loadTunnels(),loadOverview(),loadInstances()]);
    }catch(err){toast(err.message,'bad');}
    return;
  }
  if(!id)return;
  const want=e.target.checked;
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/toggle',
      {method:'POST',body:{enable:want}});
    toast(want?'实例已启用':'实例已停用','ok');
    await Promise.all([loadInstances(),loadOverview(),loadTunnels()]);
  }catch(err){
    toast(err.message,'bad');
    // 回滚开关，避免界面状态与实际不符。
    e.target.checked=!want;
  }
});

$('#instList').addEventListener('click',async e=>{
  const ed=e.target.dataset.instEdit, dl=e.target.dataset.instDel, add=e.target.dataset.instAddtunnel;
  // 隧道操作按钮（启停/编辑/删除）也在实例卡片里，统一在这里处理。
  const ted=e.target.dataset.edit, tdl=e.target.dataset.del;
  if(add){ tunnelModal({instanceId:add}); return; }
  if(ted){ const t=TUNNELS.find(x=>x.id===ted); if(t)tunnelModal(t); return; }
  if(tdl){
    const t=TUNNELS.find(x=>x.id===tdl);
    if(!confirm(`确定删除隧道「${t?t.name:''}」吗？`))return;
    try{ await api('/api/tunnels/'+encodeURIComponent(tdl),{method:'DELETE'});
      toast('隧道已删除','ok'); await Promise.all([loadTunnels(),loadOverview(),loadInstances()]);
    }catch(err){toast(err.message,'bad');}
    return;
  }
  if(ed){const i=INSTANCES.find(x=>x.id===ed);if(i)instanceModal(i);}
  if(dl){
    const i=INSTANCES.find(x=>x.id===dl);
    const n=i?i.tunnelCount:0;
    const extra=n>0?`\n该实例名下的 ${n} 条隧道会一并删除。`:'';
    if(!confirm(`确定删除实例「${i?i.name:''}」吗？${extra}\n此操作不可撤销。`))return;
    try{
      const r=await api('/api/instances/'+encodeURIComponent(dl),{method:'DELETE'});
      toast(`实例已删除，同时移除 ${r.removedTunnels||0} 条隧道`,'ok');
      await Promise.all([loadInstances(),loadOverview(),loadTunnels()]);
    }catch(err){toast(err.message,'bad');}
  }
});

$('#btnAddServerInst').onclick=()=>instanceModal(null,'server');
$('#btnAddClientInst').onclick=()=>instanceModal(null,'client');

// instanceModal 新增/编辑实例。服务端与客户端共用同一个弹窗，
// 按 kind 只显示相关字段，避免用户面对一半用不上的选项。
function instanceModal(inst,kind){
  const isEdit=!!inst;
  kind=kind||(inst?inst.kind:'server');
  const isServer=kind==='server';
  const s=(inst&&inst.server)||{};
  const c=(inst&&inst.client)||{};
  const LOGLEVELS=['trace','debug','info','warn','error'];

  const secretHint=v=>v?'已配置（留空保持不变）':'';

  const serverFields=`
    <div class="fgroup">基础设置</div>
    <div class="frow two">
      <div><label>绑定地址</label><input id="i_bindAddr" value="${esc(s.bindAddr||'0.0.0.0')}"></div>
      <div><label>绑定端口（frpc 连接端口）</label>
        <input id="i_bindPort" type="number" value="${s.bindPort||7000}"></div>
    </div>
    <div class="frow two">
      <div><label>认证令牌 token</label>
        <input id="i_token" placeholder="${esc(secretHint(s.hasToken))}">
        <div class="hint">客户端必须填写完全一致的令牌</div></div>
      <div><label>子域名后缀</label>
        <input id="i_subHost" value="${esc(s.subdomainHost||'')}" placeholder="例如 frp.example.com"></div>
    </div>
    <div class="frow two">
      <div><label>HTTP 虚拟主机端口</label><input id="i_vhttp" type="number" value="${s.vhostHttpPort||0}"></div>
      <div><label>HTTPS 虚拟主机端口</label><input id="i_vhttps" type="number" value="${s.vhostHttpsPort||0}"></div>
    </div>
    <div class="fgroup">高级设置</div>
    <div class="frow three">
      <div><label>KCP 端口（0 关闭）</label><input id="i_kcp" type="number" value="${s.kcpBindPort||0}"></div>
      <div><label>QUIC 端口（0 关闭）</label><input id="i_quic" type="number" value="${s.quicBindPort||0}"></div>
      <div><label>仪表盘端口</label><input id="i_dashPort" type="number" value="${s.dashboardPort||0}"></div>
    </div>
    <div class="frow two">
      <div><label>仪表盘账号</label><input id="i_dashUser" value="${esc(s.dashboardUser||'admin')}"></div>
      <div><label>仪表盘密码</label><input id="i_dashPwd" type="password"
        placeholder="${esc(secretHint(s.hasDashboardPwd))}"></div>
    </div>
    <div class="frow three">
      <div><label>每客户端最大端口数</label><input id="i_maxPorts" type="number" value="${s.maxPortsPerClient||0}"></div>
      <div><label>允许端口起始</label><input id="i_portStart" type="number" value="${s.allowPortsStart||0}"></div>
      <div><label>允许端口结束</label><input id="i_portEnd" type="number" value="${s.allowPortsEnd||0}"></div>
    </div>
    <div class="frow three">
      <div><label>日志级别</label><select id="i_logLevel">
        ${LOGLEVELS.map(l=>`<option value="${l}" ${(s.logLevel||'info')===l?'selected':''}>${l}</option>`).join('')}
      </select></div>
      <div><label>日志保留天数</label><input id="i_logDays" type="number" value="${s.logMaxDays||7}"></div>
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="i_tls" ${s.transportTLS?'checked':''}>
          <span class="track"></span><span>强制 TLS</span></label>
      </div>
    </div>`;

  const clientFields=`
    <div class="fgroup">基础设置</div>
    <div class="frow two">
      <div><label>服务端地址</label><input id="i_addr" value="${esc(c.serverAddr||'127.0.0.1')}"></div>
      <div><label>服务端端口</label><input id="i_port" type="number" value="${c.serverPort||7000}"></div>
    </div>
    <div class="frow two">
      <div><label>认证令牌 token</label>
        <input id="i_token" placeholder="${esc(secretHint(c.hasToken))}"></div>
      <div><label>客户端用户名 user</label>
        <input id="i_user" value="${esc(c.user||'')}" placeholder="留空则使用默认"></div>
    </div>
    <div class="fgroup">高级设置</div>
    <div class="frow three">
      <div><label>传输协议</label><select id="i_proto">
        ${['tcp','kcp','quic','websocket','wss'].map(p=>
          `<option value="${p}" ${(c.transportProtocol||'tcp')===p?'selected':''}>${p}</option>`).join('')}
      </select></div>
      <div><label>日志级别</label><select id="i_logLevel">
        ${LOGLEVELS.map(l=>`<option value="${l}" ${(c.logLevel||'info')===l?'selected':''}>${l}</option>`).join('')}
      </select></div>
      <div><label>日志保留天数</label><input id="i_logDays" type="number" value="${c.logMaxDays||7}"></div>
    </div>
    <div class="frow two">
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="i_tls" ${c.transportTLS?'checked':''}>
          <span class="track"></span><span>启用 TLS</span></label>
      </div>
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="i_failExit" ${c.loginFailExit?'checked':''}>
          <span class="track"></span><span>登录失败后退出</span></label>
      </div>
    </div>
    <div class="fgroup">新建隧道默认</div>
    <div class="frow two">
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="i_defEnc" ${c.useEncryption?'checked':''}>
          <span class="track"></span><span>默认加密传输</span></label>
      </div>
      <div style="display:flex;align-items:flex-end;padding-bottom:6px">
        <label class="switch"><input type="checkbox" id="i_defComp" ${c.useCompression?'checked':''}>
          <span class="track"></span><span>默认压缩传输</span></label>
      </div>
    </div>
    <div class="hint" style="margin-top:-6px;margin-bottom:4px">
      这两个是本实例新建隧道时的默认值，可在每条隧道里单独调整；不是 TLS。
    </div>`;

  modal(isEdit?`编辑实例 · ${inst.name}`:`新增${isServer?'服务端':'客户端'}实例`,`
    <div class="frow two">
      <div><label>实例名称</label>
        <input id="i_name" value="${esc(inst?inst.name:'')}"
          placeholder="${isServer?'例如 主服务端':'例如 公司内网'}"></div>
      <div><label>备注（可选）</label><input id="i_note" value="${esc(inst?inst.note:'')}"></div>
    </div>
    <div class="frow" style="margin-bottom:4px">
      <label class="switch"><input type="checkbox" id="i_enable" ${(!isEdit||inst.enabled)?'checked':''}>
        <span class="track"></span><span>启用该实例</span></label>
    </div>
    <div style="border-top:1px solid var(--line);margin:12px 0 14px"></div>
    ${isServer?serverFields:clientFields}
    <div class="row" style="margin-top:16px;justify-content:flex-end">
      <button id="i_cancel">取消</button>
      <button class="primary" id="i_save">${isEdit?'保存修改':'创建实例'}</button>
    </div>`);

  $('#i_cancel').onclick=closeModal;
  $('#i_save').onclick=async()=>{
    const g=sel=>{const el=$(sel);return el?el.value:'';};
    const n=sel=>{const el=$(sel);if(!el)return undefined;const v=el.value.trim();return v===''?undefined:Number(v);};
    const ck=sel=>{const el=$(sel);return el?el.checked:false;};
    const payload={
      name:str('#i_name').trim(),kind:kind,note:str('#i_note').trim(),
      id:inst?inst.id:''
    };
    if(isServer){
      payload.server={
        enable:ck('#i_enable'),bindAddr:g('#i_bindAddr'),bindPort:n('#i_bindPort')||0,
        kcpBindPort:n('#i_kcp')||0,quicBindPort:n('#i_quic')||0,
        dashboardPort:n('#i_dashPort')||0,dashboardUser:g('#i_dashUser'),
        dashboardPwd:g('#i_dashPwd'),vhostHttpPort:n('#i_vhttp')||0,
        vhostHttpsPort:n('#i_vhttps')||0,token:g('#i_token'),
        subdomainHost:g('#i_subHost'),maxPortsPerClient:n('#i_maxPorts')||0,
        allowPortsStart:n('#i_portStart')||0,allowPortsEnd:n('#i_portEnd')||0,
        logLevel:g('#i_logLevel'),logMaxDays:n('#i_logDays')||0,
        transportTLS:ck('#i_tls')
      };
    }else{
      payload.client={
        enable:ck('#i_enable'),serverAddr:g('#i_addr'),serverPort:n('#i_port')||0,
        token:g('#i_token'),user:g('#i_user'),transportProtocol:g('#i_proto'),
        logLevel:g('#i_logLevel'),logMaxDays:n('#i_logDays')||0,
        transportTLS:ck('#i_tls'),loginFailExit:ck('#i_failExit'),
        useEncryption:ck('#i_defEnc'),useCompression:ck('#i_defComp')
      };
    }
    try{
      const r = isEdit
        ? await api('/api/instances/'+encodeURIComponent(inst.id),{method:'PUT',body:payload})
        : await api('/api/instances',{method:'POST',body:payload});
      closeModal();
      // 后端可能回 200 + warning（实例已存但内核没起来），必须如实告知，
      // 否则用户会以为一切正常。
      if(r&&r.warning)toast(r.warning,'bad');
      else toast(isEdit?'实例已保存':'实例已创建','ok');
      await Promise.all([loadInstances(),loadOverview(),loadTunnels(),loadConfig()]);
    }catch(e){toast(e.message,'bad');}
  };
}

/* ---------------- 日志 ---------------- */
function startStreams(){
  stopStreams();
  startLogStream();
  evtSrc=new EventSource('/api/stream/events');
  evtSrc.addEventListener('status',ev=>{
    try{
      const d=JSON.parse(ev.data);
      HOST=d.host;
      if(STATUS){STATUS.host=d.host;}
      renderHost();
      pushSeries(d.host);
    }catch(e){}
  });
  evtSrc.onerror=()=>{};
}
function stopStreams(){
  if(logSrc){logSrc.close();logSrc=null;}
  if(evtSrc){evtSrc.close();evtSrc=null;}
}
function startLogStream(){
  if(logSrc)logSrc.close();
  const lv=$('#logLevel').value||'info';
  logSrc=new EventSource('/api/logs/stream?level='+encodeURIComponent(lv));
  logSrc.addEventListener('log',ev=>{
    try{ addLog(JSON.parse(ev.data)); }catch(e){}
  });
}
function addLog(e){
  logBuf.push(e);
  if(logBuf.length>1500)logBuf=logBuf.slice(-1200);
  const kw=$('#logKeyword').value.trim().toLowerCase();
  if(kw&&!(e.message+' '+(e.raw||'')+' '+e.source).toLowerCase().includes(kw))return;
  const srcFilter=$('#logSource')?$('#logSource').value:'';
  if(srcFilter&&e.source!==srcFilter)return;
  const box=$('#logbox');
  const atBottom=box.scrollTop+box.clientHeight>=box.scrollHeight-40;
  box.insertAdjacentHTML('beforeend',logLine(e,kw));
  while(box.childElementCount>1200)box.removeChild(box.firstChild);
  if(atBottom)box.scrollTop=box.scrollHeight;
  $('#logCount').textContent=box.childElementCount+' 条';
}
function logLine(e,kw){
  let msg=esc(e.message||e.raw||'');
  if(kw){
    const i=msg.toLowerCase().indexOf(kw);
    if(i>=0)msg=msg.slice(0,i)+'<mark>'+msg.slice(i,i+kw.length)+'</mark>'+msg.slice(i+kw.length);
  }
  const t=(e.time||'').replace('T',' ').slice(11,19);
  return `<div class="ln"><span class="t">${esc(t)}</span>`+
    `<span class="lv ${esc(e.level||'info')}">${esc(e.label||'信息')}</span>`+
    `<span class="src">${esc(e.source||'')}</span><span class="msg">${msg}</span></div>`;
}
function renderLogs(){
  const kw=$('#logKeyword').value.trim().toLowerCase();
  const lv=$('#logLevel').value;
  const srcFilter=$('#logSource')?$('#logSource').value:'';
  const rank={trace:0,debug:1,info:2,warn:3,error:4,fatal:5};
  const rows=logBuf.filter(e=>(rank[e.level]||0)>=(rank[lv]||2))
    .filter(e=>!srcFilter||e.source===srcFilter)
    .filter(e=>!kw||(e.message+' '+(e.raw||'')+' '+e.source).toLowerCase().includes(kw))
    .slice(-800);
  $('#logbox').innerHTML=rows.map(e=>logLine(e,kw)).join('');
  $('#logbox').scrollTop=$('#logbox').scrollHeight;
  $('#logCount').textContent=rows.length+' 条';
}
$('#logLevel').onchange=()=>{startLogStream();renderLogs();};
if($('#logSource'))$('#logSource').onchange=renderLogs;
let kwTimer=null;
$('#logKeyword').oninput=()=>{clearTimeout(kwTimer);kwTimer=setTimeout(renderLogs,180);};
$('#logLive').onchange=e=>{ if(e.target.checked)startLogStream(); else if(logSrc){logSrc.close();logSrc=null;} };
$('#btnLogClear').onclick=()=>{logBuf=[];renderLogs();};
$('#btnLogExport').onclick=()=>{
  const a=document.createElement('a');
  a.href='/api/logs/export?download=1';a.click();
};
$('#btnLogAnalyze').onclick=async()=>{
  if(analyzing)return; analyzing=true;
  try{
    const r=await api('/api/logs/analyze');
    const list=r.issues||[];
    modal('问题定位',list.length?list.map(i=>`
      <div class="card" style="margin-bottom:10px;background:var(--row-hover)">
        <div class="spread" style="margin-bottom:6px">
          <b>${esc(i.title)}</b><span class="tag warn">出现 ${i.count} 次</span></div>
        <div class="hint">可能原因：${esc(i.cause)}</div>
        <div class="hint" style="margin-top:4px;color:var(--ok-txt)">处理建议：${esc(i.fix)}</div>
        <div class="hint mono" style="margin-top:6px;opacity:.75">样例：${esc(i.sample||'')}</div>
      </div>`).join(''):'<div class="empty">最近没有发现需要处理的问题</div>');
  }catch(e){toast(e.message,'bad');}
  analyzing=false;
};

/* ---------------- QQ 扫码绑定 ---------------- */
// qqOfficialHint 在「启用」已打开但凭据缺失时给出明确指引，
// 避免用户看到「未填写 AppID」却不知道该去哪配。
function qqOfficialHint(){
  const on=$('#qo_enable').checked;
  const hasCreds=str('#qo_appid').trim()&&str('#qo_secret').trim();
  const el=$('#qoCredHint');
  if(!el)return;
  if(on&&!hasCreds){
    el.classList.remove('hidden');
    el.textContent='已启用但尚未配置凭据：点上方「扫码绑定」用手机 QQ 扫码即可自动填入，或手动填写 AppID 与 AppSecret。';
  }else{
    el.classList.add('hidden');
  }
}
$('#qo_enable').onchange=qqOfficialHint;
$('#qo_appid').oninput=qqOfficialHint;
$('#qo_secret').oninput=qqOfficialHint;
// 手动改动 openid 后立刻重新校验，不必等下次刷新状态。
$('#qo_groups').oninput=()=>qqOpenIDCheck();
$('#qo_user').oninput=()=>qqOpenIDCheck();

let QQ_BIND_TIMER=null, QQ_BIND_LEFT=0;
function qqBindStop(){
  if(QQ_BIND_TIMER){ clearInterval(QQ_BIND_TIMER); QQ_BIND_TIMER=null; }
}
function qqBindShow(info){
  $('#qqBindBox').classList.remove('hidden');
  if(info.qrDataUrl)$('#qqBindQR').src=info.qrDataUrl;
  $('#qqBindStatus').textContent=info.status||'等待扫码';
  $('#qqBindStatus').className='tag idle';
  QQ_BIND_LEFT=info.expiresIn>0?info.expiresIn:150;
  qqBindStop();
  QQ_BIND_TIMER=setInterval(async()=>{
    QQ_BIND_LEFT--;
    $('#qqBindTimer').textContent=QQ_BIND_LEFT>0?`二维码约 ${QQ_BIND_LEFT} 秒后过期`:'二维码可能已过期，可取消后重新生成';
    if(QQ_BIND_LEFT<=0)qqBindStop();
    try{
      const r=await api('/api/qqbot/bind/poll',{method:'POST'});
      $('#qqBindStatus').textContent=r.status||'等待扫码';
      if(r.success){
        qqBindStop();
        $('#qqBindStatus').textContent='绑定成功';
        $('#qqBindStatus').className='tag ok';
        toast('扫码绑定成功，已自动填入 AppID 并开启事件监听','ok');
        // 回填凭据与开关，让用户看到结果
        fill('#qo_appid',r.appId);
        if(r.userOpenId&&!str('#qo_user'))fill('#qo_user',r.userOpenId);
        fill('#qo_enable',true);
        fill('#qo_listen',true);
        qqOfficialHint();
        setTimeout(()=>{ $('#qqBindBox').classList.add('hidden'); loadQQBotStatus(); },2200);
      }else if((r.statusCode||0)===3){
        qqBindStop();
        $('#qqBindStatus').className='tag bad';
        $('#qqBindStatus').textContent='二维码已过期';
      }else if((r.statusCode||0)===1){
        $('#qqBindStatus').className='tag warn';
      }
    }catch(e){ /* 轮询失败保持静默，等待下一轮 */ }
  },2500);
}
$('#btnQQBind').onclick=async()=>{
  const b=$('#btnQQBind');b.disabled=true;b.textContent='生成中...';
  try{
    const r=await api('/api/qqbot/bind',{method:'POST',body:{source:'YCFRP'}});
    qqBindShow(r);
    toast('二维码已生成，请用手机 QQ 扫码','ok');
  }catch(e){ toast('生成二维码失败：'+e.message,'bad'); }
  b.disabled=false;b.textContent='扫码绑定';
};
$('#btnQQBindCancel').onclick=async()=>{
  qqBindStop();
  try{ await api('/api/qqbot/bind',{method:'DELETE'}); }catch(e){}
  $('#qqBindBox').classList.add('hidden');
  toast('已取消绑定','ok');
};
// 页面加载时若已有进行中的会话则恢复显示
async function qqBindRestore(){
  try{
    const r=await api('/api/qqbot/bind');
    if(r&&r.qrDataUrl&&r.expiresIn>0)qqBindShow(r);
  }catch(e){}
}

/* ---------------- QQ 官方机器人 ---------------- */
let QQ_TIMER=null;
let QQ_DISCOVERED=[];
async function loadQQBotStatus(){
  try{
    const r=await api('/api/qqbot/status');
    const st=r.status||{};
    const tag=$('#qoConn');
    if(!r.listen){
      tag.className='tag idle';tag.textContent='未开启监听';
    }else if(!r.hasCreds){
      tag.className='tag warn';tag.textContent='缺少 AppID/Secret';
    }else if(st.connected){
      tag.className='tag ok';tag.textContent='已连接';
    }else if(st.running){
      tag.className='tag warn';tag.textContent='连接中';
    }else{
      tag.className='tag bad';tag.textContent='未连接';
    }
    const bits=[];
    if(st.lastEventAt)bits.push(`最近事件 ${st.lastEventAt}`);
    if(st.lastEvent)bits.push(`类型 ${st.lastEvent}`);
    if(st.events)bits.push(`累计 ${st.events} 条`);
    if(st.lastError)bits.push(`错误：${st.lastError}`);
    $('#qoStatusText').textContent=bits.join(' · ');
    renderDiscovered(r.discovered||[]);
    qqOpenIDCheck(r.discovered||[]);
  }catch(e){ $('#qoStatusText').textContent='读取状态失败：'+e.message; }
}
// qqOpenIDCheck 校验已填的 openid 是否属于当前机器人。
// openid 由 QQ 按机器人分别生成，跨机器人复制过来只会得到「资源不存在」，
// 提前点出来比让用户对着泛化报错排查省事得多。
function qqOpenIDCheck(list){
  if(list)QQ_DISCOVERED=list;
  list=QQ_DISCOVERED;
  const el=$('#qoWarn');if(!el)return;
  const known=new Set();
  (list||[]).forEach(d=>{if(d.groupOpenId)known.add(d.groupOpenId);if(d.userOpenId)known.add(d.userOpenId);});
  if(!known.size){el.classList.add('hidden');return;}
  const bad=[];
  const groups=str('#qo_groups').trim().split(',').map(s=>s.trim()).filter(Boolean);
  groups.forEach(g=>{if(!known.has(g))bad.push('群 '+g);});
  const u=str('#qo_user').trim();
  if(u&&!known.has(u))bad.push('用户 '+u);
  if(!bad.length){el.classList.add('hidden');return;}
  el.classList.remove('hidden');
  el.textContent='⚠ 以下 openid 不属于当前机器人：'+bad.join('、')
    +'。openid 由 QQ 按机器人分别生成、跨机器人不通用，'
    +'若这是从别的面板复制来的，请改用下方「自动发现」里本机器人捕获到的 openid 并点「填入」。';
}
function renderDiscovered(list){
  if(!list.length){
    $('#qoDiscovered').innerHTML='<div class="empty" style="padding:16px">还没有发现 openid。开启监听后，把机器人拉进群并在群里 @ 一次。</div>';
    return;
  }
  $('#qoDiscovered').innerHTML=list.slice().reverse().map(d=>{
    const id=d.groupOpenId||d.userOpenId||'';
    const isGroup=!!d.groupOpenId;
    return `<div class="row" style="justify-content:space-between;gap:8px;padding:8px 10px;border:1px solid var(--line);border-radius:10px;background:var(--row-hover);margin-bottom:8px">
      <div style="min-width:0">
        <div class="mono" style="word-break:break-all">${esc(id)}</div>
        <div class="hint">${isGroup?'群':'用户'} · ${esc(d.kind||'')} · ${esc(d.at||'')}${d.msgId?' · 含消息ID':''}</div>
      </div>
      <div class="row" style="gap:6px;flex:0 0 auto">
        <button class="sm" data-use="${esc(id)}" data-kind="${isGroup?'group':'user'}" data-mid="${esc(d.msgId||'')}">填入</button>
      </div>
    </div>`;
  }).join('');
}
$('#qoDiscovered').addEventListener('click',async e=>{
  const btn=e.target.closest('[data-use]');if(!btn)return;
  const id=btn.dataset.use, kind=btn.dataset.kind, mid=btn.dataset.mid||'';
  if(kind==='group'){
    const cur=str('#qo_groups').trim();
    const parts=cur?cur.split(',').map(s=>s.trim()).filter(Boolean):[];
    if(!parts.includes(id))parts.push(id);
    $('#qo_groups').value=parts.join(',');
  }else{
    $('#qo_user').value=id;
  }
  // 一并带上消息 ID：被动回复不受主动消息额度限制，发送成功率更高。
  if(mid){
    fill('#qo_msgid',mid);
    toast('已填入 openid 与消息 ID（被动回复方式，成功率更高）','ok');
  }else{
    toast('已填入，记得点击「保存通知设置」生效','ok');
  }
  qqOpenIDCheck();
});
$('#btnQQRefresh').onclick=async()=>{
  const b=$('#btnQQRefresh');b.disabled=true;b.textContent='刷新中...';
  await loadQQBotStatus();
  b.disabled=false;b.textContent='刷新状态';
};
$('#btnQQClear').onclick=async()=>{
  if(!confirm('确定清空已发现的 openid 记录吗？'))return;
  try{ await api('/api/qqbot/discover',{method:'DELETE'}); toast('记录已清空','ok'); loadQQBotStatus(); }
  catch(e){ toast(e.message,'bad'); }
};
// 通知页可见时定时刷新连接状态，便于等待事件到达。
setInterval(()=>{
  const page=$$('section[data-view]').find(s=>!s.classList.contains('hidden'));
  if(page&&page.dataset.view==='settings'){const t=localStorage.getItem('ycfrp_settings_tab')||'notify';if(t==='notify')loadQQBotStatus();}
},8000);

/* ---------------- 通知 ---------------- */
function renderChannels(){
  $('#channelList').innerHTML=CHANNELS.map((c,i)=>`
    <div class="card" style="margin-bottom:10px;background:var(--row-hover)">
      <div class="frow three">
        <div><label>通道名称</label><input data-ch="${i}" data-k="name" value="${esc(c.name||'')}"></div>
        <div><label>请求方式</label><select data-ch="${i}" data-k="type">
          <option value="post" ${c.type!=='get'?'selected':''}>POST（JSON）</option>
          <option value="get" ${c.type==='get'?'selected':''}>GET</option></select></div>
        <div style="display:flex;align-items:flex-end;padding-bottom:6px">
          <label class="switch"><input type="checkbox" data-ch="${i}" data-k="enable" ${c.enable?'checked':''}>
            <span class="track"></span><span>启用</span></label></div>
      </div>
      <div class="frow"><label>Webhook 地址</label>
        <input data-ch="${i}" data-k="url" value="${esc(c.url||'')}" placeholder="https://..."></div>
      <div class="frow two">
        <div><label>附加密钥（可选）</label><input data-ch="${i}" data-k="secret" value="${esc(c.secret||'')}"></div>
        <div style="display:flex;align-items:flex-end"><button class="sm danger" data-chdel="${i}">删除该通道</button></div>
      </div>
    </div>`).join('');
  $('#channelEmpty').classList.toggle('hidden',CHANNELS.length>0);
}
$('#channelList').addEventListener('input',e=>{
  const el=e.target,i=el.dataset.ch,k=el.dataset.k;
  if(i==null||!k)return;
  CHANNELS[i][k]=el.type==='checkbox'?el.checked:el.value;
});
$('#channelList').addEventListener('change',e=>{
  const el=e.target,i=el.dataset.ch,k=el.dataset.k;
  if(i!=null&&k)CHANNELS[i][k]=el.type==='checkbox'?el.checked:el.value;
});
$('#channelList').addEventListener('click',e=>{
  const i=e.target.dataset.chdel;if(i==null)return;
  CHANNELS.splice(Number(i),1);renderChannels();
});
$('#btnAddChannel').onclick=()=>{
  CHANNELS.push({id:'',name:'新通道',type:'post',enable:false,url:'',secret:'',headers:{}});
  renderChannels();
};
$('#btnSaveNotify').onclick=async()=>{
  // 密钥输入框留空表示保持原值，因此不把空值提交上去，
  // 否则会把已保存的密钥覆盖成空（后端也有保护，这里是双保险）。
  const secret=(sel)=>{const v=str(sel).trim();return v===''?undefined:v;};
  try{
    const payload={notify:{
      enable:$('#n_enable').checked,monitorErrors:$('#n_err').checked,
      monitorOffline:$('#n_off').checked,monitorTraffic:$('#n_traffic').checked,
      keywords:str('#n_keywords').split(',').map(s=>s.trim()).filter(Boolean),
      quietMinutes:num('#n_quiet')||5,
      qq:{enable:$('#q_enable').checked,protocol:str('#q_proto'),host:str('#q_host'),
        port:num('#q_port'),adminQQ:str('#q_admin'),
        groupIds:str('#q_groups').split(',').map(s=>s.trim()).filter(Boolean)},
      qqOfficial:{enable:$('#qo_enable').checked,appId:str('#qo_appid').trim(),
        sandbox:$('#qo_sandbox').checked,
        groupOpenIds:str('#qo_groups'),userOpenId:str('#qo_user').trim(),msgId:str('#qo_msgid').trim(),
        proxy:str('#qo_proxy').trim(),listen:$('#qo_listen').checked},
      wecom:{enable:$('#wc_enable').checked,
        mentionList:str('#wc_mention'),mentionAll:$('#wc_all').checked},
      dingtalk:{enable:$('#dt_enable').checked,webhook:str('#dt_hook').trim(),
        atMobiles:str('#dt_mobiles'),atAll:$('#dt_all').checked},
      feishu:{enable:$('#fs_enable').checked,webhook:str('#fs_hook').trim()},
      serverChan:{enable:$('#sc_enable').checked,channel:str('#sc_channel').trim()},
      bark:{enable:$('#bk_enable').checked,server:str('#bk_server').trim(),group:str('#bk_group').trim()},
      telegram:{enable:$('#tg_enable').checked,chatId:str('#tg_chat').trim(),proxy:str('#tg_proxy').trim()},
      channels:CHANNELS
    }};
    // 仅在用户填了新值时才带上密钥字段。
    const at=secret('#q_token'); if(at!==undefined)payload.notify.qq.accessToken=at;
    const as=secret('#qo_secret'); if(as!==undefined)payload.notify.qqOfficial.appSecret=as;
    const wk=secret('#wc_key'); if(wk!==undefined)payload.notify.wecom.key=wk;
    const ds=secret('#dt_secret'); if(ds!==undefined)payload.notify.dingtalk.secret=ds;
    const fes=secret('#fs_secret'); if(fes!==undefined)payload.notify.feishu.secret=fes;
    const sck=secret('#sc_key'); if(sck!==undefined)payload.notify.serverChan.sendKey=sck;
    const bdk=secret('#bk_key'); if(bdk!==undefined)payload.notify.bark.deviceKey=bdk;
    const tgt=secret('#tg_token'); if(tgt!==undefined)payload.notify.telegram.botToken=tgt;

    await api('/api/config',{method:'PUT',body:payload});
    toast('通知设置已保存','ok');
    await loadConfig();
  }catch(e){toast(e.message,'bad');}
};
$('#btnTestNotify').onclick=async()=>{
  $('#testResult').textContent='正在发送测试通知 ...';
  try{
    const r=await api('/api/notify/test',{method:'POST'});
    $('#testResult').innerHTML=(r.results||[]).map(x=>
      `<div style="color:${x.ok?'var(--ok-txt)':'var(--bad-txt)'}">${x.ok?'✔':'✖'} ${esc(x.channel)}${x.ok?' 推送成功':'：'+esc(x.error||'失败')}</div>`
    ).join('')||'未配置任何启用的通道';
    toast(r.anySuccess?'测试通知已发出':'测试通知发送失败',r.anySuccess?'ok':'bad');
  }catch(e){$('#testResult').innerHTML=`<span style="color:var(--bad-txt)">${esc(e.message)}</span>`;}
};

/* ---------------- 界面自定义（精简：主题 + 主题色 + 站点信息） ---------------- */
function hexToRgb(hex){
  const m=/^#?([0-9a-f]{6})$/i.exec(String(hex||'').trim());
  if(!m)return null;
  const n=parseInt(m[1],16);
  return {r:(n>>16)&255,g:(n>>8)&255,b:n&255};
}
// applyAccent 把主题色扩散到强调色相关的 CSS 变量，确保按钮、选中态、
// 进度条与图表都跟随，而不是只改一个 --accent。
function applyAccent(hex){
  const rgb=hexToRgb(hex);if(!rgb)return;
  let clean='#'+String(hex).replace('#','').toLowerCase();
  // 旧版本的默认主题色是 #4f8cff；升级到新界面时沿用新默认的青蓝，
  // 避免所有老用户保存过的旧蓝色把新配色盖掉。
  if(clean==='#4f8cff')clean='#22d3ee';
  const root=document.documentElement.style;
  root.setProperty('--accent',clean);
  root.setProperty('--accent-soft',`rgba(${rgb.r},${rgb.g},${rgb.b},.14)`);
  root.setProperty('--accent-border',`rgba(${rgb.r},${rgb.g},${rgb.b},.42)`);
  root.setProperty('--accent-ring',`rgba(${rgb.r},${rgb.g},${rgb.b},.18)`);
}
function setTheme(theme){
  const next=theme==='light'?'light':'dark';
  document.documentElement.dataset.theme=next;
  document.body.classList.toggle('light',next==='light');
  const sel=$('#u_theme');if(sel)sel.value=next;
  syncThemeButton();
  redrawActiveChart();
}
$('#u_accent').oninput=()=>{
  $('#u_accentText').value=$('#u_accent').value;
  applyAccent($('#u_accent').value);
  redrawActiveChart();
};
$('#u_accentText').oninput=()=>{
  const v=$('#u_accentText').value.trim();
  if(/^#[0-9a-fA-F]{6}$/.test(v)){
    $('#u_accent').value=v;
    applyAccent(v);
    redrawActiveChart();
  }
};
$('#u_theme').onchange=async()=>{
  const next=str('#u_theme');
  setTheme(next);
  try{
    await api('/api/config',{method:'PUT',body:{ui:{theme:next}}});
    toast(next==='light'?'已切换为浅色主题':'已切换为深色主题','ok');
  }catch(e){
    setTheme(next==='light'?'dark':'light');
    toast('主题切换失败：'+e.message,'bad');
  }
};
$('#btnSaveUI').onclick=async()=>{
  try{
    await api('/api/config',{method:'PUT',body:{ui:{
      siteName:str('#u_siteName'),siteFooter:str('#u_footer'),
      accentColor:str('#u_accent'),theme:str('#u_theme')
    }}});
    META=await api('/api/meta');
    $('#siteName').textContent=META.ui.siteName||META.product;
    $('#siteFooter').textContent=META.ui.siteFooter||(META.product+' · FRP 双端管理面板');
    applyUI(META.ui,false);
    toast('界面设置已保存','ok');
  }catch(e){toast(e.message,'bad');}
};
function applyUI(ui,persist){
  if(!ui)return;
  if(ui.accentColor)applyAccent(ui.accentColor);
  if(ui.theme)setTheme(ui.theme);
  if(ui.siteName)$('#siteName').textContent=ui.siteName;
  const foot=$('#siteFooter');
  if(foot)foot.textContent=ui.siteFooter||((ui.siteName||'YCFRP')+' · FRP 双端管理面板');
  if(persist){
    if($('#u_siteName'))$('#u_siteName').value=ui.siteName||'';
    if($('#u_footer'))$('#u_footer').value=ui.siteFooter||'';
    if($('#u_accent')&&hexToRgb(ui.accentColor))$('#u_accent').value=ui.accentColor;
    if($('#u_accentText'))$('#u_accentText').value=ui.accentColor||'';
  }
}
// syncThemeButton 只更新图标按钮的提示，不再改 textContent，
// 否则会把按钮里的太阳/月亮图标一起清掉。
function syncThemeButton(){
  const btn=$('#btnTheme');if(!btn)return;
  const light=document.documentElement.dataset.theme==='light';
  btn.title=light?'切换为深色主题':'切换为浅色主题';
  btn.setAttribute('aria-label',btn.title);
}
$('#btnTheme').onclick=async()=>{
  const next=document.documentElement.dataset.theme==='light'?'dark':'light';
  setTheme(next);
  try{
    await api('/api/config',{method:'PUT',body:{ui:{theme:next}}});
    toast(next==='light'?'已切换为浅色主题':'已切换为深色主题','ok');
  }catch(e){
    setTheme(next==='light'?'dark':'light');
    toast('主题切换失败：'+e.message,'bad');
  }
};
$('#btnSaveAuto').onclick=async()=>{
  try{
    await api('/api/config',{method:'PUT',body:{update:{
      checkEnabled:$('#up_auto').checked,
      checkIntervalHours:Number($('#up_interval').value)||6,
      notifyOnUpdate:$('#up_notify').checked}}});
    toast($('#up_auto').checked?'已开启自动检查更新':'已关闭自动检查更新','ok');
    await loadUpdateInfo();
  }catch(e){toast(e.message,'bad');}
};
$('#btnCheckNow').onclick=async()=>{
  const b=$('#btnCheckNow');b.disabled=true;b.textContent='检查中...';
  $('#upAutoResult').textContent='正在检查 ...';
  try{
    const r=await api('/api/update/checknow',{method:'POST'});
    UPDATE_LATEST=r;
    $('#upLatest').textContent=r.latest||'-';
    $('#upLatest').style.color=r.hasUpdate?'var(--warn)':'var(--ok)';
    $('#upLatestSub').textContent=r.hasUpdate?'有新版本可用':'已是最新版本';
    if(r.hasUpdate){
      $('#upAutoResult').innerHTML=`<span style="color:var(--warn-txt)">发现新版本 ${esc(r.latest)}，已按设置推送通知${r.notes?' · '+esc(r.notes):''}</span>`;
      $('#btnOnlineUpdate').disabled=!r.canAuto;
    }else{
      $('#upAutoResult').innerHTML=`<span style="color:var(--ok-txt)">当前已是最新版本 ${esc(r.latest)}</span>`;
    }
    await loadUpdateInfo();
  }catch(e){
    $('#upAutoResult').innerHTML=`<span style="color:var(--bad-txt)">检查失败：${esc(e.message)}</span>`;
  }
  b.disabled=false;b.textContent='立即检查一次';
};

/* ---------------- 更新日志 ---------------- */
const LOG_KIND={add:['新增','ok'],fix:['修复','warn'],change:['调整','idle']};
function renderChangelog(list){
  if(!list||!list.length){
    $('#upChangelog').innerHTML='更新源未提供版本说明。';
    return;
  }
  $('#upChangelog').innerHTML=list.map(it=>{
    const k=LOG_KIND[it.kind]||LOG_KIND.change;
    const items=(it.items||[]).map(x=>`<div class="hint" style="margin-top:5px">· ${esc(x)}</div>`).join('');
    return `<div style="padding:10px 0;border-bottom:1px solid var(--line)">
      <div class="row" style="gap:8px">
        <b class="mono" style="font-size:13px">${esc(it.version||'')}</b>
        <span class="tag ${k[1]}">${k[0]}</span>
        <span class="hint">${esc(it.date||'')}</span>
      </div>
      ${items}
    </div>`;
  }).join('');
}
$('#btnLoadChangelog').onclick=async()=>{
  const b=$('#btnLoadChangelog');b.disabled=true;b.textContent='加载中...';
  try{
    const r=await api('/api/update/check');
    renderChangelog(r.changelog);
    toast('更新日志已加载','ok');
  }catch(e){
    $('#upChangelog').innerHTML=`<span style="color:var(--bad-txt)">加载失败：${esc(e.message)}</span>`;
  }
  b.disabled=false;b.textContent='加载更新日志';
};

/* ---------------- 更新 ---------------- */
let UPDATE_INFO=null, UPDATE_LATEST=null, PICKED_PKG=null, UPDATE_MODE=null;

async function loadUpdateInfo(){
  try{
    const m=await api('/api/meta');
    META=m;
    UPDATE_INFO=m.update||{};
    UPDATE_MODE=m.updateMode||{};
    $('#upCur').textContent=UPDATE_INFO.current||'-';
    $('#upCurSub').textContent='内核 '+(m.kernelVersion||'-');
    $('#upPlatform').textContent=UPDATE_INFO.platform||'-';
    if($('#up_source').value==='')$('#up_source').value=UPDATE_INFO.source||'';
    if($('#up_proxy').value==='')$('#up_proxy').value=UPDATE_INFO.proxy||'';
    // 自动检查设置回显
    fill('#up_auto',UPDATE_INFO.checkEnabled);
    if($('#up_interval').value==='')$('#up_interval').value=UPDATE_INFO.checkHours||6;
    fill('#up_notify',UPDATE_INFO.notifyOnUpdate!==false);
    const last=UPDATE_INFO.lastResult;
    if(last){
      const when=last.checkedAt||'';
      let line=`最近检查：${when}`;
      if(last.err)line+=` · 失败：${esc(last.err)}`;
      else if(last.hasUpdate)line+=` · 发现新版本 ${esc(last.latest)}`;
      else line+=` · 已是最新版本`;
      $('#upAutoState').innerHTML=line;
      if(last.hasUpdate){
        $('#upAutoResult').innerHTML=`<span style="color:var(--warn-txt)">检测到新版本 ${esc(last.latest)}${UPDATE_MODE&&UPDATE_MODE.docker?'，请在宿主机执行上方升级命令':'，可点击上方「下载并安装更新」'}</span>`;
      }
    }else if(UPDATE_INFO.lastCheckAt){
      $('#upAutoState').textContent=`最近检查：${UPDATE_INFO.lastCheckAt}`;
    }
    if(UPDATE_MODE.docker){
      $('#upDockerHint').classList.remove('hidden');
      // Docker 部署：隐藏「普通安装包」上传卡片，改为镜像包上传卡片。
      $('#upUploadCard').classList.add('hidden');
      $('#upDockerUploadCard').classList.remove('hidden');
    }else{
      $('#upDockerHint').classList.add('hidden');
      $('#upUploadCard').classList.remove('hidden');
      $('#upDockerUploadCard').classList.add('hidden');
      const allow=UPDATE_INFO.allowUpload!==false;
      $('#btnPickPkg').disabled=!allow;
      if(!allow)$('#upFileName').textContent='当前配置禁用了上传更新';
    }
  }catch(e){toast('读取更新信息失败：'+e.message,'bad');}
}
function updateProgress(box,bar,txt,text,pct){
  $(box).classList.remove('hidden');
  $(txt).textContent=text;
  $(bar).style.width=Math.max(0,Math.min(100,pct||0))+'%';
}
$('#btnSaveUpdateSrc').onclick=async()=>{
  try{
    await api('/api/config',{method:'PUT',body:{update:{
      sourceUrl:str('#up_source').trim(),proxy:str('#up_proxy').trim()}}});
    toast('更新源已保存','ok');
  }catch(e){toast(e.message,'bad');}
};
$('#btnCheckUpdate').onclick=async()=>{
  const b=$('#btnCheckUpdate');b.disabled=true;b.textContent='检查中...';
  try{
    const r=await api('/api/update/check');
    UPDATE_LATEST=r;
    $('#upLatest').textContent=r.latest||'-';
    $('#upLatest').style.color=r.hasUpdate?'var(--warn)':'var(--ok)';
    $('#upLatestSub').textContent=r.hasUpdate?'有新版本可用':'已是最新版本';
    if(r.releasedAt||r.notes){
      $('#upNotes').classList.remove('hidden');
      $('#upNotes').innerHTML=(r.releasedAt?`更新时间：${esc(r.releasedAt)}<br>`:'')+
        (r.notes?esc(r.notes):'');
    }
    if(r.hasUpdate&&r.docker){
      if(r.dockerSelf){
        toast('发现新版本 '+r.latest+'，可在线拉取镜像更新','ok');
      }else{
        toast('发现新版本 '+r.latest+'，未挂载 docker.sock，请在宿主机执行 compose 升级命令','ok');
      }
      $('#upDockerHint').classList.remove('hidden');
    }else if(r.hasUpdate&&!r.available){
      toast('发现新版本 '+r.latest+'，但更新源未提供 '+r.platform+' 的安装包','bad');
    }else if(r.hasUpdate){
      toast('发现新版本 '+r.latest+'，可在线更新','ok');
    }else{
      toast('当前已是最新版本 '+r.latest,'ok');
    }
    $('#btnOnlineUpdate').disabled=!r.canAuto;
  }catch(e){
    $('#upLatest').textContent='-';
    $('#upLatestSub').textContent='检查失败';
    toast('检查更新失败：'+e.message,'bad');
  }
  b.disabled=false;b.textContent='检查更新';
  loadUpdateInfo();
};
$('#btnOnlineUpdate').onclick=async()=>{
  const latest=UPDATE_LATEST&&UPDATE_LATEST.latest||'新版本';
  const isDocker=UPDATE_MODE&&UPDATE_MODE.docker;
  const tip=isDocker
    ? `确定拉取最新镜像并重启容器吗？\n面板会短暂无法访问，稍后自动恢复。`
    : `确定下载并安装 ${latest} 吗？\n面板会在替换完成后自动重启，期间短暂无法访问。`;
  if(!confirm(tip))return;
  const b=$('#btnOnlineUpdate');b.disabled=true;b.textContent='更新中...';
  updateProgress('#upProgress','#upProgressBar','#upProgressText',isDocker?'正在拉取最新镜像，请稍候 ...':'正在下载安装包，请稍候 ...',30);
  try{
    const r=isDocker
      ? await api('/api/update/docker-apply',{method:'POST',body:{}})
      : await api('/api/update/apply',{method:'POST',body:{}});
    updateProgress('#upProgress','#upProgressBar','#upProgressText',r.message||'更新已开始，正在重启 ...',100);
    toast('更新已开始，面板将在数秒后重启','ok');
    waitPanelBack();
  }catch(e){
    $('#upProgress').classList.add('hidden');
    toast('在线更新失败：'+e.message,'bad');
    b.disabled=false;b.textContent='下载并安装更新';
  }
};
$('#btnPickPkg').onclick=()=>$('#upFile').click();
$('#upFile').onchange=e=>{
  PICKED_PKG=e.target.files[0]||null;
  $('#upFileName').textContent=PICKED_PKG?`${PICKED_PKG.name}（${bytes(PICKED_PKG.size)}）`:'未选择文件';
  $('#btnUploadUpdate').disabled=!PICKED_PKG;
};
$('#btnPickDockerPkg').onclick=()=>$('#upDockerFile').click();
$('#upDockerFile').onchange=e=>{
  const f=e.target.files[0]||null;
  $('#upDockerFileName').textContent=f?`${f.name}（${bytes(f.size)}）`:'未选择文件';
  $('#btnUploadDockerUpdate').disabled=!f;
};
$('#btnUploadDockerUpdate').onclick=async()=>{
  const f=$('#upDockerFile').files[0];
  if(!f)return;
  if(!confirm(`确定用「${f.name}」更新容器镜像吗？\n面板载入镜像后会重启容器，期间短暂无法访问。`))return;
  const b=$('#btnUploadDockerUpdate');b.disabled=true;b.textContent='上传中...';
  const fd=new FormData();fd.append('file',f);
  updateProgress('#upDockerUploadProgress','#upDockerUploadBar','#upDockerUploadText','正在上传镜像包 ...',10);
  try{
    const xhr=new XMLHttpRequest();
    const done=new Promise((res,rej)=>{
      xhr.upload.onprogress=ev=>{
        if(ev.lengthComputable)
          updateProgress('#upDockerUploadProgress','#upDockerUploadBar','#upDockerUploadText',
            `正在上传 ${bytes(ev.loaded)} / ${bytes(ev.total)}`,ev.loaded/ev.total*90);
      };
      xhr.onload=()=>{
        let d={};
        try{d=JSON.parse(xhr.responseText);}catch(e){}
        if(xhr.status>=200&&xhr.status<300&&d.ok!==false)res(d.data!==undefined?d.data:d);
        else rej(new Error(d.error||('上传失败 '+xhr.status)));
      };
      xhr.onerror=()=>rej(new Error('网络错误，上传中断'));
    });
    xhr.open('POST','/api/update/docker-upload');
    xhr.send(fd);
    const r=await done;
    updateProgress('#upDockerUploadProgress','#upDockerUploadBar','#upDockerUploadText',r.message||'更新已开始，正在重启 ...',100);
    toast('镜像已载入，容器即将重启','ok');
    waitPanelBack();
  }catch(e){
    $('#upDockerUploadProgress').classList.add('hidden');
    toast('镜像更新失败：'+e.message,'bad');
    b.disabled=false;b.textContent='上传镜像并更新';
  }
};
$('#btnUploadUpdate').onclick=async()=>{
  if(!PICKED_PKG)return;
  if(!confirm(`确定用「${PICKED_PKG.name}」替换当前程序吗？\n面板会在替换完成后自动重启。`))return;
  const b=$('#btnUploadUpdate');b.disabled=true;b.textContent='上传中...';
  const fd=new FormData();fd.append('file',PICKED_PKG);
  updateProgress('#upUploadProgress','#upUploadBar','#upUploadText','正在上传安装包 ...',10);
  try{
    const xhr=new XMLHttpRequest();
    const done=new Promise((res,rej)=>{
      xhr.upload.onprogress=ev=>{
        if(ev.lengthComputable)
          updateProgress('#upUploadProgress','#upUploadBar','#upUploadText',
            `正在上传 ${bytes(ev.loaded)} / ${bytes(ev.total)}`,ev.loaded/ev.total*90);
      };
      xhr.onload=()=>{
        let d={};
        try{d=JSON.parse(xhr.responseText);}catch(e){}
        if(xhr.status>=200&&xhr.status<300&&d.ok!==false)res(d.data!==undefined?d.data:d);
        else rej(new Error(d.error||('上传失败 '+xhr.status)));
      };
      xhr.onerror=()=>rej(new Error('网络错误，上传中断'));
    });
    xhr.open('POST','/api/update/upload');
    xhr.send(fd);
    const r=await done;
    updateProgress('#upUploadProgress','#upUploadBar','#upUploadText',r.message||'更新已开始，正在重启 ...',100);
    toast('上传成功，面板即将重启','ok');
    waitPanelBack();
  }catch(e){
    $('#upUploadProgress').classList.add('hidden');
    toast('上传更新失败：'+e.message,'bad');
    b.disabled=false;b.textContent='上传并安装';
  }
};
// 面板替换期间会短暂断连，轮询等待新实例就绪后自动刷新。
async function waitPanelBack(){
  for(let i=0;i<80;i++){
    await new Promise(r=>setTimeout(r,1000));
    try{
      const res=await fetch('/api/auth/status',{credentials:'same-origin'});
      if(res.ok){
        const d=await res.json();
        if(d&&d.ok!==false){ toast('新版本已就绪，正在刷新页面','ok'); setTimeout(()=>location.reload(),800); return; }
      }
    }catch(e){}
  }
  toast('等待面板重启超时，请手动刷新页面','bad');
}

/* ---------------- 账号 ---------------- */
$('#btnSaveAccount').onclick=async()=>{
  try{
    await api('/api/account',{method:'POST',body:{
      oldPassword:str('#a_old'),newUsername:str('#a_user'),newPassword:str('#a_new')}});
    toast('账号信息已更新，请重新登录','ok');
    setTimeout(()=>location.reload(),1200);
  }catch(e){toast(e.message,'bad');}
};

/* ---------------- 开机自启动 ---------------- */
// 仅 Windows 显示开关；状态变化即时写入注册表 Run 键。
async function loadAutostart(){
  try{
    const r=await api('/api/autostart');
    if(!r.supported){ return; } // 非 Windows 隐藏卡片
    $('#autostartCard').classList.remove('hidden');
    fill('#a_autostart', r.enabled);
  }catch(e){}
}
$('#a_autostart').onchange=async()=>{
  const want=$('#a_autostart').checked;
  const err=$('#autostartErr');
  err.classList.add('hidden');
  try{
    const r=await api('/api/autostart',{method:'POST',body:{enabled:want}});
    fill('#a_autostart', r.enabled);
    toast(want?'已开启开机自启动':'已关闭开机自启动','ok');
  }catch(e){
    fill('#a_autostart', !want);
    err.textContent=e.message;
    err.classList.remove('hidden');
  }
};

/* ---------------- 图表 ---------------- */
function pushSeries(h){
  if(!h)return;
  const rx=h.rxRate||0, tx=h.txRate||0;
  // 后端采样间隔（默认 5 秒）大于 SSE 推送间隔（2 秒），会连续推重复值；
  // 去重避免曲线出现「阶梯状不动」的观感。
  const last=SERIES.length?SERIES[SERIES.length-1]:null;
  if(last && last.rx===rx && last.tx===tx)return;
  SERIES.push({t:Date.now(),rx,tx});
  if(SERIES.length>240)SERIES=SERIES.slice(-240);
  // 刷新当前页的图：dash 用实时速率，traffic 用流量趋势，都画 SERIES。
  const page=$$('section[data-view]').find(s=>!s.classList.contains('hidden'));
  if(!page)return;
  if(page.dataset.view==='dash')drawRate();
  else if(page.dataset.view==='traffic')drawTraffic();
}
function setupCanvas(cv){
  const dpr=window.devicePixelRatio||1;
  const w=cv.clientWidth||600,h=cv.clientHeight||176;
  if(cv.width!==w*dpr||cv.height!==h*dpr){cv.width=w*dpr;cv.height=h*dpr;}
  const ctx=cv.getContext('2d');
  ctx.setTransform(dpr,0,0,dpr,0,0);
  ctx.clearRect(0,0,w,h);
  return {ctx,w,h};
}
function drawSeries(cv,series,colorA,colorB,labelA,labelB){
  const {ctx,w,h}=setupCanvas(cv);
  const pad={l:8,r:8,t:12,b:20};
  const iw=w-pad.l-pad.r, ih=h-pad.t-pad.b;
  ctx.strokeStyle=cssv('--chart-grid');ctx.lineWidth=1;
  for(let i=0;i<=4;i++){
    const y=pad.t+ih*i/4;
    ctx.beginPath();ctx.moveTo(pad.l,y);ctx.lineTo(pad.l+iw,y);ctx.stroke();
  }
  if(!series.length){
    ctx.fillStyle=cssv('--chart-label');ctx.font='13px sans-serif';
    ctx.textAlign='center';ctx.fillText('正在采集数据 ...',w/2,h/2);return;
  }
  let max=0;
  series.forEach(p=>{max=Math.max(max,p.rx,p.tx);});
  if(max<=0)max=1024;
  const N=series.length;
  const px=i=>pad.l+(N<=1?iw/2:iw*i/(N-1));
  const py=v=>pad.t+ih-(v/max)*ih;
  const draw=(key,color,fill)=>{
    ctx.beginPath();
    series.forEach((p,i)=>{const x=px(i),y=py(p[key]);i?ctx.lineTo(x,y):ctx.moveTo(x,y);});
    ctx.strokeStyle=color;ctx.lineWidth=2;ctx.lineJoin='round';ctx.stroke();
    if(fill){
      ctx.lineTo(px(N-1),pad.t+ih);ctx.lineTo(px(0),pad.t+ih);ctx.closePath();
      const g=ctx.createLinearGradient(0,pad.t,0,pad.t+ih);
      g.addColorStop(0,fill);g.addColorStop(1,'rgba(0,0,0,0)');
      ctx.fillStyle=g;ctx.fill();
    }
  };
  draw('tx',colorB,cssv('--chart-fill-b'));
  draw('rx',colorA,cssv('--chart-fill-a'));
  ctx.font='11px sans-serif';ctx.textAlign='right';
  ctx.fillStyle=colorA;ctx.fillText(labelA,w-pad.r-58,pad.t+10);
  ctx.fillStyle=colorB;ctx.fillText(labelB,w-pad.r,pad.t+10);
  ctx.fillStyle=cssv('--chart-label2');ctx.textAlign='left';
  ctx.fillText('峰值 '+bytes(max)+'/s',pad.l+2,pad.t+10);
}
function drawRate(){drawSeries($('#chartRate'),SERIES,cssv('--chart-a'),cssv('--chart-b'),'下行','上行');}
function drawTraffic(){drawSeries($('#chartTraffic'),SERIES,cssv('--chart-a'),cssv('--chart-b'),'下行','上行');}
function redrawActiveChart(){
  const page=$$('section[data-view]').find(s=>!s.classList.contains('hidden'));
  if(!page)return;
  if(page.dataset.view==='dash')drawRate();
  if(page.dataset.view==='traffic')drawTraffic();
}
window.addEventListener('resize',()=>{
  const page=$$('section[data-view]').find(s=>!s.classList.contains('hidden'));
  if(!page)return;
  if(page.dataset.view==='dash')drawRate();
  if(page.dataset.view==='traffic')drawTraffic();
});

/* ---------------- 启动 ---------------- */
boot();
setInterval(()=>{
  if($('#app').style.display==='none')return;
  const page=$$('section[data-view]').find(s=>!s.classList.contains('hidden'));
  if(!page)return;
  const v=page.dataset.view;
  if(v==='dash'||v==='traffic')
    loadOverview().catch(()=>{});
  // 实例页也要实时刷新隧道状态：隧道「等待中→运行中」由内核异步推进，
  // 若不轮询，用户会一直看到旧的「等待中」，必须手动刷新网页。
  if(v==='instances'){
    loadTunnels().catch(()=>{});
    loadOverview().catch(()=>{});
  }
},8000);
document.addEventListener('keydown',e=>{if(e.key==='Escape')closeModal();});

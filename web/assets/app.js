/* ═══ Qoder-2API 管理后台前端：共享层 ═══
 * 由 shell.html 以 <script defer> 引入；页面专属动作见 assets/pages/<key>.js（PAGE 注册）。
 * 当前页 key 由服务端写入 <body data-page="…">，见 web/web.go。
 * 状态字段集中在共享层（同名成员由页面层覆盖），跨页数据加载器亦放这里。
 */

/* ═══ Lucide 图标（inline SVG path） ═══ */
const ICONS = {
  dashboard:'<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>',
  keys:'<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/>',
  token:'<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5" fill="currentColor"/>',
  box:'<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
  settings:'<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>',
  refresh:'<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  copy:'<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
  plus:'<path d="M5 12h14"/><path d="M12 5v14"/>',
  logout:'<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/>',
  sun:'<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>',
  moon:'<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>',
  monitor:'<rect width="20" height="14" x="2" y="3" rx="2"/><line x1="8" x2="16" y1="21" y2="21"/><line x1="12" x2="12" y1="17" y2="21"/>',
  menu:'<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>',
  check:'<path d="M20 6 9 17l-5-5"/>',
  alert:'<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
  activity:'<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/>',
  type:'<polyline points="4 7 4 4 20 4 20 7"/><line x1="9" x2="15" y1="20" y2="20"/><line x1="12" x2="12" y1="4" y2="20"/>',
  dollar:'<circle cx="12" cy="12" r="10"/><path d="M16 8h-6a2 2 0 1 0 0 4h4a2 2 0 1 1 0 4H8"/><path d="M12 6v2m0 8v2"/>',
  download:'<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/>',
  upload:'<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" x2="12" y1="3" y2="15"/>',
};

/* ═══ 页面 key 推导：'/admin' | '/admin/keys' | '/admin/keys/' → 'stats' | 'keys' ═══
 * 全站唯一的 key 归一规则，Go 侧（内部路由）与 JS 侧（navClick / popstate）共用同一约定：
 * 统计页是命名空间根 /admin，其余页面 key 就是路径去掉 /admin/ 前缀。
 */
function pageKey(p){
  let s = String(p||'').replace(/^\/+|\/+$/g, '');
  if(s === '' || s === 'admin') return 'stats';
  if(s.startsWith('admin/')) s = s.slice('admin/'.length);
  return s;
}

/* ═══ 页面层注册表：各页脚本调用 PAGE(id, def) 注册自己的成员 ═══ */
const PAGES = {};
function PAGE(id, def){ PAGES[id] = def; }

/* ═══ 共享层：状态 / 主题 / 图标 / 请求工具 / 登录 ═══ */
function appShell(){
  return {
    /* ── 状态（view 由各页 data-page 在总装时注入；entered/titles 同处注入） ── */
    ready:false, loggedIn:false, loading:false, password:'',
    mobileNav:false, toasts:[], _toastSeq:0,
    theme: localStorage.getItem('qoder2api:theme') || 'system',
    stats:{}, keys:[],
    keyForm:{key:'',note:''}, keyModal:false,
    pat:{masked:'',input:''},
    models:{models:[],loading:false},
    config:{}, pwForm:{current_password:'',new_password:''}, configHint:'',
    confirmBox:{open:false,title:'',message:'',detail:'',okText:'确定',danger:true,onOk:null},
    _chartGeom:null,


    /* ── 主题 ── */
    get themeLabel(){ return this.theme==='light'?'浅色':this.theme==='dark'?'深色':'系统' },
    applyTheme(){
      const dark = this.theme==='dark' || (this.theme==='system' && matchMedia('(prefers-color-scheme: dark)').matches);
      document.documentElement.classList.toggle('dark', dark);
    },
    cycleTheme(){
      this.theme = this.theme==='light'?'dark':this.theme==='dark'?'system':'light';
      localStorage.setItem('qoder2api:theme', this.theme);
      this.applyTheme();
    },

    /* ── 图标 ── */
    ic(name){
      const p = ICONS[name];
      return p ? `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">${p}</svg>` : '';
    },

    /* ── 初始化 ── */
    async init(){
      this.applyTheme();
      matchMedia('(prefers-color-scheme: dark)').addEventListener('change', ()=>{ if(this.theme==='system') this.applyTheme(); });
      let rto;
      window.addEventListener('resize', ()=>{ clearTimeout(rto); rto=setTimeout(()=>{ if(this.view==='stats') this.renderHourly(); }, 200); });
      // 统计页驻留时每 15 秒自动刷新（与旧面板的轮询节奏一致），离开页面或登出后停发；
      // 后台标签页跳过（visibilityState），回到前台时立即补一次，避免展示陈旧数据。
      setInterval(()=>{ if(this.loggedIn && this.view==='stats' && document.visibilityState==='visible') this.loadStats(); }, 15000);
      document.addEventListener('visibilitychange', ()=>{
        if(!document.hidden && this.loggedIn && this.view==='stats') this.loadStats();
      });
      try{
        const r = await this.api('/admin/api/auth');
        this.loggedIn = !!r.authenticated;
      }catch(e){ this.loggedIn = false; }
      this.ready = true;
      if(this.loggedIn) this.afterLogin();
    },

    /* ── 登录后进场：首屏页数据（走 enter，与后续切页同一入口） ── */
    afterLogin(){ this.enter(this.view); },

    /* ═══ 软导航：切 view + 换 URL，不重载文档 ═══ */

    /* 切页统一入口：首次进入加载数据；本面板各页数据均可被服务端改写
       （统计持续累计、密钥/PAT/模型可在别处变更），故全部按 realtime 处理：
       重入即刷新，两次进入间隔 <5s 时退化为保活，抑制快速切页的重复请求。 */
    enter(k){
      const p = PAGES[k];
      if(!p) return;
      if(this.entered[k]){
        if(p.realtime && Date.now() - (this.entered[k] || 0) > 5000){
          this.entered[k] = Date.now();
          if(p.load) p.load.call(this);
        }
        return;
      }
      this.entered[k] = Date.now();
      if(p.load) p.load.call(this);
    },
    applyTitle(k){
      // 页名映照惰性采集：侧栏导航文案是页名唯一来源，但导航在主界面渲染后（它在
      // template x-if 里）才进 DOM，而改标题总是发生在渲染之后。
      // 采集为空（壳层此刻不在 DOM 里，例如会话过期后触发 popstate）时**不缓存**：
      // 否则空表会被永久记住，此后标题再也不更新。
      if(!this._titles || !Object.keys(this._titles).length){
        const m = Object.fromEntries([...document.querySelectorAll('.nav-link')]
          .map(a=>[pageKey(a.getAttribute('href')), a.textContent.trim()]));
        if(Object.keys(m).length) this._titles = m;
      }
      const title = this._titles && this._titles[k];
      if(!title) return;
      document.title = document.title.replace(/( · .*)?$/, ' · ' + title);
    },
    /* 浏览器前进/后退（由 shell 的 @popstate.window 转发）：只有经 Alpine 求值，this 才是
       响应式代理；若在 app() 里直接监听并给原始对象赋值，view 变了也不会触发重渲染。 */
    onPop(e){
      const k = (e.state && e.state.view) || pageKey(location.pathname);
      if(!PAGES[k] || k === this.view) return;   // 同页返回：保留当前页（表单输入、滚动位置都在）
      this.mobileNav = false;                    // 换页了：收起移动端侧栏（与 navigate 一致）
      this.view = k;
      this.applyTitle(k);
      this.enter(k);
      // 必须等 Alpine 把新 view 显示出来再滚：否则此刻文档还停在上一页的高度，
      // 滚动位置会被浏览器夹到 0（x-show 的显隐是在下一微任务里生效的）。
      const y = (e.state && e.state.scroll) || 0;
      this.$nextTick(()=>requestAnimationFrame(()=>window.scrollTo(0, y)));
    },

    navigate(k){
      if(!PAGES[k] || k === this.view) return;
      this.mobileNav = false; // 移动端点导航后必须收起侧栏，否则遮罩一直盖着内容
      history.replaceState(Object.assign({}, history.state, {scroll: window.scrollY}), '');
      this.view = k;
      history.pushState({view:k}, '', k === 'stats' ? '/admin' : '/admin/' + k);
      this.applyTitle(k);
      window.scrollTo(0, 0);
      this.enter(k);
    },
    /* 侧边栏 <a href> 的拦截层：href 是唯一真相，未知目标一律交回浏览器（404 语义不变） */
    navClick(e){
      if(e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return; // 新标签/新窗口：交给浏览器
      const k = pageKey(e.currentTarget.getAttribute('href'));
      if(!PAGES[k]) return;
      e.preventDefault();
      this.navigate(k);
    },

    /* ── API / 基础工具 ── */
    async api(path, opts={}){
      opts.headers = Object.assign({'Content-Type':'application/json'}, opts.headers||{});
      const r = await fetch(path, opts);
      // 登录端点自身的 401 是「密码错误」，不是会话过期：交下方按 data.error 报出服务端原文。
      // 若一并当作会话过期，用户看到的是「登录已过期」，而服务端返回的「密码错误」被吞掉。
      if(r.status===401 && path!=='/admin/api/login'){ this.loggedIn=false; this.resetSession(); throw new Error('登录已过期'); }
      const data = await r.json().catch(()=>({}));
      if(!r.ok) throw new Error(data.error||('HTTP '+r.status));
      return data;
    },
    /* 会话边界：清空「已进场」缓存。401 或退出后再登录属新会话，数据已失效；
       若不清理，afterLogin() → enter(view) 会被 entered 挡下，当前页保留过期前的旧统计 / 旧 Key 列表。 */
    resetSession(){ this.entered = {}; },
    toast(msg, type=''){
      const id = ++this._toastSeq;
      this.toasts.push({id,msg,type});
      setTimeout(()=>{ this.toasts = this.toasts.filter(t=>t.id!==id); }, 3400);
    },
    busy(fn){
      return async (...a)=>{ this.loading=true; try{ return await fn(...a); }
        catch(e){ this.toast(e.message,'err'); }
        finally{ this.loading=false; } };
    },
    /**
     * 通用确认弹窗。
     * opts.detail  次要说明（灰字，排在正文下方）
     * opts.okText  主按钮文案，默认「确定」
     * opts.danger  是否危险操作（默认 true，走 btn-danger；非破坏性操作传 false 走主色）
     */
    askConfirm(title, message, fn, opts){
      const o = opts || {};
      this.confirmBox = {
        open:true, title, message,
        detail:o.detail||'', okText:o.okText||'确定', danger:o.danger!==false,
        onOk:()=>{ this.confirmBox.open=false; fn(); },
      };
    },
    copy(text){
      // navigator.clipboard 仅在安全上下文（HTTPS / localhost）存在：非安全部署下
      // 访问它会同步抛 TypeError，须判空 + catch，否则复制按钮静默失败。
      if(!navigator.clipboard || !navigator.clipboard.writeText){
        this.toast('当前环境不支持剪贴板复制','err');
        return;
      }
      navigator.clipboard.writeText(text)
        .then(()=>this.toast('已复制到剪贴板','ok'))
        .catch(()=>this.toast('复制失败','err'));
    },
    fmt(n){ return (n||0).toLocaleString(); },
    compact(n){
      n = n||0;
      if(n>=1e9) return (n/1e9).toFixed(1).replace(/\.0$/,'')+'B';
      if(n>=1e6) return (n/1e6).toFixed(1).replace(/\.0$/,'')+'M';
      if(n>=1e3) return (n/1e3).toFixed(1).replace(/\.0$/,'')+'K';
      return String(n);
    },
    ts(unix, withSec){
      if(!unix) return '—';
      const d = typeof unix==='number' ? new Date(unix*1000) : new Date(unix);
      if(isNaN(d.getTime())) return String(unix);
      const p = n=>String(n).padStart(2,'0');
      let s = d.getFullYear()+'-'+p(d.getMonth()+1)+'-'+p(d.getDate())+' '+p(d.getHours())+':'+p(d.getMinutes());
      if(withSec) s += ':'+p(d.getSeconds());
      return s;
    },
    dateStr(unix){
      if(!unix) return '—';
      return new Date(unix*1000).toLocaleDateString('zh-CN');
    },
    pctStr(rate){
      if(rate === null || rate === undefined || isNaN(rate)) return '—';
      return (rate * 100).toFixed(1) + '%';
    },

    /* ── 登录 ── */
    async doLogin(){
      if(!this.password){ this.toast('请输入密码','err'); return; }
      this.loading = true;
      try{
        await this.api('/admin/api/login',{method:'POST',body:JSON.stringify({password:this.password})});
        this.loggedIn = true; this.password = '';
        this.toast('登录成功','ok');
        this.afterLogin();
      }catch(e){ this.toast(e.message,'err'); }
      this.loading = false;
    },
    async logout(){
      try{ await this.api('/admin/api/logout',{method:'POST'}); }catch(e){}
      this.loggedIn = false;
      this.resetSession();
      location.href = '/admin';
    },
  };
}

/* ═══ 总装：共享层 + 当前页层（同名成员以页面层为准） ═══ */
/* 页面 key 来自服务端渲染的 <body data-page>；缺失时回退首页。 */
function app(){
  const key = pageKey(document.body.dataset.page);
  const page = PAGES[key];
  if(!page) console.warn("未注册的页面："+key);
  const inst = appShell();
  // 合并全部页面层（不只当前页）：5 个 view 常驻同一文档，Alpine 会求值全部页面的
  // 指令，而它始终在 x-data 根作用域（即 inst）上求值 —— 只合并当前页会让其他页的
  // 表达式报 ReferenceError。同名成员以后注册者胜出（PAGES 键序即文件拼接序）。
  // 用属性描述符而非 Object.assign：页面层的 getter 必须保持 getter（Object.assign
  // 会立即求值、且以页面对象为 this，那里没有 stats 等状态）。
  for(const id of Object.keys(PAGES)){
    for(const k of Object.keys(PAGES[id])){
      Object.defineProperty(inst, k, Object.getOwnPropertyDescriptor(PAGES[id], k));
    }
  }
  // view 必须在 Alpine 首次求值前就绪：它决定哪个 .view 显示（错的第一帧会闪出其余页面）。
  // entered 必须留空：afterLogin() 走的就是 enter(this.view)，预置首屏 key 会让首屏页的
  // load() 被直接跳过，且此后切回该页也不再加载（entered 已为真）——首屏永远是空态。
  // （resetSession() 同样清空 entered。）
  inst.view = key;
  inst.entered = {};
  return inst;
}

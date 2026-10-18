/* ═══ 页面层：账号（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('token', {
  realtime: true,

  /* ── 账号池 ── */
  async loadAccounts(){
    try{ const r = await this.api('/admin/api/accounts'); this.accounts = r.accounts||[]; }
    catch(e){ this.toast(e.message,'err'); }  // 与 loadStats/loadKeys 一致：加载失败必须有提示
  },
  /* 添加：服务端对相同 PAT 幂等（视为切换），此处不做本地去重判断 */
  async createAccount(){
    const pat = (this.accountForm.pat||'').trim();
    if(!pat){ this.toast('令牌不能为空','err'); return; }
    this.busy(async()=>{
      await this.api('/admin/api/accounts',{method:'POST',body:JSON.stringify({
        pat, note:(this.accountForm.note||'').trim(), region:(this.accountForm.region||'cn') })});
      this.toast('账号已添加','ok');
      this.accountModal = false;
      this.accountForm = {pat:'',note:'',region:'cn'};
      this.loadAccounts();
    })();
  },
  deleteAccount(acc){
    this.askConfirm('删除账号','确定删除此账号？删除后不可恢复。', ()=>{
      this.busy(async()=>{
        await this.api('/admin/api/accounts?id='+encodeURIComponent(acc.id),{method:'DELETE'});
        this.toast('账号已删除','ok'); this.loadAccounts();
      })();
    });
  },
  editAccountNote(acc){
    this.noteForm = {id:acc.id, note:acc.note||''};
    this.noteModal = true;
  },
  async saveAccountNote(){
    this.busy(async()=>{
      await this.api('/admin/api/accounts/note',{method:'POST',body:JSON.stringify({
        id:this.noteForm.id, note:(this.noteForm.note||'').trim() })});
      this.toast('备注已更新','ok');
      this.noteModal = false;
      this.loadAccounts();
    })();
  },
  /* 权重：表格内联编辑，失焦/回车即保存（1..100）；失败时回滚到原值 */
  async setAccountWeight(acc, ev){
    const el = ev.target;
    const w = Number(el.value);
    const prev = acc.weight || 1;
    if(!Number.isFinite(w) || w < 1 || w > 100){
      this.toast('权重需在 1..100 之间','err');
      el.value = prev;
      return;
    }
    if(w === prev) return;
    try{
      await this.api('/admin/api/accounts/weight',{method:'POST',body:JSON.stringify({id:acc.id, weight:w})});
      acc.weight = w;
      this.toast('权重已更新','ok');
    }catch(e){
      el.value = prev;
      this.toast(e.message,'err');
    }
  },
  /* 检测：经临时会话查询一次真实额度并落快照；每次检测都是一次完整会话引导，勿高频点击 */
  async checkAccount(acc){
    this.checkingId = acc.id;
    try{
      await this.api('/admin/api/accounts/check',{method:'POST',body:JSON.stringify({id:acc.id})});
      this.toast('检测完成','ok');
      this.loadAccounts();
    }catch(e){ this.toast(e.message,'err'); this.loadAccounts(); }
    finally{ this.checkingId = ''; }
  },

  /* ── 表格展示 ── */
  /* 剩余额度：快照存在才展示（checked_at 为零说明从未检测过） */
  accountQuota(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at || s.last_error) return '—';
    return this.fmtCredits(s.remaining) + ' / ' + this.fmtCredits(s.total);
  },
  /* 区域标签：账号所属的 Qoder 部署（国内 / 国际） */
  accountRegion(acc){
    return acc.region === 'global' ? '国际版' : '国内版';
  },
  /* 状态：按实时在途判断账号是否正在服务请求（而非“默认账号”标记）。
   * 多个对话并发时，会有多个账号同时显示「使用中」。 */
  accountStatus(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at) return '未检测';
    if(s.last_error) return '检测失败';
    if(s.is_exceeded) return '已耗尽';
    if((acc.in_flight||0) > 0) return '使用中';
    return '空闲';
  },
  accountStatusClass(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at) return 'badge-muted';
    if(s.last_error) return 'badge-err';
    if(s.is_exceeded) return 'badge-warn';
    if((acc.in_flight||0) > 0) return 'badge-ok';
    return 'badge-muted';
  },

  /* ── 自动切换设置 ── */
  async loadAutoSwitch(){
    try{
      const r = await this.api('/admin/api/accounts/auto');
      this.autoSwitch = {
        enabled: !!r.enabled,
        threshold: r.threshold || 20,
        strategy: r.strategy || 'sticky',
        sticky_limit: r.sticky_limit || 30,
        max_retry_accounts: r.max_retry_accounts || 3,
      };
    }catch(e){ this.toast(e.message,'err'); }
  },
  async saveAutoSwitch(){
    const as = this.autoSwitch;
    const t = Number(as.threshold);
    if(!Number.isFinite(t) || t < 1){ this.toast('阈值需不小于 1 积分','err'); return; }
    const sticky = Number(as.sticky_limit);
    if(!Number.isFinite(sticky) || sticky < 1){ this.toast('粘性次数需不小于 1','err'); return; }
    const retry = Number(as.max_retry_accounts);
    if(!Number.isFinite(retry) || retry < 1 || retry > 16){ this.toast('最大重试账号数需在 1..16 之间','err'); return; }
    this.busy(async()=>{
      await this.api('/admin/api/accounts/auto',{method:'POST',body:JSON.stringify({
        enabled: !!as.enabled, threshold: t, strategy: as.strategy,
        sticky_limit: sticky, max_retry_accounts: retry })});
      this.toast('负载均衡设置已保存','ok');
    })();
  },

  /* ── 进场：本页所需数据（账号等级徽标取自 stats 快照，故一并拉统计；本页不驻留，
   * 不参与统计页的定时刷新） ── */
  load(){ this.loadAccounts(); this.loadAutoSwitch(); this.loadStats(); },
});

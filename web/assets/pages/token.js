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
        pat, note:(this.accountForm.note||'').trim(), region:(this.accountForm.region||'').trim() })});
      this.toast('账号已添加','ok');
      this.accountModal = false;
      this.accountForm = {pat:'',note:'',region:''};
      this.loadAccounts(); this.loadPAT();
    })();
  },
  switchAccount(acc){
    this.askConfirm('切换账号','切换后 API 桥接将使用该账号的令牌，桥接会话在下次请求时自动重建。', ()=>{
      this.busy(async()=>{
        await this.api('/admin/api/accounts/switch',{method:'POST',body:JSON.stringify({id:acc.id})});
        this.toast('已切换到 '+(acc.note||acc.pat_masked),'ok');
        this.loadAccounts(); this.loadPAT();
      })();
    }, {okText:'切换', danger:false});
  },
  deleteAccount(acc){
    this.askConfirm('删除账号','确定删除此账号？激活中的账号需先切换到其他账号后才能删除。', ()=>{
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
      this.toast('备注已保存','ok');
      this.noteModal = false;
      this.loadAccounts();
    })();
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

  /* 逐个检测全部账号（串行，避免同时引导多个会话触发上游风控） */
  async checkAllAccounts(){
    if(this.bulkChecking) return;
    this.bulkChecking = true;
    let ok = 0, fail = 0;
    try{
      for(const acc of (this.accounts||[])){
        try{
          await this.api('/admin/api/accounts/check',{method:'POST',body:JSON.stringify({id:acc.id})});
          ok++;
        }catch(e){ fail++; }
        this.loadAccounts();
      }
      this.toast(`全部检测完成：成功 ${ok}，失败 ${fail}`, fail ? 'err' : 'ok');
    } finally { this.bulkChecking = false; this.loadAccounts(); }
  },

  /* ── 区域切换：两区 PAT 不通用，逐账号指定上游区域 ── */
  toggleAccountRegion(acc){
    const to = acc.region === 'cn' ? 'intl' : 'cn';
    const label = to === 'cn' ? '国内区（qoder.com.cn）' : '国际区（qoder.sh）';
    this.askConfirm('切换账号区域', '将「'+(acc.note||acc.pat_masked)+'」的上游区域切换为 '+label+'。注意：两区 PAT 不通用，切错区域会导致检测失败。', ()=>{
      this.busy(async()=>{
        await this.api('/admin/api/accounts/region',{method:'POST',body:JSON.stringify({
          id: acc.id, region: to })});
        this.toast('区域已切换为 '+label,'ok');
        this.loadAccounts();
      })();
    }, {okText:'切换区域', danger:false});
  },

  /* ── 启用/停用：停用的账号不参与负载均衡，也不是自动切换候选 ── */
  toggleAccountEnabled(acc){
    const disabling = !acc.disabled;
    const doIt = ()=>this.busy(async()=>{
      await this.api('/admin/api/accounts/enabled',{method:'POST',body:JSON.stringify({
        id: acc.id, disabled: disabling })});
      this.toast(disabling ? '账号已停用' : '账号已启用','ok');
      this.loadAccounts();
    })();
    if(disabling && acc.active){
      this.askConfirm('停用主账号','该账号当前是主账号。停用后负载均衡会跳过它；若负载均衡未开启，API 仍会使用该账号，建议先切换到其他账号。', doIt, {okText:'仍要停用', danger:false});
    } else doIt();
  },

  /* ── 表格展示 ── */
  /* 剩余额度：快照存在才展示（checked_at 为零说明从未检测过） */
  accountQuota(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at || s.last_error) return '—';
    return this.fmtCredits(s.remaining) + ' / ' + this.fmtCredits(s.total);
  },
  accountStatus(acc){
    if(acc.disabled) return '已停用';
    const s = acc.snapshot;
    if(!s || !s.checked_at) return '未检测';
    if(s.last_error) return '检测失败';
    if(s.is_exceeded) return '已耗尽';
    if(acc.active) return this.loadBalance.enabled ? '主账号' : '使用中';
    return this.loadBalance.enabled ? '分流中' : '正常';
  },
  accountStatusClass(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at) return 'badge-muted';
    if(s.last_error) return 'badge-err';
    if(s.is_exceeded) return 'badge-warn';
    return 'badge-ok';
  },

  /* ── PAT（旧字段保留：面板底部不再单独展示，数据仍由激活账号镜像） ── */
  async loadPAT(){
    try{
      const r = await this.api('/admin/api/pat');
      this.pat.masked = r.pat_masked || '';
    }catch(e){ this.toast(e.message,'err'); }
  },

  /* ── 自动切换设置 ── */
  async loadAutoSwitch(){
    try{
      const r = await this.api('/admin/api/accounts/auto');
      this.autoSwitch = {enabled: !!r.enabled, threshold: r.threshold || 20};
    }catch(e){ this.toast(e.message,'err'); }
  },
  async saveAutoSwitch(){
    const as = this.autoSwitch;
    const t = Number(as.threshold);
    if(!Number.isFinite(t) || t < 1){ this.toast('阈值需不小于 1 积分','err'); return; }
    this.busy(async()=>{
      await this.api('/admin/api/accounts/auto',{method:'POST',body:JSON.stringify({
        enabled: !!as.enabled, threshold: t })});
      this.toast('自动切换设置已保存','ok');
    })();
  },

  /* ── 负载均衡 ── */
  async loadLoadBalance(){
    try{
      const r = await this.api('/admin/api/loadbalance');
      this.loadBalance = {enabled: !!r.enabled, threshold: (r.threshold ?? 5), max_concurrency: r.max_concurrency || 0};
    }catch(e){ this.toast(e.message,'err'); }
  },
  async saveLoadBalance(){
    const lb = this.loadBalance;
    const t = Number(lb.threshold);
    if(!Number.isFinite(t) || t < 0){ this.toast('阈值不能为负数','err'); return; }
    const mc = Number(lb.max_concurrency);
    if(!Number.isFinite(mc) || mc < 1 || mc > 32){ this.toast('单账号并发上限需在 1-32 之间','err'); return; }
    this.busy(async()=>{
      await this.api('/admin/api/loadbalance',{method:'POST',body:JSON.stringify({
        enabled: !!lb.enabled, threshold: t, max_concurrency: mc })});
      this.toast('负载均衡设置已保存','ok');
    })();
  },

  /* ── 进场：本页所需数据（账号等级徽标取自 stats 快照，故一并拉统计；本页不驻留，
   * 不参与统计页的定时刷新） ── */
  load(){ this.loadAccounts(); this.loadAutoSwitch(); this.loadLoadBalance(); this.loadPAT(); this.loadStats(); },
});

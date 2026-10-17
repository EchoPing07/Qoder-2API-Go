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
        pat, note:(this.accountForm.note||'').trim() })});
      this.toast('账号已添加','ok');
      this.accountModal = false;
      this.accountForm = {pat:'',note:''};
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

  /* ── 表格展示 ── */
  /* 剩余额度：快照存在才展示（checked_at 为零说明从未检测过） */
  accountQuota(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at || s.last_error) return '—';
    return this.fmtCredits(s.remaining) + ' / ' + this.fmtCredits(s.total);
  },
  accountStatus(acc){
    const s = acc.snapshot;
    if(!s || !s.checked_at) return '未检测';
    if(s.last_error) return '检测失败';
    if(s.is_exceeded) return '已耗尽';
    if(acc.active) return '使用中';
    return '正常';
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

  /* ── 进场：本页所需数据（账号等级徽标取自 stats 快照，故一并拉统计；本页不驻留，
   * 不参与统计页的定时刷新） ── */
  load(){ this.loadAccounts(); this.loadPAT(); this.loadStats(); },
});

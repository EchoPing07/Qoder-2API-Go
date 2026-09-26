/* ═══ 页面层：令牌（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('token', {
  realtime: true,

  /* ── PAT ── */
  // 服务端只回掩码形式；明文 PAT 永不出服务端，编辑以整值提交、从不就地改写。
  async loadPAT(){
    try{
      const r = await this.api('/admin/api/pat');
      this.pat.masked = r.pat_masked || '';
    }catch(e){ this.toast(e.message,'err'); }  // 与 loadStats/loadModels 一致：加载失败必须有提示
  },
  async savePAT(){
    const v = (this.pat.input||'').trim();
    if(!v){ this.toast('PAT 不能为空','err'); return; }
    this.busy(async()=>{
      await this.api('/admin/api/pat',{method:'POST',body:JSON.stringify({pat:v})});
      this.toast('令牌已保存','ok');
      this.pat.input = '';
      this.loadPAT();
    })();
  },

  /* ── 进场：本页所需数据 ── */
  load(){ this.loadPAT(); },
});

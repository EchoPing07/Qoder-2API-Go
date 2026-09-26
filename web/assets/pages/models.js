/* ═══ 页面层：模型（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('models', {
  realtime: true,

  /* ── 模型 ── */
  async loadModels(){
    this.models.loading = true;
    try{
      const r = await this.api('/admin/api/models');
      this.models.models = r.models||[];
    }catch(e){ this.toast(e.message,'err'); }
    finally{ this.models.loading = false; }
  },

  /* ── 进场：本页所需数据 ── */
  load(){ this.loadModels(); },
});

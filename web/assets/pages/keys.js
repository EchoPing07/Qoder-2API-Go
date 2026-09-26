/* ═══ 页面层：密钥（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('keys', {
  realtime: true,

  /* ── Keys ── */
  async loadKeys(){
    try{ const r = await this.api('/admin/api/keys'); this.keys = r.keys||[]; }catch(e){}
  },
  /* 创建：留空 key 由服务端随机生成；「随机生成」与「自定义」共用此入口。 */
  async createKey(){
    this.busy(async()=>{
      const f = this.keyForm;
      await this.api('/admin/api/keys',{method:'POST',body:JSON.stringify({
        key:(f.key||'').trim(), note:(f.note||'').trim() })});
      this.toast('密钥已创建','ok');
      this.keyModal = false;
      this.keyForm = {key:'',note:''};
      this.loadKeys();
    })();
  },
  deleteKey(k){
    this.askConfirm('删除密钥','确定删除此密钥？此操作不可撤销。', ()=>{
      this.busy(async()=>{
        await this.api('/admin/api/keys?id='+encodeURIComponent(k.id),{method:'DELETE'});
        this.toast('密钥已删除','ok'); this.loadKeys();
      })();
    });
  },

  /* ── 进场：本页所需数据 ── */
  load(){ this.loadKeys(); },
});

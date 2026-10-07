/* ═══ 页面层：密钥（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('keys', {
  realtime: true,

  /* ── 调用卡片 ──
   * Base URL 由浏览器当前来源（location.origin）推导：用户可能用任意 IP / 端口部署，
   * 写死 localhost 的示例无法直接复制。面板与 API 同源；经反向代理改变对外域名时
   * 以代理暴露的地址为准（卡片内亦有提示）。
   */
  get callBase(){ return location.origin + '/v1'; },
  get callChat(){ return this.callBase + '/chat/completions'; },
  get callModels(){ return this.callBase + '/models'; },
  /* 示例中的密钥用占位符：真实密钥在上方列表里，避免截图 / 录屏连带泄露 */
  get callCurl(){
    return `curl ${this.callChat} \\
  -H "Authorization: Bearer sk-xxxxxxxxxxxxxxxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"Qwen3.8-Flash","messages":[{"role":"user","content":"你好"}],"stream":true}'`;
  },

  /* ── Keys ── */
  async loadKeys(){
    try{ const r = await this.api('/admin/api/keys'); this.keys = r.keys||[]; }
    catch(e){ this.toast(e.message,'err'); }  // 与 loadStats/loadModels 一致：加载失败必须有提示，否则用户对着陈旧列表无感知
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

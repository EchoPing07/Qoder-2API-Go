/* ═══ 页面层：设置（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('settings', {
  realtime: true,

  /* ── 服务器 / 超时配置 ── */
  async loadConfig(){
    try{
      const r = await this.api('/admin/api/config');
      this.config = {
        host: r.host || '',
        port: r.port || '',
        chat_timeout_seconds: r.chat_timeout_seconds || '',
        idle_timeout_seconds: r.idle_timeout_seconds || '',
      };
    }catch(e){ this.toast(e.message,'err'); }  // 与 loadStats/loadModels 一致：加载失败必须有提示
  },
  /* 保存：host 留空回落默认 0.0.0.0；port 必填且须在 1-65535（空/非法均被拒，
   * 不会静默回落）；超时字段留空 = 不下发（保持服务端当前值）；数值输入被清空时
   * x-model.number 得到 null，须按「留空」处理，不能当 0 发出去（0 会被服务端当作
   * 「未携带」之外的非法值路径）。 */
  async saveConfig(){
    this.busy(async()=>{
      const c = this.config||{};
      let host = String(c.host==null?'':c.host).trim();
      const port = parseInt(c.port, 10);
      if(!host) host = '0.0.0.0';
      if(!port || port < 1 || port > 65535){ this.toast('端口无效','err'); return; }
      const body = { host: host, port: port };
      const ctRaw = c.chat_timeout_seconds;
      if(ctRaw !== null && ctRaw !== undefined && String(ctRaw).trim() !== ''){
        const ct = parseInt(ctRaw, 10);
        if(!ct || ct < 1 || ct > 3600){ this.toast('Chat 响应超时需在 1-3600 秒之间','err'); return; }
        body.chat_timeout_seconds = ct;
      }
      const itRaw = c.idle_timeout_seconds;
      if(itRaw !== null && itRaw !== undefined && String(itRaw).trim() !== ''){
        const it = parseInt(itRaw, 10);
        if(!it || it < 1 || it > 3600){ this.toast('流空闲超时需在 1-3600 秒之间','err'); return; }
        body.idle_timeout_seconds = it;
      }
      const r = await this.api('/admin/api/config',{method:'POST',body:JSON.stringify(body)});
      this.toast('配置已保存（超时配置即时生效）');
      this.loadConfig();
      this.configHint = r.restart || '修改主机或端口后需要重启服务才能生效。';
    })();
  },

  /* ── 修改管理密码 ── */
  async savePassword(){
    const f = this.pwForm;
    if(!f.current_password || !f.new_password){ this.toast('请填写完整','err'); return; }
    this.busy(async()=>{
      await this.api('/admin/api/password',{method:'POST',body:JSON.stringify({
        current_password:f.current_password, new_password:f.new_password })});
      this.toast('密码已修改','ok');
      this.pwForm = {current_password:'',new_password:''};
    })();
  },

  /* ── 进场：本页所需数据 ──（进场时清空上次的保存提示，与旧面板一致） */
  load(){ this.loadConfig(); this.configHint = ''; },
});

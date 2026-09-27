/* ═══ 页面层：统计（PAGE(key, def) 注册本页专属状态与动作） ═══ */
PAGE('stats', {
  realtime: true,

  /* ── 派生指标 ── */
  get totalTokens(){
    const s = this.stats||{};
    return (s.prompt_tokens||0) + (s.completion_tokens||0);
  },
  get avgOut(){
    const s = this.stats||{};
    return s.total > 0 ? Math.round((s.completion_tokens||0)/s.total) : 0;
  },

  /* ── Credits 消耗统计 ──
   * 本服务实际扣费：usage 帧 credits 累加（billable=false 的帧既不计费也不计数）。
   * 官方额度池是账号级数字，含 IDE/CLI 等其它客户端消耗，回答不了「本服务花了多少」。
   */
  get totalCredits(){
    return Number((this.stats||{}).credits || 0);
  },
  get cycleCredits(){
    return Number((this.stats||{}).cycle_credits || 0);
  },
  get billedCount(){
    return Number((this.stats||{}).billed_requests || 0);
  },
  /* 平均每次计费请求的额度消耗；无计费请求时返回 0（不显示误导性的均值） */
  get avgCredits(){
    const avg = Number((this.stats||{}).avg_credits || 0);
    return this.billedCount > 0 ? avg : 0;
  },
  /* 卡片主体数字：已同步订阅周期时看本周期消耗，否则看本服务累计。周期消耗可以为
   * 0（新周期刚开始），故不能写作 `cycle>0 ? cycle : lifetime`，否则会让人误以为周期
   * 重置没有生效。
   */
  get shownCredits(){
    return this.hasCycle ? this.cycleCredits : this.totalCredits;
  },
  get hasCycle(){
    return Number((this.stats||{}).next_reset_ms || 0) > 0;
  },
  /* 卡片副行：只陈述与主体数字同一口径的事实，不混入另一个范围的计数 */
  get creditsDetail(){
    if (this.hasCycle) return '本周期 · 累计 ' + this.fmtCredits(this.totalCredits);
    if (this.billedCount === 0) {
      // 升级前的数据只有累计 credits、没有计费次数：说明口径，而不是报「暂无计费
      // 请求」——那会和卡片主体上非零的数字自相矛盾。
      return this.totalCredits > 0 ? '计费次数与均值自本版起统计' : '暂无计费请求';
    }
    return '计费请求 ' + this.fmt(this.billedCount) + ' 次 · 平均 ' + this.fmtCredits(this.avgCredits);
  },

  /* ── 额度池 ──
   * Collect the allowance pools the account actually holds. A pool with a zero
   * total carries no allowance at all: the free tier reports userQuota.total=0
   * while the real budget sits in addOnQuota. Treating it as absent (instead of
   * as an exhausted pool) is what keeps the panel from rendering "8 / 0 · 剩 0"
   * plus a bogus out-of-quota verdict for an account that still has credits.
   */
  get quotaPoolsList(){
    const acct = (this.stats||{}).account || {};
    const pools = [];
    if (acct.user_quota && Number(acct.user_quota.total || 0) > 0) {
      pools.push(this.decoratePool({ name: '套餐额度', quota: acct.user_quota, totalKey: 'total', available: true }));
    }
    if (acct.add_on_quota && Number(acct.add_on_quota.total || 0) > 0) {
      pools.push(this.decoratePool({ name: '加购额度', quota: acct.add_on_quota, totalKey: 'total', available: true }));
    }
    if (acct.org_resource_package && Number(acct.org_resource_package.cap || 0) > 0) {
      pools.push(this.decoratePool({
        name: '组织资源包',
        quota: acct.org_resource_package,
        totalKey: 'cap',
        available: Boolean(acct.org_resource_package.available),
        checkAvailability: true
      }));
    }
    return pools;
  },
  /* 单池的展示字段：用量、比率与状态徽标。 */
  decoratePool(pool){
    const used = Number(pool.quota.used || 0);
    const total = Number(pool.quota[pool.totalKey] || 0);
    const remaining = Number(pool.quota.remaining || 0);
    const ratio = total > 0 ? Math.min(Math.max(used / total, 0), 1) : 0;
    const statusText = pool.available ? (remaining > 0 ? '可用' : '已用尽') : '不可用';
    const statusKind = pool.available && remaining > 0 ? 'badge-ok' : (pool.available ? 'badge-err' : 'badge-muted');
    return Object.assign(pool, { used, total, remaining, ratio, statusText, statusKind });
  },
  /* 合计剩余只累计可花费的额度：不可用池仍有明细行（标 不可用），
   * 但把它的剩余加进合计会与该行自相矛盾。 */
  get quotaSummary(){
    let totalRemaining = 0;
    this.quotaPoolsList.forEach(function(pool){
      if (pool.available) totalRemaining += pool.remaining;
    });
    const resetMs = Number((this.stats||{}).next_reset_ms || 0);
    return '合计剩余 ' + this.fmtCredits(totalRemaining) +
      (resetMs > 0 ? ' · ' + this.fmtMonthDay(resetMs) + ' 重置' : '');
  },

  /* ── Credits 概要行（Token 卡片副行） ──
   * Prefer the authoritative cycle allowance used by Qoder's official /usage
   * view. Local cycle_credits remains a fallback for temporary OpenAPI outages;
   * it only covers traffic observed by this bridge and is therefore never mixed
   * with the account-wide official value.
   */
  get creditsHeadline(){
    const d = this.stats||{};
    const resetMs = Number(d.next_reset_ms || 0);
    const pools = this.quotaPoolsList;
    let text;
    if (pools.length > 0) {
      // The headline describes the pool that actually carries the allowance.
      // Naming it explicitly keeps a free account (whose budget lives in the
      // add-on pool) from reading as the plan pool being exhausted.
      const primary = pools[0];
      text = primary.name + ' ' + this.fmtCredits(primary.used) + ' / ' + this.fmtCredits(primary.total) +
        ' · 剩 ' + this.fmtCredits(primary.remaining);
    } else if (resetMs > 0) {
      text = '本服务周期内 ' + this.fmtCredits(this.cycleCredits);
    } else {
      text = '本服务累计 ' + this.fmtCredits(this.totalCredits);
    }
    if (resetMs > 0) {
      const left = Math.ceil((resetMs - Date.now()) / 86400000);
      text += ' · ' + this.fmtMonthDay(resetMs) + ' 重置';
      if (left > 0) text += ' · 剩 ' + left + ' 天';
    }
    return text;
  },
  /* 完整分池明细（悬停 tooltip）：先给本服务自算的消耗口径，再逐池列出官方额度。 */
  get creditsTitle(){
    const d = this.stats||{};
    const resetMs = Number(d.next_reset_ms || 0);
    const self = this;
    const title = ['本服务累计实际消耗 ' + this.fmtCredits(this.totalCredits) +
      '（计费请求 ' + this.fmt(this.billedCount) + ' 次 · 平均 ' + this.fmtCredits(this.avgCredits) + ' / 次）'];
    if (resetMs > 0) {
      title.push('本服务本周期消耗 ' + this.fmtCredits(this.cycleCredits) +
        (this.cycleStartLabel ? '（周期自 ' + this.cycleStartLabel + '）' : ''));
    }
    const pools = this.quotaPoolsList;
    if (pools.length > 0) {
      title.push('');
      title.push('官方账号级额度（含 IDE / CLI 等其它客户端）：');
      pools.forEach(function(pool){
        let line = pool.name + '：已用 ' + self.fmtCredits(pool.used) +
          ' / ' + self.fmtCredits(pool.total) +
          '，剩余 ' + self.fmtCredits(pool.remaining);
        if (pool.checkAvailability) {
          line += pool.available ? '（可用）' : '（不可用）';
        }
        title.push(line);
      });
    } else if (resetMs > 0) {
      title.push('官方额度暂不可用；上方数字仅统计本服务观察到的请求');
    }
    return title.join('\n');
  },
  /* 周期起点（MM-DD），未同步到订阅边界时为空串 */
  get cycleStartLabel(){
    const ms = Number((this.stats||{}).cycle_start_ms || 0);
    return ms > 0 ? this.fmtMonthDay(ms) : '';
  },
  get accountExceeded(){
    const acct = (this.stats||{}).account;
    return !!(acct && acct.is_quota_exceeded);
  },
  /* 账号等级徽标（Free / Teams…）：渲染在「令牌」页，数据仍取自本统计快照 */
  get accountTag(){
    const acct = (this.stats||{}).account;
    return (acct && acct.tag) ? acct.tag : '';
  },

  async loadStats(){
    try{
      this.stats = await this.api('/admin/api/stats');
      this.$nextTick(()=>requestAnimationFrame(()=>this.renderHourly()));
    }catch(e){ this.toast(e.message,'err'); }
  },

  /* ── 图表：近 24 小时请求量（手写 SVG，堆叠柱：下=成功，上=失败） ── */
  renderHourly(){
    const el = document.getElementById('chartHourly');
    if(!el) return;
    const hourly = (this.stats||{}).hourly || [];
    const max = hourly.reduce((a,h)=>Math.max(a,h.total||0),0);
    if(!hourly.length || !max){ el.innerHTML = '<div class="chart-empty">暂无数据</div>'; return; }
    const W = el.clientWidth || 600;
    if(W < 40) return;
    const H = 236, padL = 34, padR = 10, padT = 12, padB = 24;
    const iw = W-padL-padR, ih = H-padT-padB;
    const step = iw/hourly.length;
    const barW = Math.max(4, Math.min(16, step*0.48));
    const cx = i => padL + step*i + step/2;
    const y = v => padT + ih - v/max*ih;
    let s = '';
    for(let g=0; g<=4; g++){
      const gy = padT + ih - ih*g/4;
      s += `<line class="grid-line" x1="${padL}" x2="${W-padR}" y1="${gy.toFixed(1)}" y2="${gy.toFixed(1)}"/>`;
      s += `<text class="axis-label" text-anchor="end" x="${padL-7}" y="${(gy+3.5).toFixed(1)}">${this.compact(Math.round(max*g/4))}</text>`;
    }
    hourly.forEach((h,i)=>{
      const okH = (h.success||0)/max*ih;
      const failH = (h.failed||0)/max*ih;
      if(okH > 0){
        s += `<rect class="bar-c" data-i="${i}" x="${(cx(i)-barW/2).toFixed(1)}" y="${y(h.success||0).toFixed(1)}" width="${barW.toFixed(1)}" height="${okH.toFixed(1)}" rx="${Math.min(3,barW/2).toFixed(1)}"/>`;
      }
      if(failH > 0){
        // 失败段叠在成功段上方，走错误色：两段颜色与图例一一对应
        s += `<rect class="bar-fail" data-i="${i}" x="${(cx(i)-barW/2).toFixed(1)}" y="${y(h.total||0).toFixed(1)}" width="${barW.toFixed(1)}" height="${failH.toFixed(1)}" rx="${Math.min(3,barW/2).toFixed(1)}"/>`;
      }
    });
    hourly.forEach((h,i)=>{
      // 每 4 小时一个刻度（与旧面板一致），末桶必标
      if(i%4===0 || i===hourly.length-1) s += `<text class="axis-label" text-anchor="middle" x="${cx(i).toFixed(1)}" y="${H-6}">${h.label||''}</text>`;
    });
    hourly.forEach((h,i)=>{ s += `<rect class="hz" data-i="${i}" x="${(padL+step*i).toFixed(1)}" y="${padT}" width="${step.toFixed(1)}" height="${ih.toFixed(1)}" fill="transparent"/>`; });
    s += `<g class="tip" opacity="0" pointer-events="none"><rect class="tip-bg" rx="7" width="130" height="56"/><text class="tip-t" x="10" y="16"></text><text class="tip-l" x="10" y="30"></text><text class="tip-l" x="10" y="43"></text></g>`;
    el.innerHTML = `<svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">${s}</svg>`;
    this._chartGeom = {padL, step, W};
    // 悬停提示
    const svg = el.querySelector('svg');
    const tip = svg.querySelector('.tip');
    const bg = tip.querySelector('.tip-bg');
    const [tt, l1, l2] = tip.querySelectorAll('text');
    svg.querySelectorAll('.hz').forEach(hz=>{
      hz.addEventListener('mouseenter', ()=>{
        const i = +hz.dataset.i, h = hourly[i];
        tt.textContent = (h.label||'')+' 时段';
        l1.textContent = `总 ${this.fmt(h.total||0)} · 成功 ${this.fmt(h.success||0)}`;
        // 第三行是本时段实际扣费：与模型表的消耗列同一口径（仅计费帧）
        l2.textContent = `失败 ${this.fmt(h.failed||0)} · 消耗 ${this.fmtCredits(h.credits||0)}`;
        const w = Math.max(...[tt,l1,l2].map(t=>t.getComputedTextLength())) + 20;
        bg.setAttribute('width', w.toFixed(0));
        const g = this._chartGeom;
        let tx = g.padL + g.step*i + g.step/2 + 10;
        if(tx + w > g.W - 2) tx = g.padL + g.step*i + g.step/2 - w - 10;
        tip.setAttribute('transform', `translate(${Math.max(2,tx).toFixed(1)},${padT+2})`);
        tip.setAttribute('opacity','1');
        const bar = svg.querySelector(`.bar-c[data-i="${i}"]`);
        if(bar) bar.classList.add('hl');
      });
      hz.addEventListener('mouseleave', ()=>{
        tip.setAttribute('opacity','0');
        svg.querySelectorAll('.bar-c.hl').forEach(b=>b.classList.remove('hl'));
      });
    });
  },
  /* 柱图峰值文案（卡头右侧），无数据时与图表空态一致 */
  get hourlyPeak(){
    const max = (this.stats&&this.stats.hourly||[]).reduce((a,h)=>Math.max(a,h.total||0),0);
    return max > 0 ? '峰值 ' + this.fmt(max) : '暂无数据';
  },
  /* 成功率条的着色：与旧面板阈值一致（<50% 红，<90% 黄） */
  rateClass(rate){
    if(rate < 0.5) return 'err';
    if(rate < 0.9) return 'warn';
    return '';
  },

  /* ── 额度数字格式化（本页模板与 getter 共用） ── */
  fmtMonthDay(ms) {
    var t = new Date(ms);
    var m = t.getMonth() + 1;
    var day = t.getDate();
    return (m < 10 ? '0' + m : m) + '-' + (day < 10 ? '0' + day : day);
  },
  fmtCredits(v) {
    var n = Number(v || 0);
    if (n === 0) return '0';
    if (n >= 1000) return n.toLocaleString('zh-CN', {maximumFractionDigits: 2});
    return n.toPrecision(6).replace(/\.?0+$/, '');
  },

  /* ── 模型（自「模型」页迁入：模型列表改在本页展示） ── */
  async loadModels(){
    this.models.loading = true;
    try{
      const r = await this.api('/admin/api/models');
      this.models.models = r.models||[];
    }catch(e){ this.toast(e.message,'err'); }
    finally{ this.models.loading = false; }
  },

  /* ── 进场：本页所需数据 ── */
  load(){ this.loadStats(); this.loadModels(); },
});

# Qoder-2API-Go

> Qoder2API 的 Go 语言实现 —— 将 Qoder AI 服务桥接为 OpenAI 兼容 API。

本项目参考 [fengyinxia/qoder2api: QoderWork -> OpenAI compatible API bridge (dynamic model loading, pure Python)](https://github.com/fengyinxia/qoder2api) ，将原有 Python 代码完整重写为 Go，保留了全部核心功能，同时提升了性能与部署便利性。

## 功能特性

- **OpenAI API 兼容** —— 支持 `/v1/chat/completions` 和 `/v1/models` 端点，可无缝替换 OpenAI API
- **流式响应** —— 支持 SSE 流式输出；`stream_options.include_usage: true` 时末尾附带 usage 帧（含输入 / 输出 / 缓存命中 / 思考 token 与实际扣费额度）
- **参数完全透传** —— `reasoning_effort`、`max_tokens`、`temperature` 等参数原样转发网关，不做本地校验/钳制/映射；网关拒绝时按真实状态码与原文回传客户端
- **真实用量统计** —— `/v1/chat/completions` 响应携带网关真实 token 用量：`usage.prompt_tokens` / `completion_tokens` / `total_tokens`、`prompt_tokens_details.cached_tokens`、`completion_tokens_details.reasoning_tokens`，以及 Qoder 扩展字段 `credits`（实际扣费额度）/ `original_credits`（折扣前额度）
- **多模态支持** —— 支持图片输入
- **Tool Calls** —— 支持函数调用
- **模型动态加载** —— 自动从网关获取可用模型列表
- **Web 管理面板** —— 浅色排版风格中文界面（支持深浅模式），支持密码登录、API 密钥管理、PAT 配置、模型查看、请求统计（总量 / 成功失败 / 总输入 / 总输出 / 缓存命中 / **本服务实际扣费额度与均值** / 本周期扣费与重置倒计时 / 按模型消耗 / 近 24 小时趋势）
- **请求日志** —— 记录每次调用的元信息（时间、模型、密钥、流式与否、token 用量、扣费额度、耗时、状态码与错误摘要，不保存对话内容），面板「日志」页支持按模型 / 密钥 / 状态筛选与分页，保留天数与条数上限可配置、自动清理最旧条目
- **订阅周期感知** —— 从网关 `/algo/api/v3/user/status` 读取真实订阅信息（套餐、组织、`nextResetAt` 重置时间、`isQuotaExceeded` 超额标记），使面板额度按计费周期统计而非终身累计
- **单文件部署** —— 编译为单一二进制文件，零外部依赖

## 支持的模型

| 显示名称 | 内部 Key |
| ----------------- | -------------- |
| Qwen3.8-Max | qmodel_38max |
| Qwen3.7-Max | qmodel_latest |
| Qwen3.7-Plus | qmodel |
| Qwen3.6-Flash | q36fmodel |
| DeepSeek-V4-Pro | dmodel |
| DeepSeek-V4-Flash | dfmodel |
| GLM-5.3 | gmodel |
| GLM-5.2 | gm51model |
| Kimi-K2.7-Code | kmodel |
| MiniMax-M2.7 | mmodel |

> 以上为内置默认列表（catalog-v6，2026-08-15），实际可用模型以网关动态返回为准。
>
> **关于图片输入**：部分模型并非原生多模态，而是由网关对图片附带辅助识别（非原生多模态模型加上辅助后实际可以识图），因此上表不再标注是否支持视觉；识图效果以实际调用结果为准。

## 快速开始

### 方式一：直接运行

```bash
# 编译
go build -o qoder2api .

# 运行（默认监听 0.0.0.0:10081）
./qoder2api
```

> 收到 `SIGINT`（Ctrl+C）或 `SIGTERM` 会触发优雅关闭：停止接受新连接、等待在途请求并落盘统计后退出。

### 方式二：Docker 部署

```bash
# 使用 docker-compose
docker-compose up -d

# 或手动构建（可传 VERSION 构建参数，登录页将显示对应版本号，缺省为 dev）
docker build --build-arg VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev) -t qoder2api .
docker run -d -p 10081:10081 -v qoder2api-data:/app/data -e QODER_DATA_PATH=/app/data/data.json qoder2api
```

### 方式三：下载预编译二进制

前往 [Releases](https://github.com/EchoPing07/Qoder-2API-Go/releases) 下载对应平台的二进制文件，直接运行即可。

## 配置

### 环境变量

| 变量名 | 默认值 | 说明 |
| ------- | -------- | ------ |
| `QODER_HOST` | `0.0.0.0` | 监听地址 |
| `QODER_PORT` | `10081` | 监听端口 |
| `QODER_DATA_PATH` | `data.json` | 数据文件路径 |
| `QODER_ADMIN_PASSWORD` | `password` | 管理面板密码（覆盖 data.json 中的值） |
| `QODER_SIGNATURE_SECRET` | 内置值 | 请求签名密钥 |
| `QODER_CHAT_TIMEOUT_SECONDS` | `120` | Chat 响应超时：等待上游开始响应（返回响应头）的最长秒数，范围 1-3600；设置后管理面板中不可修改 |
| `QODER_IDLE_TIMEOUT_SECONDS` | `300` | 流空闲超时：流式响应中持续无数据的最长等待秒数，范围 1-3600（数据持续到达时流不会被中断）；设置后管理面板中不可修改 |
| `QODER_MAX_CONCURRENCY` | `1` | 单 PAT 同时发往上游的聊天请求数上限；超出部分本地排队等待。调大会提升吞吐，但超过 Qoder 账号的并发窗口时网关会拒绝请求（业务码 10605） |
| `QODER_LOG_RETENTION_DAYS` | `90` | 请求日志保留天数，范围 1-3650；超期条目自动删除。设置后管理面板中不可修改 |
| `QODER_LOG_MAX_ENTRIES` | `2000` | 请求日志条数上限，范围 100-100000；超限时自动删除最旧条目，与保留天数同时生效。设置后管理面板中不可修改 |

> 两个超时也可在管理面板「设置 → 超时配置」中修改，保存后即时生效（无需重启）。响应超时只约束上游「开始响应」，不会截断已经建立的流。

> 请求统计（`stats` 字段）与请求日志（`logs` 字段）随 data.json 持久化：服务每 30 秒批量写入一次，并在收到 `SIGINT`/`SIGTERM` 优雅关闭时执行最终落盘；仅统计通过密钥鉴权且请求体合法的调用，客户端主动断开不计入失败。令牌用量（总输入 / 总输出 / 缓存命中 / 实际扣费额度）从网关 usage 帧自动累计，流中断无 usage 帧时仅计次不计量。请求日志同样只记录通过密钥鉴权的调用（鉴权失败不记录），错误信息截断至 500 字符，日志配置亦可在管理面板「设置 → 日志配置」中修改并即时生效。

### 配置文件 (data.json)

首次运行时自动生成，也可通过 Web 管理面板修改：

```json
{
  "host": "0.0.0.0",
  "port": 10081,
  "pat": "pt-xxxxxxxx",
  "password": "password",
  "chat_timeout_seconds": 120,
  "idle_timeout_seconds": 300,
  "log_retention_days": 90,
  "log_max_entries": 2000,
  "api_keys": [
    {
      "id": "xxxxxxxx",
      "key": "sk-xxxxxxxxxxxxxxxx",
      "note": "备注",
      "created_at": 1234567890
    }
  ]
}
```

## 健康检查

`GET /health` 返回服务状态（免鉴权，供容器探针 / 负载均衡使用）：

```json
{
  "status": "ok",
  "has_pat": true,
  "chat_timeout_seconds": 120,
  "idle_timeout_seconds": 300
}
```

## 使用方法

### 1. 配置 PAT

启动服务后访问 `http://localhost:10081/admin`，输入默认密码 `password` 登录，在「令牌」标签页中填入你的 Qoder PAT。

> ⚠️ **默认密码仅用于首次登录，请立即在「设置」标签页中修改**。登录失败会按客户端 IP 触发指数退避限流，部署到公网时务必启用 HTTPS 反代。

### 2. 创建 API 密钥

在「API 密钥」标签页中创建密钥（可自定义或自动生成），客户端使用此密钥调用 API。

### 3. 调用 API

```bash
curl http://localhost:10081/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxxxxxxxxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen3.7-Max",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

### 4. 获取模型列表

```bash
curl http://localhost:10081/v1/models \
  -H "Authorization: Bearer sk-xxxxxxxxxxxxxxxx"
```

### 5. 思考强度与参数透传（verbatim 原则）

本桥接层对请求参数执行**完全原样转发**（verbatim）策略：客户端给什么就往网关送什么，不做本地校验、钳制、映射或静默替换；网关不接受时，错误按真实状态码与原文回传（见下文「错误回传」）。

```json
{
  "model": "Qwen3.8-Max",
  "messages": [{"role": "user", "content": "你好"}],
  "reasoning_effort": "xhigh",
  "max_tokens": 16000,
  "temperature": 0.5
}
```

参数处理一览：

| 请求字段 | 处理 |
| --- | --- |
| `reasoning_effort` / `reasoning.effort` | 原样写入 `parameters.reasoning_effort`（顶层优先）。不校验档位、不映射 `minimal`、不因目录未声明而丢弃；模型不支持时由网关报错回传 |
| `max_tokens` / `max_completion_tokens` | 任意 JSON 数値原样写入 `parameters.max_tokens`（含 0、负数、小数、超大值与超出 float64 精度的整数；`max_tokens` 优先）。未传时才用目录默认值 |
| `max_thinking_tokens` | 客户端显式传入时原样转发（即使同时携带 `reasoning_effort: "none"`，也以客户端值为准）；仅 `none` 档且未传时才合成显式 `0`（官方客户端的天闭开关） |
| `temperature` / `top_p` / `stop` / `seed` / `response_format` / `logit_bias` / `presence_penalty` / `frequency_penalty` / `n=1` | 原样写入 `parameters`。网关认不认由网关决定，错误回传 |
| `n` ≠ 1 | 本地 `400`：响应通道结构上只能产生单个 choice，拒绝静默只返回一个 |
| 类型错误（如 `"max_tokens": "8000"`、`"reasoning_effort": 3`） | 本地 `400 invalid_request_error` |
| `stream_options.include_usage` | 流式响应仅在显式 `true` 时发送 usage 终帧（OpenAI 语义）；非流式始终携带 usage |

#### `none` 档：完全关闭思考

`reasoning_effort: "none"` 无条件转发（不再要求目录声明 `thinking_config.disabled`），并同时完成三件事（与官方客户端一致）：

1. `parameters.reasoning_effort` 置为 `"none"`
2. `parameters.max_thinking_tokens` 显式置为 `0`
3. `model_config.is_reasoning` 与 `chat_context.extra.modelConfig.is_reasoning` 均置为 `false`

其余档位不写 `max_thinking_tokens`；显式非 `none` 档位会强制 `is_reasoning=true`（覆盖目录默认），未传档位时才由目录决定。

#### `is_reasoning` 与 `max_tokens` 的来源

- `is_reasoning`：客户端显式档位优先（非 `none` → `true`，`none` → `false`）；未传时看目录（`thinking_config.enabled` 存在且非 null → 顶层 `is_reasoning` → 回退 `true`）。
- `max_tokens`：仅当客户端未传时才用目录 `max_output_tokens`（缺失时 32000）。

> **档位实效说明**：实测显示，除 `none` 外各档位字符串对 `reasoning_tokens` 的影响落在采样噪声内，不应据此期待精细的思考强度控制；但桥接层不再因此替网关做裁决，调用方发送的档位一律上船。

**pi 客户端配置示例**：Qoder 上游不接受 OpenAI 的 `developer` 角色，而 pi 会用该角色承载代理指令；因此必须在 `~/.pi/agent/models.json` 的 `qoder-local` provider 上设置 `supportsDeveloperRole: false`，使 pi 改用 `system` 角色。为模型设置 `reasoning: true`，并将 pi 的档位映射到模型目录实际支持的值：

```json
{
  "providers": {
    "qoder-local": {
      "compat": {
        "supportsDeveloperRole": false
      }
    }
  }
}
```

以下模型配置适用于支持 `low` / `medium` / `xhigh` 的模型（桥接层不再过滤档位，`max` 映射仅是为了不发送无意义的无效值）：

```json
{
  "id": "Qwen3.8-Max",
  "reasoning": true,
  "thinkingLevelMap": {
    "off": "none",
    "minimal": "low",
    "low": "low",
    "medium": "medium",
    "high": "xhigh",
    "xhigh": "xhigh",
    "max": "xhigh"
  }
}
```

## API 端点

| 端点 | 方法 | 说明 |
| ------ | ------ | ------ |
| `/v1/chat/completions` | POST | 聊天补全（兼容 OpenAI 格式） |
| `/v1/models` | GET | 获取模型列表 |
| `/admin` | GET | Web 管理面板 |
| `/admin/api/login` | POST | 管理面板登录 |
| `/admin/api/keys` | GET/POST/DELETE | API 密钥管理 |
| `/admin/api/pat` | GET/POST | PAT 令牌管理 |
| `/admin/api/models` | GET | 获取模型列表 |
| `/admin/api/stats` | GET | 请求统计（总量 / 按模型 / 近 24 小时趋势 / token 用量 / 实际扣费额度与均值 / 计费周期与套餐信息） |
| `/admin/api/config` | GET/POST | 服务器配置 |
| `/admin/api/password` | POST | 修改管理密码 |

**错误响应约定**：登录失败限流返回 `429`（附 `Retry-After`）；创建重复 API Key 返回 `409`；端口配置越界（非 1–65535）返回 `400`；未鉴权或会话过期返回 `401`。

**上游错误回传**：网关对请求本身的拒绝（如不支持的档位、`max_tokens` 范围校验失败）按真实状态码回传——4xx 请求级拒绝 → `400 invalid_request_error` + 网关原始 message；429 → `429 rate_limit_error`（上游携带 `Retry-After` 时一并透传）；5xx → `502 upstream_error`。流式与非流式一视同仁：只要尚未向客户端写出任何内容，拒绝就以真实状态码的 JSON 错误体返回；内容已开始输出后才失败的，则以 SSE error chunk 携带真实原因（此时状态码只能保持 200）。网关在流内报错（HTTP 200 但错误帧）时，若尚无内容，同样按错误帧内嵌的状态码映射（无状态码则 `502`）。上游会话/PAT 凭证被拒（网关 401/403）回传 `502 upstream_error`（上游凭证问题不是客户端的 API key 问题，不回传 401 以免误导）。参数类型错误（字符串数值等）与 `n`≠1 在本地返回 `400`。

## 额度与计费周期

管理面板使用 Qoder 官方客户端同源的额度接口，展示当前计费周期的权威用量，而不是仅统计本服务启动后的局部消耗：

```text
套餐额度      2,939 / 3,000   剩余 61      可用
加购额度      0 / 1,000       剩余 1,000   可用
组织资源包    120 / 4,000     剩余 3,880   可用
合计剩余      4,941 · 09-25 重置
```

> **合计口径**：`合计剩余` 只累加可消费的额度——标记为「不可用」的池子（如组织资源包的 `available=false`）虽仍会单独成行展示，但不计入合计，否则汇总数字会与紧上方那一行的「不可用」自相矛盾。

> 免费账号的套餐池为 `userQuota.total = 0`（套餐本身不含额度），此时面板首行直接展示真正持有额度的池子（通常是加购额度），而不是渲染成「套餐 0 / 0 · 已用尽」。`total`/`cap` 为 0 的池子视为不存在，不会出现在额度明细卡中。

### 数据来源

主数据来自 `GET https://openapi.qoder.com.cn/api/v2/quota/usage`，使用现有 jobToken 交换返回的 `securityOauthToken` 作为 Bearer。该接口与官方客户端 `/usage` 面板相同，提供：

| 字段 | 含义 | 用途 |
| ------ | ------ | ------ |
| `userQuota.total/used/remaining` | 套餐总额、已用及剩余 Credits | 面板额度行（`total = 0` 时该池不参与展示） |
| `userQuota.percentage` | 套餐使用比例 | 管理 API 输出及详细信息 |
| `addOnQuota` | 加购额度包（存在时） | 管理 API 输出及额度明细卡 |
| `orgResourcePackage` | 组织资源包容量、已用、剩余及可用状态 | 管理 API 输出及额度明细卡 |
| `expiresAt` | 当前额度周期结束时刻（仅兜底） | 重置日期与倒计时 |
| `isQuotaExceeded` | 官方客户端采用的超额判定 | 面板红色告警徽标 |

网关 `POST /algo/api/v3/user/status?Encode=1` 仍负责提供 `plan`、`userTag`、组织名称和真实 `userType`。它的 `quota: 0` 是精简字段，**不能解释为套餐没有数字上限**；额度必须以 OpenAPI 的 `userQuota` 为准。

### 统计与降级口径

- **官方额度优先**：面板的套餐用量来自账号级 OpenAPI，包含 IDE、CLI 和本服务产生的全部消耗，也能覆盖服务启动前的当期用量。
- **额度来源分别展示**：套餐额度、加购额度和组织资源包在额度明细卡中分别成行展示，并汇总各额度来源返回的剩余 Credits；组织资源包的“可用/不可用”状态以 OpenAPI 返回值为准。
- **本服务实际消耗单独成卡**：官方额度池是账号级数字，无法回答“本服务花了多少”。统计页的「实际消耗 Credits」卡直接累加网关 usage 帧里的 `credits`（实际扣费额度，已含折扣），因此可核对到每一次请求：

  | 字段 | 含义 |
  | ------ | ------ |
  | `credits` | 本服务历史累计实际扣费 |
  | `cycle_credits` | 本订阅周期内的实际扣费（有周期边界时卡片展示此项） |
  | `billed_requests` | 实际计费的请求数（仅 `billable=true` 的 usage 帧） |
  | `avg_credits` | `credits / billed_requests`，平均每次请求的实际花费 |

  > **分母为什么不是请求总数**：`total` 含失败请求、`billable=false` 的免费帧，以及流中断未收到 usage 帧的请求，用它作分母会低估均值。`billed_requests` 只统计真正扣费的帧，均值才等于「每次计费请求的均价」。

  > **按模型 / 按小时归因**：`by_model[].credits` 与 `hourly[].credits` 把消耗归到具体模型与时段，行内的 `avg_credits` 给出该行的均价（按该行 `billed` 计）；模型表的「消耗 / 平均」列与趋势图 tooltip 均取此口径。该模型未产生计费请求时「平均」列显示 `—` 而非 `0`，避免把「未计费」误读为「免费」。

- **升级说明**：`billed_requests` 自本版起累计，旧 `data.json` 中没有该字段时，卡片副行会提示「计费次数与均值自本版起统计」，而不是在非零的累计值下方报「暂无计费请求」；已累计的 `credits` 与历史周期数据不受影响。
- **本地累计仅作降级**：usage 帧中 `billable=false` 的请求仍计入 token，但不计入本地 credits；OpenAPI 从未成功时，面板才显示本服务观察到的周期消耗，并明确标注口径。
- **周期边界权威同步**：重置时刻以网关 `/user/status` 的 `nextResetAt` 为权威，OpenAPI `expiresAt` 仅在其缺失时兜底；且 `expiresAt` 必须小于 `2100-01-01` 才会被采纳——无期限套餐会把它填成 `9999-12-31` 哨兵值，该值会被忽略。本地周期起点按自然月回退推导（月末会夹取到目标月最后一天，3/31 → 2/28），断网期间仍可完成一次性滚动。
- **边界变更的接受规则**（防止累计值被抖动或陈旧缓存误清）：首次拿到边界时**保留**已累计的 credits；边界**未向前推进**（`/user/status` 缓存 1 小时，可能上报已滚过的旧值）则直接忽略；前进幅度小于 7 天时视为同一周期，不清零；达到 7 天以上才判定为真实续期并清零上一周期。
- **累计值仍保留**：`stats.credits` 保存本服务历史累计扣费，鼠标悬停在额度行可查看。

### 刷新时机

主程序启动后立即获取一次额度，此后每分钟在后台刷新。管理面板的 15 秒轮询完全读取内存和持久化快照，不直接调用上游；临时网络故障会保留最后一次成功结果。

### `userType` 修复

`jobToken` 响应中**不含** `userType` 字段，此前 bridge 一律回退为 `personal_standard`，导致团队账号的签名会话载荷层级错误。现改为在构建会话**之前**从 `/user/status` 取真实层级（该值被 AES 签入 bearer 载荷，事后无法修改）。状态接口临时不可用时，按「上次已知层级 → 空值 → 历史默认值」顺序降级，避免续期时把团队账号静默降级。

## 安全

本项目面向内网 / 个人使用，已内置以下加固措施，**部署到公网前请务必修改默认密码并使用 HTTPS 反代**：

- **HTTP 超时**：`ReadHeaderTimeout=10s`、`IdleTimeout=120s`，抵御 slowloris 类慢速连接耗尽；`WriteTimeout` 不设以兼容长连接 SSE 流式响应，流内超过 5 分钟无数据（空闲）自动断开，防止网关连接卡死。
- **请求体上限**：`/v1/chat/completions` 限制 10 MiB，管理 API 限制 64 KiB，防止超大 payload 耗尽内存。
- **登录限流**：管理面板登录按客户端 IP 计数，失败后指数退避（1s → 30s 封顶），锁定期间返回 `429` + `Retry-After`。
- **恒定时间比较**：API Key 校验与管理密码比对均使用 `crypto/subtle`，避免时序侧信道泄露。
- **Cookie 安全**：会话 Cookie 为 `HttpOnly` + `SameSite=Lax`，在 HTTPS（含 `X-Forwarded-Proto: https` 反代）下自动附加 `Secure`。
- **优雅关闭**：收到 `SIGINT`/`SIGTERM` 后停止接受新连接、等待在途请求（最长 15s）并完成统计最终落盘，避免数据丢失。
- **XSS 防护**：Web 面板用户输入通过 `textContent` 渲染与 `addEventListener` 绑定，避免内联事件处理器中的字符串注入。
- **输入校验**：API Key 唯一性校验（重复返回 `409`）；端口范围校验 1–65535；工具调用索引边界保护（防 panic / OOM）。

## 项目结构

```
Qoder-2API-Go/
├── main.go              # 程序入口（请求模板内联在 bridge 包中）
├── auth/                # 认证模块
├── bridge/              # API 桥接模块
├── models/              # 模型管理
├── store/               # 数据持久化
├── transform/           # 数据转换
├── admin/               # Web 管理面板
├── Dockerfile           # Docker 构建文件
├── docker-compose.yaml  # Docker Compose 配置
└── .github/workflows/   # GitHub Actions 自动构建
```

## 技术栈

- **语言**：Go 1.22+
- **依赖**：仅使用 `github.com/google/uuid`，其余全部标准库
- **加密**：RSA + AES 混合加密、MD5 签名、自定义 Base64 编码
- **HTTP**：标准库 `net/http`，SSE 流式响应，优雅关闭
- **存储**：JSON 文件持久化，线程安全读写
- **安全**：请求体上限、登录限流、恒定时间鉴权、Secure Cookie、XSS 防护

## 开发

```bash
# 克隆仓库
git clone https://github.com/EchoPing07/Qoder-2API-Go.git
cd Qoder-2API-Go

# 运行测试
go test ./...

# 构建
go build -o qoder2api .

# 构建（跨平台）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o qoder2api-linux-arm64 .
```

## 许可证

本项目基于 [MIT License](./LICENSE) 开源。

## 免责声明

本项目仅供学习和研究目的使用。

1. **本项目不隶属于 Qoder 或其关联公司**，Qoder 是其各自所有者的商标。本项目不对任何官方服务提供保证或支持。
2. **仅供学习交流使用**，不得用于任何商业用途或非法用途。使用者应遵守所在地区的法律法规。
3. **本项目不对任何因使用或滥用本项目而导致的直接或间接损失负责**，包括但不限于数据丢失、服务中断、账号封禁等情况。
4. **使用者需自行承担使用本项目的所有风险**，项目维护者不对本项目的正确性、完整性、可靠性或适用性作任何明示或暗示的保证。
5. **请勿将本项目用于绕付费、绕限制、大规模爬取或其他可能损害服务提供方利益的行为**。请尊重服务提供方的条款和条件。
6. 如果本项目的任何内容侵犯了您的权益，请及时联系项目维护者，我们将积极配合处理。

使用本项目即表示您已阅读并同意以上免责声明。如果您不同意，请立即停止使用并删除本项目。

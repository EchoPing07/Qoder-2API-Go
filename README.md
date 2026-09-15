# Qoder-2API-Go

> Qoder2API 的 Go 语言实现 —— 将 Qoder AI 服务桥接为 OpenAI 兼容 API。

本项目参考 [fengyinxia/qoder2api: QoderWork -> OpenAI compatible API bridge (dynamic model loading, pure Python)](https://github.com/fengyinxia/qoder2api) ，将原有 Python 代码完整重写为 Go，保留了全部核心功能，同时提升了性能与部署便利性。

## 功能特性

- **OpenAI API 兼容** —— 支持 `/v1/chat/completions` 和 `/v1/models` 端点，可无缝替换 OpenAI API
- **流式响应** —— 支持 SSE 流式输出，末尾附带 usage 帧（含输入 / 输出 / 缓存命中 / 思考 token 与实际扣费额度，遵循 `stream_options.include_usage` 终帧格式）
- **真实用量统计** —— `/v1/chat/completions` 响应携带网关真实 token 用量：`usage.prompt_tokens` / `completion_tokens` / `total_tokens`、`prompt_tokens_details.cached_tokens`、`completion_tokens_details.reasoning_tokens`，以及 Qoder 扩展字段 `credits`（实际扣费额度）/ `original_credits`（折扣前额度）
- **多模态支持** —— 支持图片输入
- **Tool Calls** —— 支持函数调用
- **模型动态加载** —— 自动从网关获取可用模型列表
- **Web 管理面板** —— 浅色排版风格中文界面（支持深浅模式），支持密码登录、API 密钥管理、PAT 配置、模型查看、请求统计（总量 / 成功失败 / 总输入 / 总输出 / 缓存命中 / **本周期扣费额度与重置倒计时** / 按模型 / 近 24 小时趋势）
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

# 或手动构建
docker build -t qoder2api .
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

> 两个超时也可在管理面板「设置 → 超时配置」中修改，保存后即时生效（无需重启）。响应超时只约束上游「开始响应」，不会截断已经建立的流。

> 请求统计（`stats` 字段）随 data.json 持久化：服务每 30 秒批量写入一次，并在收到 `SIGINT`/`SIGTERM` 优雅关闭时执行最终落盘；仅统计通过密钥鉴权且请求体合法的调用，客户端主动断开不计入失败。令牌用量（总输入 / 总输出 / 缓存命中 / 实际扣费额度）从网关 usage 帧自动累计，流中断无 usage 帧时仅计次不计量。

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

### 5. 控制思考强度（`reasoning_effort`）

请求体支持 OpenAI 风格的 `reasoning_effort`。有效档位写入网关请求的 `parameters` 对象（与官方客户端一致），**无需**额外的 device token、Bearer token 或模型服务域名配置；未携带该字段的请求保持原有行为。

```json
{
  "model": "Qwen3.8-Max",
  "messages": [{"role": "user", "content": "你好"}],
  "reasoning_effort": "xhigh"
}
```

桥接层从动态模型目录读取每个模型的 `efforts` 与 `supports_disabled` 后校验档位：有效值为 `none`、`low`、`medium`、`high`、`xhigh`、`max`，`minimal` 会映射为 `low`。不受当前模型支持的档位会被省略，模型继续使用默认思考强度；不会发送可能导致上游拒绝的无效值。

> **注意**：档位必须放在 `parameters` 内才会生效。网关不读取请求体顶层的 `reasoning_effort`，写在该位置会被静默忽略，模型一律按默认强度思考。此外 `parameters` 还承载 `max_tokens`、`tool_choice` 等字段，桥接层会自动组装，调用方无需关心。

#### `none` 档：完全关闭思考

`none` 是二值开关而非强度档位，需要额外配合才能真正关闭思考。桥接层在档位解析为 `none` 时会同时做三件事（与官方客户端行为一致）：

1. `parameters.reasoning_effort` 置为 `"none"`
2. `parameters.max_thinking_tokens` 显式置为 `0`（该字段用指针承载，以保证 `0` 不被 JSON 序列化省略）
3. `model_config.is_reasoning` 与 `chat_context.extra.modelConfig.is_reasoning` 均置为 `false`

其余档位不会写入 `max_thinking_tokens`：官方客户端仅在技能 / 子代理覆盖场景才把档位换算为思考预算，主交互路径只发送档位字符串。

#### `is_reasoning` 与 `max_tokens` 的来源

两项能力信息来自网关模型目录，与官方客户端的解析方式一致：

- `is_reasoning`：取目录的 `is_reasoning` 字段，缺失时为 `false`。动态目录不可用时回退为 `true`，以保持桥接层原有的"默认开启思考"行为，避免静默关闭所有模型的思考能力。
- `max_tokens`：取目录的 `max_output_tokens`，缺失或非法（非正整数）时回退为 `32000`，与官方客户端的 `LS()` 归一化结果相同。

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

以下模型配置适用于支持 `low` / `medium` / `xhigh` 的模型（`max` 映射到目录实际支持的最高档位，避免发送被上游丢弃的无效值）：

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
| `/admin/api/stats` | GET | 请求统计（总量 / 按模型 / 近 24 小时趋势 / token 用量 / 扣费额度 / 计费周期与套餐信息） |
| `/admin/api/config` | GET/POST | 服务器配置 |
| `/admin/api/password` | POST | 修改管理密码 |

**错误响应约定**：登录失败限流返回 `429`（附 `Retry-After`）；创建重复 API Key 返回 `409`；端口配置越界（非 1–65535）返回 `400`；未鉴权或会话过期返回 `401`。

## 额度与计费周期

Qoder 的订阅额度**按月刷新**，因此「累计扣费」这个数字在跨过重置点后就不再反映当前套餐余量。面板改为按计费周期统计：

```text
本周期 Credits 12.34 · 09-25 重置 · 剩 18 天   [Teams]
```

### 数据来源

计费边界来自网关 `POST /algo/api/v3/user/status?Encode=1` 的 `nextResetAt` 字段（epoch 毫秒），**无需手工配置锚定日期**。该接口同时返回：

| 字段 | 含义 | 用途 |
| ------ | ------ | ------ |
| `nextResetAt` | 额度刷新时刻 | 周期边界，过期后本周期额度自动归零 |
| `isQuotaExceeded` | 网关判定的超额状态 | 面板徽标（超额时显示红色告警） |
| `plan` / `userTag` | 套餐标识与展示名 | 面板徽标（如 `Teams`） |
| `userType` | 真实账号层级 | 签名会话载荷（详见下文） |

### 统计口径

- **只统计计费请求**：usage 帧中 `billable=false` 的请求仍计入 token，但不计入 credits —— 未被扣费的请求若计入会虚增周期消耗。
- **周期起点按自然月推导**：由 `nextResetAt` 回退一个自然月得到（9 月 25 日重置 → 周期自 8 月 25 日起），而非固定 30 天，避免日期漂移。
- **周期归零只发生一次**：跨过边界后本地自动推进到下一个边界；`/user/status` 随后返回权威边界并与之对账，重复轮询不会丢失已累计的额度。
- **累计值仍保留**：终身总额存于 `stats.credits`，鼠标悬停在周期额度上即可查看。

### 刷新时机

`/user/status` 结果在 bridge 内缓存 1 小时，主程序每 10 分钟检查一次，因此**绝大多数轮询都命中缓存**。管理面板的 15 秒轮询完全走内存，不产生任何上游调用。

### 关于 `quota` 字段

团队套餐返回 `quota: 0`，即网关**不提供数字化的额度上限**。因此面板不渲染进度条或百分比 —— 只显示确凿可知的信息（已用量 + 重置时间 + 网关超额判定），不臆造上限。

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

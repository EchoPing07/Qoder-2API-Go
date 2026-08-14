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
- **Web 管理面板** —— 浅色排版风格中文界面（支持深浅模式），支持密码登录、API 密钥管理、PAT 配置、模型查看、请求统计（总量 / 成功失败 / 总输入 / 总输出 / 缓存命中 / 实际扣费额度 / 按模型 / 近 24 小时趋势）
- **单文件部署** —— 编译为单一二进制文件，零外部依赖

## 支持的模型

| 显示名称            | 内部 Key         |
| ----------------- | -------------- |
| Qwen3.8-Max       | qmodel_38max   |
| Qwen3.7-Max       | qmodel_latest  |
| Qwen3.7-Plus      | qmodel         |
| Qwen3.6-Flash     | q36fmodel      |
| DeepSeek-V4-Pro   | dmodel         |
| DeepSeek-V4-Flash | dfmodel        |
| GLM-5.3           | gmodel         |
| GLM-5.2           | gm51model      |
| Kimi-K2.7-Code    | kmodel         |
| MiniMax-M2.7      | mmodel         |

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
|-------|--------|------|
| `QODER_HOST` | `0.0.0.0` | 监听地址 |
| `QODER_PORT` | `10081` | 监听端口 |
| `QODER_DATA_PATH` | `data.json` | 数据文件路径 |
| `QODER_ADMIN_PASSWORD` | `password` | 管理面板密码（覆盖 data.json 中的值） |
| `QODER_SIGNATURE_SECRET` | 内置值 | 请求签名密钥 |

> 请求统计（`stats` 字段）随 data.json 持久化：服务每 30 秒批量写入一次，并在收到 `SIGINT`/`SIGTERM` 优雅关闭时执行最终落盘；仅统计通过密钥鉴权且请求体合法的调用，客户端主动断开不计入失败。令牌用量（总输入 / 总输出 / 缓存命中 / 实际扣费额度）从网关 usage 帧自动累计，流中断无 usage 帧时仅计次不计量。

### 配置文件 (data.json)

首次运行时自动生成，也可通过 Web 管理面板修改：

```json
{
  "host": "0.0.0.0",
  "port": 10081,
  "pat": "pt-xxxxxxxx",
  "password": "password",
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

## API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/chat/completions` | POST | 聊天补全（兼容 OpenAI 格式） |
| `/v1/models` | GET | 获取模型列表 |
| `/admin` | GET | Web 管理面板 |
| `/admin/api/login` | POST | 管理面板登录 |
| `/admin/api/keys` | GET/POST/DELETE | API 密钥管理 |
| `/admin/api/pat` | GET/POST | PAT 令牌管理 |
| `/admin/api/models` | GET | 获取模型列表 |
| `/admin/api/stats` | GET | 请求统计（总量 / 按模型 / 近 24 小时趋势 / token 用量 / 扣费额度） |
| `/admin/api/config` | GET/POST | 服务器配置 |
| `/admin/api/password` | POST | 修改管理密码 |

**错误响应约定**：登录失败限流返回 `429`（附 `Retry-After`）；创建重复 API Key 返回 `409`；端口配置越界（非 1–65535）返回 `400`；未鉴权或会话过期返回 `401`。

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

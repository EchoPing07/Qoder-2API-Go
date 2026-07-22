# Qoder-2API-Go

> Qoder2API 的 Go 语言实现 —— 将 Qoder AI 服务桥接为 OpenAI 兼容 API。

本项目参考 [fengyinxia/qoder2api: QoderWork -> OpenAI compatible API bridge (dynamic model loading, pure Python)](https://github.com/fengyinxia/qoder2api) ，将原有 Python 代码完整重写为 Go，保留了全部核心功能，同时提升了性能与部署便利性。

## 功能特性

- **OpenAI API 兼容** —— 支持 `/v1/chat/completions` 和 `/v1/models` 端点，可无缝替换 OpenAI API
- **流式响应** —— 支持 SSE 流式输出
- **多模态支持** —— 支持图片输入
- **Tool Calls** —— 支持函数调用
- **模型动态加载** —— 自动从网关获取可用模型列表
- **Web 管理面板** —— 浅色白绿配色中文界面，支持密码登录、API 密钥管理、PAT 配置、模型查看
- **单文件部署** —— 编译为单一二进制文件，零外部依赖

## 支持的模型

| 显示名称 | 内部 Key | 视觉 |
|---------|----------|------|
| Qwen3.8-Max-Preview | qmodel_preview | 支持 |
| Qwen3.7-Max | qmodel_latest | 支持 |
| Qwen3.7-Plus | qmodel | 支持 |
| Qwen3.6-Flash | q36fmodel | 支持 |
| DeepSeek-V4-Pro | dmodel | 支持 |
| DeepSeek-V4-Flash | dfmodel | 支持 |
| GLM-5.2 | gm51model | 支持 |
| Kimi-K2.7-Code | kmodel | 支持 |
| MiniMax-M2.7 | mmodel | 不支持 |

> 以上为内置默认列表，实际可用模型以网关动态返回为准。

## 快速开始

### 方式一：直接运行

```bash
# 编译
go build -o qoder2api .

# 运行（默认监听 0.0.0.0:18080）
./qoder2api
```

### 方式二：Docker 部署

```bash
# 使用 docker-compose
docker-compose up -d

# 或手动构建
docker build -t qoder2api .
docker run -d -p 18080:18080 -v qoder2api-data:/app/data qoder2api
```

### 方式三：下载预编译二进制

前往 [Releases](https://github.com/EchoPing07/Qoder-2API-Go/releases) 下载对应平台的二进制文件，直接运行即可。

## 配置

### 环境变量

| 变量名 | 默认值 | 说明 |
|-------|--------|------|
| `QODER_HOST` | `0.0.0.0` | 监听地址 |
| `QODER_PORT` | `18080` | 监听端口 |
| `QODER_DATA_PATH` | `data.json` | 数据文件路径 |
| `QODER_ADMIN_PASSWORD` | `password` | 管理面板密码（覆盖 data.json 中的值） |
| `QODER_SIGNATURE_SECRET` | 内置值 | 请求签名密钥 |

### 配置文件 (data.json)

首次运行时自动生成，也可通过 Web 管理面板修改：

```json
{
  "host": "0.0.0.0",
  "port": 18080,
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

启动服务后访问 `http://localhost:18080/admin`，输入默认密码 `password` 登录，在「令牌」标签页中填入你的 Qoder PAT。

### 2. 创建 API 密钥

在「API 密钥」标签页中创建密钥（可自定义或自动生成），客户端使用此密钥调用 API。

### 3. 调用 API

```bash
curl http://localhost:18080/v1/chat/completions \
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
curl http://localhost:18080/v1/models \
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
| `/admin/api/config` | GET/POST | 服务器配置 |
| `/admin/api/password` | POST | 修改管理密码 |

## 项目结构

```
Qoder-2API-Go/
├── main.go              # 程序入口
├── auth/                # 认证模块
├── bridge/              # API 桥接模块
├── models/              # 模型管理
├── store/               # 数据持久化
├── transform/           # 数据转换
├── admin/               # Web 管理面板
├── baseprompt.json      # Qoder 请求模板
├── Dockerfile           # Docker 构建文件
├── docker-compose.yaml  # Docker Compose 配置
└── .github/workflows/   # GitHub Actions 自动构建
```

## 技术栈

- **语言**：Go 1.22+
- **依赖**：仅使用 `github.com/google/uuid`，其余全部标准库
- **加密**：RSA + AES 混合加密、MD5 签名、自定义 Base64 编码
- **HTTP**：标准库 `net/http`，SSE 流式响应
- **存储**：JSON 文件持久化，线程安全读写

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

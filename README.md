# ⚡ Amp Sentinel

基于 [Amp](https://ampcode.com) AI 的线上故障自动诊断平台。

接收任意格式的事件上报，自动拉取项目源码，由 Amp AI 分析排障，将结构化诊断结论推送至飞书。

```
事件上报 ──▶ 接入层(Schema-less / 去重 / 限流) ──▶ 优先级调度 ──▶ 诊断引擎 ──▶ 飞书通知
                                                                      │
                                                              源码 + Skills
                                                              结构化输出 + 质量评分
                                                              指纹复用
```

## 核心特性

- **Schema-less 事件接入** — 任意 JSON payload，无需适配固定字段结构；支持标准模式、简单模式、批量 NDJSON、旧版兼容四种上报方式
- **全自动闭环** — 事件上报 → 源码拉取 → AI 诊断 → 飞书通知，无需人工介入
- **只读安全** — 绝不修改代码，只做分析诊断；四层防护机制（Amp Permissions + Prompt 约束 + 文件系统权限 + 结果校验）
- **结构化诊断输出** — AI 返回结构化 JSON，支持本地质量评分和置信度量化
- **指纹复用** — 相同故障指纹在配置窗口内命中历史报告时直接复用，避免重复分析
- **优先级调度** — Critical > Warning > Info，支持并发控制、超时、自动重试
- **去重 & 限流** — 可配置去重字段和窗口（支持项目级覆盖），分片速率限制，OOM 防护
- **可扩展 Skills** — 自定义脚本查询订单、日志等业务数据，辅助 AI 排障
- **多存储后端** — SQLite / MySQL / JSON 文件，可插拔切换
- **Web 管理后台** — 仪表盘、事件列表、任务详情、诊断报告全屏查看

## 快速开始

### 前置要求

- Go 1.25+
- [Amp CLI](https://ampcode.com) 已安装并登录
- Amp API Key（从 [ampcode.com/settings](https://ampcode.com/settings) 获取）

### 安装

```bash
git clone <repo-url> amp-sentinel
cd amp-sentinel
go build -o amp-sentinel .
```

### 配置

```bash
cp config.yaml.example config.yaml
```

编辑 `config.yaml`，填入必要配置：

| 配置项 | 说明 | 获取方式 |
|---|---|---|
| `amp.api_key` | Amp API Key | [ampcode.com/settings](https://ampcode.com/settings) |
| `intake.auth_token` | 事件上报认证 Token | `openssl rand -hex 32` |
| `source.git_ssh_key` | Git SSH 私钥路径 | `~/.ssh/id_ed25519` |
| `feishu.default_webhook` | 飞书机器人 Webhook | 飞书群设置 → 机器人 |
| `admin_api.auth_token` | 管理后台认证 Token | 自定义字符串 |

### 启动

```bash
# 通过环境变量传入敏感配置
export AMP_API_KEY=your_api_key

# 启动服务
./amp-sentinel
```

启动后会输出：
```
sentinel.ready  projects=1 concurrency=3 listen=:8080
admin.dashboard url=http://localhost:9090/admin/dashboard/
```

## 上报事件

### 标准模式

信封 + payload 分离，payload 为任意 JSON：

```bash
curl -X POST http://localhost:8080/api/v1/events \
  -H "Authorization: Bearer <intake_auth_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "project_key": "order-service",
    "severity": "critical",
    "payload": {
      "error_msg": "NullPointerException at OrderService.java:128",
      "stacktrace": "at com.example.order...",
      "user_id": "12345",
      "order_no": "ORD20260301001"
    }
  }'
```

### 简单模式

通过 query 参数指定项目和严重级别，请求体整体作为 payload：

```bash
curl -X POST "http://localhost:8080/api/v1/events?project=order-service&severity=critical" \
  -H "Authorization: Bearer <intake_auth_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "error_msg": "gateway timeout at /api/order",
    "request_id": "req-abc123"
  }'
```

### 批量模式 (NDJSON)

每行一个 JSON 事件，一次请求提交多个事件：

```bash
curl -X POST "http://localhost:8080/api/v1/events/batch?project=order-service" \
  -H "Authorization: Bearer <intake_auth_token>" \
  -H "Content-Type: application/x-ndjson" \
  --data-binary @- <<'EOF'
{"error_msg":"timeout at /api/order","severity":"critical"}
{"error_msg":"connection refused","severity":"warning"}
EOF
```

### 旧版兼容

`POST /api/v1/incidents` 兼容旧版固定字段格式，自动转换为 Schema-less 事件。

### 请求字段（标准模式）

| 字段 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `project_key` | ✅ | — | 项目标识，须在 projects 中注册 |
| `payload` | ✅ | — | 任意 JSON，将直接交给 AI 分析 |
| `severity` | — | `warning` | `critical` / `warning` / `info` |
| `source` | — | `custom` | 来源标识 |
| `title` | — | 自动提取 | 从 payload 中提取 title/error_msg 等字段 |

### 响应

```json
{
  "event_id": "evt-a1b2c3d4",
  "task_id": "task-e5f6g7h8",
  "status": "queued",
  "message": "event accepted"
}
```

## 诊断流水线

```
事件接入 → 指纹计算 → 历史复用检查
                          │
              ┌───命中───┘└───未命中───┐
              ▼                        ▼
         返回缓存报告          拉取源码 + 构建 Prompt
                                       │
                                       ▼
                               Amp AI 诊断执行
                                       │
                                       ▼
                              源码安全校验（只读铁律）
                                       │
                                       ▼
                            结构化输出解析 + 质量评分
                                       │
                                       ▼
                            生成报告 → 飞书通知 → 持久化
```

### 结构化输出 (P0)

开启 `diagnosis.structured_output: true` 后，AI 返回结构化 JSON 格式的诊断结果，系统自动解析并进行质量评分（文件引用验证、诊断完整性等），量化置信度。

### 指纹复用 (P1)

开启 `diagnosis.fingerprint_reuse_enabled: true` 后，系统基于 payload 中的关键字段计算指纹。在配置的时间窗口内，若命中历史高质量报告（且代码未变更或严重度未升级），直接复用历史结论，节省 AI 调用成本。

## 项目配置

```yaml
projects:
  - key: "order-service"
    name: "订单服务"
    repo_url: "git@github.com:your-org/order-service.git"
    branch: "main"
    language: "java"
    source_root: "."            # 源码根目录（相对于仓库根）
    skills: ["query_order"]
    owners: ["张三"]
    feishu_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx"
    dedup:                       # 项目级去重覆盖（可选）
      fields: ["error_type", "error_msg"]
      window: "30m"
```

## 存储后端

在 `config.yaml` 的 `store` 段切换：

```yaml
# SQLite（默认，单机部署）
store:
  type: "sqlite"
  sqlite:
    path: "./data/sentinel.db"

# MySQL（生产推荐）
store:
  type: "mysql"
  mysql:
    dsn: "user:pass@tcp(127.0.0.1:3306)/amp_sentinel?charset=utf8mb4&parseTime=true"

# JSON 文件（开发测试）
store:
  type: "json"
  json:
    path: "./data/sentinel.json"
```

## 管理后台

启用 `admin_api` 后访问 Dashboard：

```yaml
admin_api:
  enabled: true
  listen: ":9090"
  auth_token: "${ADMIN_API_TOKEN}"
```

**API 端点：**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/dashboard/` | Web 管理界面 |
| GET | `/admin/v1/health` | 健康检查 |
| GET | `/admin/v1/stats` | 统计概览 |
| GET | `/admin/v1/incidents` | 事件列表 |
| GET | `/admin/v1/incidents/:id` | 事件详情 |
| POST | `/admin/v1/incidents/:id/retry` | 重新诊断 |
| GET | `/admin/v1/tasks` | 任务列表 |
| GET | `/admin/v1/tasks/:id` | 任务详情 |
| GET | `/admin/v1/reports/:id` | 诊断报告 |
| GET | `/admin/v1/projects` | 项目列表 |

## 项目结构

```
amp-sentinel/
├── main.go                 # 入口，组装各模块
├── config.go               # 配置定义与加载
├── config.yaml.example     # 配置模板
├── DESIGN.md               # 技术方案策划文档
├── SCHEMA-LESS-DESIGN.md   # Schema-less 事件接入设计文档
├── DIAGNOSIS_STRATEGY.md   # 智能化诊断验证策略文档
├── amp/                    # Amp CLI 客户端封装
├── intake/                 # 事件接入（HTTP、去重、限流、Schema-less 解析）
│   ├── handler.go          # 标准/简单/批量/兼容模式处理
│   └── types.go            # RawEvent 模型、标题提取、严重度映射
├── scheduler/              # 优先级调度器（Worker pool + 并发控制 + 超时重试）
├── diagnosis/              # 诊断引擎
│   ├── engine.go           # 诊断流程编排（指纹复用 → Amp 调用 → 安全校验）
│   ├── prompt.go           # Prompt + AGENTS.md 动态构建
│   ├── report.go           # 诊断报告结构化
│   ├── structured.go       # 结构化 JSON 输出解析
│   ├── scoring.go          # 质量评分（文件验证、完整性）
│   ├── fingerprint.go      # 事件指纹计算与复用判断
│   └── fixer.go            # LLM JSON 修复器（兜底）
├── notify/                 # 飞书通知（富文本卡片）
├── store/                  # 持久化（SQLite / MySQL / JSON，可插拔）
├── project/                # 项目注册表 & 源码管理
├── skill/                  # 自定义 Skill 加载
├── logger/                 # 结构化日志（控制台 / 文件轮转 / JSON）
└── api/                    # 管理后台 API & Web Dashboard
    └── web/                # 前端静态文件
```

## 技术选型

| 组件 | 选择 | 说明 |
|---|---|---|
| 语言 | Go 1.25 | 纯 Go 实现，无 CGO |
| AI 引擎 | Amp CLI (`amp -x`) | `--stream-json` 流式 NDJSON 输出 |
| HTTP | 标准库 `net/http` | 无外部框架 |
| SQLite | `modernc.org/sqlite` | 纯 Go，无 CGO |
| MySQL | `go-sql-driver/mysql` | 社区标准驱动 |
| 配置 | `gopkg.in/yaml.v3` | YAML + 环境变量展开 |
| 日志 | 自研结构化日志 | 控制台彩色 + 文件轮转 + JSON |
| 前端 | Tailwind CSS + Chart.js | 单页应用 |
| 安全 | `crypto/subtle` | 认证 token 常量时间比较 |

## License

MIT

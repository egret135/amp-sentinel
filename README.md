# ⚡ Amp Sentinel

基于 [Amp](https://ampcode.com) AI 的线上故障自动诊断平台。

当线上项目发生错误时，自动接收告警、拉取源码、AI 分析排障，将诊断结论推送至飞书。

```
告警上报 ──▶ 接入层(去重/限流) ──▶ 优先级调度 ──▶ Amp AI 诊断 ──▶ 飞书通知
                                                    │
                                              源码 + Skills
```

## 核心特性

- **全自动闭环** — 故障上报 → 源码拉取 → AI 诊断 → 飞书通知，无需人工介入
- **只读安全** — 绝不修改代码，只做分析诊断；检测到源码变更自动回滚并标记报告
- **优先级调度** — Critical > Warning > Info，支持并发控制、超时、自动重试
- **去重 & 限流** — 相同错误短时间内自动去重，按项目限流防止轰炸
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
export AMP_API_KEY=sgamp_xxx

# 启动服务
./amp-sentinel
```

启动后会输出：
```
sentinel.ready  projects=1 concurrency=3 listen=:8080
admin.dashboard url=http://localhost:9090/admin/dashboard/
```

## 上报事件

```bash
curl -X POST http://localhost:8080/api/v1/incidents \
  -H "Authorization: Bearer <intake_auth_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "project_key": "your-project",
    "title": "接口超时",
    "error_msg": "gateway timeout at /api/order",
    "severity": "critical"
  }'
```

### 请求字段

| 字段 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `project_key` | ✅ | — | 项目标识，须在 projects 中注册 |
| `title` / `error_msg` | 至少一个 | — | 故障标题或错误信息 |
| `severity` | — | `warning` | `critical` / `warning` / `info` |
| `environment` | — | `production` | 环境标识 |
| `error_type` | — | — | 错误类型 |
| `stacktrace` | — | — | 堆栈跟踪 |
| `source` | — | `custom` | 来源标识 |
| `url` | — | — | 相关链接 |
| `metadata` | — | — | 自定义键值对 |

## 项目结构

```
amp-sentinel/
├── main.go              # 入口，组装各模块
├── config.go            # 配置定义与加载
├── config.yaml.example  # 配置模板
├── amp/                 # Amp CLI 客户端封装
├── intake/              # 事件接入（HTTP、去重、限流）
├── scheduler/           # 优先级调度器
├── diagnosis/           # 诊断引擎（Amp 调用、报告解析）
├── notify/              # 飞书通知
├── store/               # 持久化（SQLite/MySQL/JSON）
├── project/             # 项目注册表 & 源码管理
├── skill/               # 自定义 Skill 加载
├── logger/              # 结构化日志
└── api/                 # 管理后台 API & Web Dashboard
    └── web/             # 前端静态文件
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

## 技术选型

- **语言**: Go 1.25，纯 Go 实现，无 CGO
- **AI 引擎**: Amp CLI（`amp -x` 执行模式）
- **HTTP**: 标准库 `net/http`，无外部框架
- **日志**: 自研结构化日志
- **前端**: Tailwind CSS + Chart.js，单页应用

## License

MIT

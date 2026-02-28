# Amp Sentinel — 线上故障自动诊断平台技术方案策划文档

> 版本：v0.2.0 | 日期：2026-02-28 | 状态：策划阶段

---

## 目录

- [1. 项目概述](#1-项目概述)
- [2. 系统全景架构](#2-系统全景架构)
- [3. Amp CLI 集成方式](#3-amp-cli-集成方式)
- [4. Amp 消息协议](#4-amp-消息协议)
- [5. 故障上报与接入](#5-故障上报与接入)
- [6. 项目注册与源码管理](#6-项目注册与源码管理)
- [7. 安全防护 — 只读铁律](#7-安全防护--只读铁律)
- [8. 自定义 Skill 系统](#8-自定义-skill-系统)
- [9. 诊断引擎](#9-诊断引擎)
- [10. 飞书通知](#10-飞书通知)
- [11. 调度器](#11-调度器)
- [12. 持久化方案](#12-持久化方案)
- [13. 日志方案](#13-日志方案)
- [14. 配置设计](#14-配置设计)
- [15. HTTP API 设计](#15-http-api-设计)
- [16. 关键数据结构](#16-关键数据结构)
- [17. 技术选型](#17-技术选型)
- [18. 开发阶段规划](#18-开发阶段规划)
- [19. 注意事项](#19-注意事项)

---

## 1. 项目概述

### 1.1 定位

基于 Amp AI 构建的**线上故障自动诊断平台**。当线上项目发生错误时，自动接收告警、拉取源码、结合业务数据进行 AI 分析排障，最终将诊断结论推送至飞书通知相关人员。

### 1.2 核心原则

| 原则 | 说明 |
|---|---|
| **只读分析** | 🔴 **绝对不允许修改代码、不允许提交代码**，只做分析诊断 |
| **自动闭环** | 故障上报 → 识别项目 → 拉取源码 → AI 分析 → 飞书通知，全程自动 |
| **可扩展** | 用户可自定义 Skill 查询订单、用户、日志等业务数据辅助排障 |
| **结论明确** | 无论是否定位到问题，都给出明确结论和说明 |

### 1.3 核心流程总览

```
线上项目告警                       使用者
   │                                ▲
   ▼                                │
┌──────────┐    ┌──────────┐    ┌───┴──────┐
│ 故障上报  │───▶│ 诊断引擎  │───▶│ 飞书通知  │
│ API      │    │          │    │ Webhook  │
└──────────┘    │ Amp AI   │    └──────────┘
                │ + Skills │
                │ + 源码   │
                └──────────┘
```

---

## 2. 系统全景架构

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        amp-sentinel (线上故障自动诊断平台)                     │
│                                                                             │
│  ┌─────────────────┐                                                        │
│  │   Intake API     │  ◄── Sentry / AlertManager / 自定义监控 上报故障        │
│  │  POST /incidents │                                                       │
│  └────────┬────────┘                                                        │
│           │                                                                  │
│           ▼                                                                  │
│  ┌─────────────────┐     ┌──────────────────┐     ┌──────────────────────┐  │
│  │ Project Registry │────▶│  Source Manager   │────▶│    Diagnosis Engine   │  │
│  │                 │     │                  │     │                      │  │
│  │ - 项目 → 仓库映射│     │ - git clone/pull │     │ ┌──────────────────┐ │  │
│  │ - 分支/标签配置  │     │ - 只读工作区管理  │     │ │   Amp Client     │ │  │
│  │ - 负责人/飞书群  │     │ - 自动清理       │     │ │  --stream-json   │ │  │
│  └─────────────────┘     └──────────────────┘     │ │  只读权限锁定    │ │  │
│                                                    │ └────────┬─────────┘ │  │
│                                                    │          │           │  │
│  ┌─────────────────┐                               │ ┌────────▼─────────┐ │  │
│  │  Skill Manager   │◄─────────────────────────────┤ │  Prompt Builder  │ │  │
│  │                 │                               │ │                  │ │  │
│  │ - 用户自定义脚本 │  Amp 通过 Skill 查询业务数据    │ │ - 错误上下文注入  │ │  │
│  │ - 查询订单/用户  │                               │ │ - Skill 清单注入  │ │  │
│  │ - 查询线上日志   │                               │ │ - 只读约束注入   │ │  │
│  │ - MCP Server    │                               │ └──────────────────┘ │  │
│  └─────────────────┘                               └──────────┬───────────┘  │
│                                                                │              │
│           ┌────────────────────────────────────────────────────┘              │
│           ▼                                                                   │
│  ┌─────────────────┐     ┌──────────────────┐     ┌──────────────────────┐   │
│  │   Scheduler      │     │   Feishu Notifier │     │   Store + Logger     │   │
│  │                 │     │                  │     │                      │   │
│  │ - 并发控制      │────▶│ - Webhook 推送   │     │ - SQLite / MySQL     │   │
│  │ - 队列/优先级   │     │ - 富文本卡片消息  │     │ - 文件/结构化日志    │   │
│  │ - 超时/重试     │     │ - 诊断报告格式化  │     │ - 会话日志保留       │   │
│  └─────────────────┘     └──────────────────┘     └──────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.1 目录结构

```
amp-sentinel/
├── main.go                        # 入口
├── go.mod
├── go.sum
├── config.yaml                    # 配置文件
├── DESIGN.md                      # 本文档
│
├── amp/                           # Amp CLI 封装层
│   ├── client.go                  # AmpClient —— 封装 amp -x --stream-json
│   ├── types.go                   # Stream JSON 消息类型定义
│   └── permission.go              # 只读权限规则生成
│
├── intake/                        # 故障接入层
│   ├── server.go                  # HTTP 接口，接收故障上报
│   ├── handler.go                 # 请求校验、去重、限流
│   └── types.go                   # Incident 数据模型
│
├── project/                       # 项目注册与源码管理
│   ├── registry.go                # 项目注册表（项目 → 仓库 → 负责人）
│   └── source.go                  # 源码 clone/pull/只读工作区管理
│
├── skill/                         # 自定义 Skill 系统
│   ├── manager.go                 # Skill 加载、注册、生命周期
│   ├── types.go                   # Skill 定义格式
│   └── builtin/                   # 内置 Skill 示例
│       ├── query_log/             # 查询线上日志
│       │   └── SKILL.md
│       └── query_order/           # 查询订单数据
│           └── SKILL.md
│
├── diagnosis/                     # 诊断引擎
│   ├── engine.go                  # 诊断流程编排
│   ├── prompt.go                  # Prompt 构建（注入错误上下文 + 只读约束）
│   └── report.go                  # 诊断报告结构化
│
├── notify/                        # 通知层
│   ├── feishu.go                  # 飞书 Webhook 推送
│   └── types.go                   # 消息卡片模板
│
├── scheduler/                     # 调度引擎
│   ├── scheduler.go               # Worker pool + 队列 + 并发控制
│   └── task.go                    # 诊断任务模型
│
├── store/                         # 持久化层（可插拔）
│   ├── store.go                   # Store 接口定义
│   ├── sqlite.go                  # SQLite 实现
│   ├── mysql.go                   # MySQL 实现
│   └── json.go                    # JSON 文件实现
│
├── logger/                        # 日志层（可插拔）
│   ├── logger.go                  # Logger 接口定义
│   ├── console.go                 # 控制台日志
│   ├── file.go                    # 文件日志（按天轮转）
│   └── structured.go             # 结构化 JSON 日志
│
└── api/                           # 管理 API
    └── server.go                  # 项目管理、任务查询、统计
```

---

## 3. Amp CLI 集成方式

Amp 提供两种编程接口：

| 方式 | 支持语言 | 特点 |
|---|---|---|
| CLI `--stream-json` 模式 | 任意语言 | 启动 `amp -x --stream-json` 子进程，stdout 逐行输出 NDJSON |
| 官方 SDK | TypeScript / Python | 封装好的 `execute()` 函数，流式消息 |

**本项目选择 CLI 封装方案**（Go 无官方 SDK）。

### 3.1 单次执行

```bash
amp --execute "<prompt>" --stream-json --dangerously-allow-all
```

可选参数：

| 参数 | 说明 |
|---|---|
| `--cwd <dir>` | 指定工作目录（未直接提供时通过设置 `cmd.Dir` 实现） |
| `--stream-json-thinking` | 输出包含 thinking 块（扩展 schema） |
| `--dangerously-allow-all` | 跳过所有权限确认（自动化必须） |

### 3.2 多轮对话

```bash
amp --execute --stream-json --stream-json-input --dangerously-allow-all
```

通过 stdin 发送用户消息：

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"你的消息"}]}}
```

关闭 stdin 后 Amp 输出最终 result 并退出。

### 3.3 认证

设置环境变量：

```bash
export AMP_API_KEY=sgamp_your_access_token_here
```

Token 在 [ampcode.com/settings](https://ampcode.com/settings) 获取，或通过 `amp login` 登录后自动存储。

---

## 4. Amp 消息协议

Amp Stream JSON 输出为 NDJSON 格式（每行一个 JSON 对象），包含 4 种消息类型：

```
system(init) → user(prompt) → assistant(text/tool_use) → user(tool_result) → ... → result(success/error)
```

### 4.1 消息类型一览

| type | subtype | 含义 | 关键字段 |
|---|---|---|---|
| `system` | `init` | 会话初始化 | `session_id`, `tools[]`, `mcp_servers[]`, `cwd` |
| `assistant` | — | AI 回复 | `content[]`（text / tool_use / thinking）, `stop_reason`, `usage` |
| `user` | — | 工具返回结果 | `content[]`（tool_result）|
| `result` | `success` | 执行成功 | `result`, `duration_ms`, `num_turns`, `usage` |
| `result` | `error_during_execution` | 执行出错 | `error`, `duration_ms` |
| `result` | `error_max_turns` | 超过最大轮次 | `error`, `duration_ms` |

### 4.2 Assistant 消息内容块类型

| content.type | 说明 |
|---|---|
| `text` | 文本回复 |
| `tool_use` | 调用工具（含 `id`, `name`, `input`） |
| `thinking` | 思考过程（需 `--stream-json-thinking`） |
| `redacted_thinking` | 脱敏思考内容 |

### 4.3 子代理（Subagent）支持

- 子代理消息的 `parent_tool_use_id` 指向 Task 工具的 ID
- 主代理消息的 `parent_tool_use_id` 为 `null`
- 最终 result 会等待所有子代理完成后再输出

### 4.4 Stream JSON 消息类型定义

```go
type StreamMessage struct {
    Type            string          `json:"type"`
    Subtype         string          `json:"subtype,omitempty"`
    SessionID       string          `json:"session_id,omitempty"`
    ParentToolUseID *string         `json:"parent_tool_use_id"`
    Message         *MessagePayload `json:"message,omitempty"`

    // system/init 字段
    Cwd        string      `json:"cwd,omitempty"`
    Tools      []string    `json:"tools,omitempty"`
    MCPServers []MCPServer `json:"mcp_servers,omitempty"`

    // result 字段
    IsError    bool       `json:"is_error,omitempty"`
    Result     string     `json:"result,omitempty"`
    Error      string     `json:"error,omitempty"`
    DurationMs int64      `json:"duration_ms,omitempty"`
    NumTurns   int        `json:"num_turns,omitempty"`
    Usage      *Usage     `json:"usage,omitempty"`
}

type MessagePayload struct {
    Type       string         `json:"type,omitempty"`
    Role       string         `json:"role"`
    Content    []ContentBlock `json:"content"`
    StopReason *string        `json:"stop_reason,omitempty"`
    Usage      *Usage         `json:"usage,omitempty"`
}

type ContentBlock struct {
    Type      string          `json:"type"`
    Text      string          `json:"text,omitempty"`
    ID        string          `json:"id,omitempty"`
    Name      string          `json:"name,omitempty"`
    Input     json.RawMessage `json:"input,omitempty"`
    Content   string          `json:"content,omitempty"`
    IsError   bool            `json:"is_error,omitempty"`
    ToolUseID string          `json:"tool_use_id,omitempty"`
    Thinking  string          `json:"thinking,omitempty"`
    Data      string          `json:"data,omitempty"`
}

type Usage struct {
    InputTokens              int    `json:"input_tokens"`
    OutputTokens             int    `json:"output_tokens"`
    MaxTokens                int    `json:"max_tokens"`
    CacheCreationInputTokens int    `json:"cache_creation_input_tokens,omitempty"`
    CacheReadInputTokens     int    `json:"cache_read_input_tokens,omitempty"`
    ServiceTier              string `json:"service_tier,omitempty"`
}

type MCPServer struct {
    Name   string `json:"name"`
    Status string `json:"status"`
}
```

---

## 5. 故障上报与接入

### 5.1 接入方式

外部监控系统通过 HTTP API 上报故障：

```
POST /api/v1/incidents
```

支持多种告警源适配：

| 告警源 | 接入方式 |
|---|---|
| Sentry | Sentry Webhook → 转换为统一格式 |
| Prometheus AlertManager | AlertManager Webhook → 转换为统一格式 |
| 自定义监控系统 | 直接调用统一 API |
| 手动触发 | 管理 API / CLI 手动提交 |

### 5.2 Incident 数据模型

```go
type Incident struct {
    ID          string            `json:"id"`
    ProjectKey  string            `json:"project_key"`  // 项目标识（用于匹配项目注册表）
    Title       string            `json:"title"`        // 故障标题
    ErrorType   string            `json:"error_type"`   // 错误类型: exception / timeout / 5xx / panic 等
    ErrorMsg    string            `json:"error_msg"`    // 错误信息
    Stacktrace  string            `json:"stacktrace"`   // 堆栈信息（如有）
    Environment string            `json:"environment"`  // 环境: production / staging
    Severity    string            `json:"severity"`     // 严重程度: critical / warning / info
    URL         string            `json:"url"`          // 触发错误的请求 URL（如有）
    Metadata    map[string]string `json:"metadata"`     // 附加信息（用户ID、订单号、请求ID等）
    Source      string            `json:"source"`       // 告警来源: sentry / alertmanager / custom
    OccurredAt  time.Time         `json:"occurred_at"`  // 故障发生时间
    ReportedAt  time.Time         `json:"reported_at"`  // 上报时间
}
```

### 5.3 请求示例

```bash
POST /api/v1/incidents
Content-Type: application/json

{
  "project_key": "order-service",
  "title": "订单创建接口 500 错误",
  "error_type": "exception",
  "error_msg": "NullPointerException: Cannot invoke method getPrice() on null object",
  "stacktrace": "at com.example.order.service.OrderService.createOrder(OrderService.java:128)\nat com.example.order.controller.OrderController.create(OrderController.java:45)\n...",
  "environment": "production",
  "severity": "critical",
  "url": "/api/v1/orders",
  "metadata": {
    "user_id": "12345",
    "order_no": "ORD20260228001",
    "request_id": "req-abc-123",
    "pod": "order-service-7d8f9b6c4-x2k9p"
  },
  "source": "sentry",
  "occurred_at": "2026-02-28T10:00:00Z"
}
```

### 5.4 防重与限流

| 策略 | 说明 |
|---|---|
| **去重窗口** | 相同 `project_key` + `error_msg` 在 N 分钟内只受理一次（可配置，默认 10 分钟） |
| **速率限制** | 每个项目每小时最多 N 次诊断（可配置，默认 10 次） |
| **严重程度过滤** | 可配置只处理 `critical` / `warning` 级别 |

---

## 6. 项目注册与源码管理

### 6.1 项目注册表

每个受监控的项目需要预先注册，建立 `project_key` 到仓库、分支、负责人的映射。

```go
type Project struct {
    Key           string   `json:"key" yaml:"key"`                 // 唯一标识，如 "order-service"
    Name          string   `json:"name" yaml:"name"`               // 显示名，如 "订单服务"
    RepoURL       string   `json:"repo_url" yaml:"repo_url"`       // Git 仓库地址
    Branch        string   `json:"branch" yaml:"branch"`           // 分析分支（默认 main）
    Language      string   `json:"language" yaml:"language"`       // 主语言: go / java / python / node 等
    SourceRoot    string   `json:"source_root" yaml:"source_root"` // 源码根目录（相对仓库根，默认 "."）
    Skills        []string `json:"skills" yaml:"skills"`           // 该项目可用的 Skill 列表
    Owners        []string `json:"owners" yaml:"owners"`           // 负责人列表
    FeishuWebhook string   `json:"feishu_webhook" yaml:"feishu_webhook"` // 飞书 Webhook（可覆盖全局）
    AgentsMD      string   `json:"agents_md" yaml:"agents_md"`     // 项目级 AGENTS.md 内容（可选）
}
```

配置方式（`config.yaml` 中定义）：

```yaml
projects:
  - key: "order-service"
    name: "订单服务"
    repo_url: "git@github.com:company/order-service.git"
    branch: "main"
    language: "java"
    skills: ["query_order", "query_log", "query_user"]
    owners: ["张三", "李四"]
    feishu_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx-order"

  - key: "payment-gateway"
    name: "支付网关"
    repo_url: "git@github.com:company/payment-gateway.git"
    branch: "main"
    language: "go"
    skills: ["query_log", "query_payment"]
    owners: ["王五"]

  - key: "user-center"
    name: "用户中心"
    repo_url: "git@github.com:company/user-center.git"
    branch: "release"
    language: "python"
    skills: ["query_user", "query_log"]
    owners: ["赵六"]
```

### 6.2 源码管理

```go
type SourceManager struct {
    BaseDir string // 所有项目源码的父目录，如 /data/repos
}
```

**工作流程**：

```
收到诊断任务
    │
    ▼
项目目录是否已存在？ (/data/repos/order-service)
    │
  是 │ 否
    ▼   ▼
git pull   git clone --depth=1 --branch=main <repo_url>
    │       │
    └───┬───┘
        ▼
   设为 Amp 的 --cwd
   （目录设为只读 chmod -R a-w 作为额外防护）
```

**关键设计**：

| 要点 | 说明 |
|---|---|
| **浅克隆** | `git clone --depth=1` 减少磁盘和时间消耗 |
| **定期清理** | 可配置最大缓存项目数 / 最大缓存时间 |
| **并发安全** | 同一项目的 clone/pull 操作加互斥锁 |
| **SSH Key** | 通过配置 `GIT_SSH_COMMAND` 指定私钥 |

---

## 7. 安全防护 — 只读铁律

**🔴 核心约束：系统绝对不允许修改代码、提交代码。这是不可妥协的安全底线。**

### 7.1 多层防护机制

```
┌─────────────────────────────────────────────────────────┐
│                    安全防护层次                           │
│                                                          │
│  Layer 1: Amp Permissions（权限规则）                     │
│  ├── 禁止 edit_file / create_file / undo_edit            │
│  ├── 禁止 Bash 中的 git commit / git push / rm / mv     │
│  └── 只允许 Read / Grep / glob / finder 等只读工具       │
│                                                          │
│  Layer 2: AGENTS.md Prompt 约束                          │
│  ├── 系统提示词中强调"只分析不修改"                        │
│  └── 要求输出诊断报告而非代码修改                         │
│                                                          │
│  Layer 3: 文件系统权限                                    │
│  ├── 源码目录设为只读 (chmod -R a-w)                      │
│  └── 备选：使用只读 bind mount                           │
│                                                          │
│  Layer 4: 结果校验                                       │
│  ├── 诊断完成后检查 git status，确认无变更                │
│  └── 如发现变更，立即 git checkout -- . 回滚并告警        │
│                                                          │
└─────────────────────────────────────────────────────────┘
```

### 7.2 Amp 权限规则

通过 Amp 的 permissions 机制，**精确控制允许和禁止的工具**：

```go
// 生成只读权限规则，传递给 amp CLI
func ReadOnlyPermissions() []string {
    return []string{
        // ===== 允许：只读工具 =====
        `allow Read`,
        `allow Grep`,
        `allow glob`,
        `allow finder`,
        `allow web_search`,
        `allow read_web_page`,

        // ===== 禁止：所有写入工具 =====
        `reject edit_file`,
        `reject create_file`,
        `reject undo_edit`,

        // ===== Bash：只允许只读命令 =====
        `allow Bash --cmd "cat *"`,
        `allow Bash --cmd "head *"`,
        `allow Bash --cmd "tail *"`,
        `allow Bash --cmd "grep *"`,
        `allow Bash --cmd "find *"`,
        `allow Bash --cmd "wc *"`,
        `allow Bash --cmd "ls *"`,
        `allow Bash --cmd "tree *"`,
        `allow Bash --cmd "file *"`,
        `allow Bash --cmd "git log *"`,
        `allow Bash --cmd "git show *"`,
        `allow Bash --cmd "git diff *"`,
        `allow Bash --cmd "git blame *"`,

        // ===== Bash：禁止危险命令 =====
        `reject Bash --cmd "git commit*"`,
        `reject Bash --cmd "git push*"`,
        `reject Bash --cmd "git add*"`,
        `reject Bash --cmd "git checkout*"`,
        `reject Bash --cmd "git reset*"`,
        `reject Bash --cmd "git merge*"`,
        `reject Bash --cmd "git rebase*"`,
        `reject Bash --cmd "rm *"`,
        `reject Bash --cmd "mv *"`,
        `reject Bash --cmd "cp *"`,
        `reject Bash --cmd "chmod *"`,
        `reject Bash --cmd "chown *"`,
        `reject Bash --cmd "sed *"`,
        `reject Bash --cmd "awk *"`,
        `reject Bash --cmd "dd *"`,
        `reject Bash --cmd "tee *"`,
        `reject Bash --cmd "curl -X PUT*"`,
        `reject Bash --cmd "curl -X POST*"`,
        `reject Bash --cmd "curl -X DELETE*"`,
        `reject Bash --cmd "curl -X PATCH*"`,
        `reject Bash --cmd "wget *"`,
    }
}
```

### 7.3 结果校验流程

```go
// 诊断完成后，强制校验源码目录无变更
func (e *Engine) verifyNoChanges(repoDir string) error {
    // 1. git status --porcelain
    // 2. 如果输出非空 → 有未预期的变更
    // 3. git checkout -- .  强制回滚
    // 4. 记录告警日志
    // 5. 诊断结果标记为 "tainted"（被污染）
}
```

---

## 8. 自定义 Skill 系统

### 8.1 设计目标

允许使用者定义自己的 Skill，让 Amp 在诊断过程中能够：

- 查询订单数据（通过内部 API）
- 查询用户数据
- 查询线上日志（Elasticsearch / Loki）
- 查询数据库记录
- 查询链路追踪（Jaeger / Zipkin）
- 调用任何内部诊断工具

### 8.2 Skill 结构

每个 Skill 是一个目录，遵循 Amp 的 Skill 规范：

```
skills/
├── query_order/
│   ├── SKILL.md              # Skill 描述 + 使用说明
│   └── mcp.json              # MCP Server 配置（定义工具）
│
├── query_log/
│   ├── SKILL.md
│   └── mcp.json
│
├── query_user/
│   ├── SKILL.md
│   └── mcp.json
│
└── query_payment/
    ├── SKILL.md
    └── mcp.json
```

### 8.3 SKILL.md 示例 — 查询订单

```markdown
---
name: query_order
description: 查询订单系统的订单数据，用于辅助排查订单相关故障
globs: ["**/*.java", "**/*.go"]
---

# 查询订单 Skill

当需要排查订单相关问题时，使用此 Skill 查询订单详情。

## 可用工具

- `query_order_by_id`: 根据订单号查询订单详情
- `query_order_by_user`: 根据用户ID查询最近订单
- `query_order_stats`: 查询订单统计（最近N分钟的成功/失败数）

## 使用场景

- 订单创建失败时，查询该订单的完整信息和状态流转
- 排查某用户相关问题时，查看该用户最近的订单记录
- 排查系统性问题时，查询订单成功率变化趋势
```

### 8.4 MCP Server 配置示例

每个 Skill 通过 MCP Server 暴露工具给 Amp。MCP Server 可以是：

**方式 A：本地脚本 MCP Server**

```json
// skills/query_order/mcp.json
{
  "query_order_server": {
    "command": "node",
    "args": ["skills/query_order/server.js"],
    "env": {
      "ORDER_API_BASE": "${ORDER_API_BASE}",
      "ORDER_API_TOKEN": "${ORDER_API_TOKEN}"
    }
  }
}
```

**方式 B：远程 MCP Server**

```json
// skills/query_log/mcp.json
{
  "log_query_server": {
    "url": "${LOG_MCP_SERVER_URL}",
    "headers": {
      "Authorization": "Bearer ${LOG_MCP_TOKEN}"
    }
  }
}
```

**方式 C：Toolbox 脚本（Shell/Python）**

如果不想写完整的 MCP Server，可以使用 Amp Toolbox 协议写简单的脚本工具：

```bash
#!/bin/bash
# skills/query_log/tools/search_log

# 当 TOOLBOX_ACTION=describe 时输出工具描述
if [ "$TOOLBOX_ACTION" = "describe" ]; then
cat <<EOF
name: search_log
description: 搜索线上日志。可按关键字、时间范围、服务名等条件搜索。
keyword: string 搜索关键字
service: string? 服务名（可选）
minutes: string? 最近N分钟（默认30）
EOF
exit 0
fi

# 当 TOOLBOX_ACTION=execute 时执行查询
KEYWORD=$(echo "$1" | jq -r '.keyword')
SERVICE=$(echo "$1" | jq -r '.service // "all"')
MINUTES=$(echo "$1" | jq -r '.minutes // "30"')

# 调用内部日志查询API（只读操作）
curl -s "http://log-api.internal/search?q=${KEYWORD}&service=${SERVICE}&minutes=${MINUTES}" \
  -H "Authorization: Bearer ${LOG_API_TOKEN}"
```

### 8.5 Skill 加载机制

```
诊断任务启动
    │
    ▼
根据 project.skills 列表确定需要加载的 Skill
    │
    ▼
┌───────────────────────────────────┐
│ 方式 1：通过 --skills 参数传递     │  amp -x --skills ./skills/query_order
│ 方式 2：通过 settings.json 注入   │  amp.mcpServers 配置
│ 方式 3：通过 AGENTS.md 引用       │  在项目目录放置 AGENTS.md
└───────────────────────────────────┘
    │
    ▼
Amp 自动发现并加载 Skill 中的 MCP Server / Toolbox
    │
    ▼
诊断过程中 Amp 可调用 Skill 提供的工具查询业务数据
```

### 8.6 Skill 与安全

| 安全要点 | 措施 |
|---|---|
| Skill 只做查询 | Skill 工具本身应设计为只读，不提供写入能力 |
| 网络隔离 | Skill MCP Server 只访问内部只读 API |
| 敏感数据脱敏 | Skill 返回的数据应由 MCP Server 做脱敏处理 |
| Token 隔离 | Skill 使用独立的只读 API Token |

---

## 9. 诊断引擎

### 9.1 诊断流程

```
收到 Incident
    │
    ▼
匹配 Project（通过 project_key 查注册表）
    │ 未匹配 → 飞书通知"未知项目，请注册"
    ▼
拉取/更新源码（git clone/pull）
    │
    ▼
准备 Amp 运行环境
├── 设置 cwd 为源码目录
├── 注入只读权限规则
├── 加载项目关联的 Skills
├── 生成 AGENTS.md（只读约束 + 项目上下文）
└── 构建诊断 Prompt
    │
    ▼
调用 Amp CLI (--stream-json)
    │
    ▼
流式接收诊断过程
├── 记录日志
├── 监控工具调用（安全审计）
└── 超时控制
    │
    ▼
提取诊断结果
    │
    ▼
验证源码无变更（git status）
    │
    ▼
结构化诊断报告
    │
    ▼
推送飞书通知
    │
    ▼
更新 Store 记录
```

### 9.2 动态生成 AGENTS.md

系统会在源码目录的临时位置生成 AGENTS.md，注入诊断上下文和安全约束：

```go
func (e *Engine) generateAgentsMD(project *Project, incident *Incident) string {
    return fmt.Sprintf(`
# 诊断任务指令

## 🔴 安全约束（最高优先级）

你正在执行一个**只读诊断任务**。以下规则不可违反：

1. **绝对禁止**修改任何文件
2. **绝对禁止**创建任何文件
3. **绝对禁止**执行 git commit / git push / git add
4. **绝对禁止**执行 rm / mv / cp / sed 等写入命令
5. 你只能使用 Read、Grep、glob、finder 等只读工具分析代码
6. 你只能使用 Bash 执行 cat / grep / find / git log / git blame 等只读命令

## 项目信息

- 项目: %s (%s)
- 语言: %s
- 分支: %s

## 故障信息

- 标题: %s
- 错误类型: %s
- 错误信息: %s
- 环境: %s
- 严重程度: %s
- 发生时间: %s

## 堆栈信息

%s

## 附加信息

%s

## 可用 Skill

你可以使用以下 Skill 中的工具查询业务数据辅助排障:
%s

## 输出要求

请按以下结构输出诊断报告：

1. **故障摘要**：一句话总结故障现象
2. **根因分析**：分析可能的根本原因（可以列出多个可能性，按可能性从高到低排序）
3. **代码定位**：指出具体的代码文件和行号（如果能定位到）
4. **影响范围**：评估故障影响的范围和严重程度
5. **修复建议**：给出修复建议（注意：你不能修改代码，只需给出建议）
6. **排查建议**：如果无法完全确认根因，给出进一步排查的建议

如果经过充分分析后认为代码层面没有问题，请明确说明：
- 代码逻辑无异常的分析依据
- 可能的非代码因素（基础设施、配置、外部依赖、数据等）
- 建议排查的方向
`,
        project.Name, project.Key,
        project.Language,
        project.Branch,
        incident.Title,
        incident.ErrorType,
        incident.ErrorMsg,
        incident.Environment,
        incident.Severity,
        incident.OccurredAt.Format(time.RFC3339),
        incident.Stacktrace,
        formatMetadata(incident.Metadata),
        formatSkillsList(project.Skills),
    )
}
```

### 9.3 诊断 Prompt 构建

```go
func (e *Engine) buildPrompt(project *Project, incident *Incident) string {
    return fmt.Sprintf(`你是一个线上故障诊断专家。请分析以下故障并给出诊断报告。

项目「%s」(%s) 在 %s 环境发生了故障:

错误类型: %s
错误信息: %s

%s

请阅读项目源码进行分析。你可以：
1. 使用 Read / Grep / finder 等工具阅读和搜索代码
2. 使用 git log / git blame 查看代码历史
3. 使用可用的 Skill 工具查询订单、用户、日志等业务数据

请输出结构化的诊断报告。`,
        project.Name, project.Key,
        incident.Environment,
        incident.ErrorType,
        incident.ErrorMsg,
        formatStacktrace(incident.Stacktrace),
    )
}
```

### 9.4 诊断报告结构

```go
type DiagnosisReport struct {
    IncidentID   string        `json:"incident_id"`
    ProjectKey   string        `json:"project_key"`
    ProjectName  string        `json:"project_name"`
    Summary      string        `json:"summary"`        // 故障摘要
    RawResult    string        `json:"raw_result"`     // Amp 原始输出
    HasIssue     bool          `json:"has_issue"`      // 是否发现问题
    Confidence   string        `json:"confidence"`     // 置信度: high / medium / low
    SessionID    string        `json:"session_id"`     // Amp 线程 ID
    DurationMs   int64         `json:"duration_ms"`    // 诊断耗时
    NumTurns     int           `json:"num_turns"`      // 对话轮次
    Usage        TokenUsage    `json:"usage"`          // Token 消耗
    ToolsUsed    []string      `json:"tools_used"`     // 使用的工具列表
    SkillsUsed   []string      `json:"skills_used"`    // 使用的 Skill 列表
    Tainted      bool          `json:"tainted"`        // 源码是否被意外修改
    DiagnosedAt  time.Time     `json:"diagnosed_at"`
}
```

---

## 10. 飞书通知

### 10.1 Webhook 推送

使用飞书自定义机器人 Webhook 推送诊断结果。

```
飞书 Webhook URL 格式:
https://open.feishu.cn/open-apis/bot/v2/hook/<webhook-id>
```

### 10.2 消息格式 — 交互式卡片

使用飞书的**消息卡片（Interactive Card）**格式，展示结构化的诊断报告：

**发现问题时的卡片模板**：

```json
{
  "msg_type": "interactive",
  "card": {
    "header": {
      "title": { "tag": "plain_text", "content": "🔴 故障诊断报告 — 订单服务" },
      "template": "red"
    },
    "elements": [
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**故障标题**: 订单创建接口 500 错误\n**严重程度**: Critical\n**发生时间**: 2026-02-28 10:00:00"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**📋 故障摘要**\n订单创建时因商品价格为 null 导致 NullPointerException"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**🔍 根因分析**\n1. **[高可能性]** `OrderService.java:128` — `getPrice()` 调用时商品对象为 null，缺少空值检查\n2. **[中可能性]** 商品服务返回了异常数据，商品信息未正确加载"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**💡 修复建议**\n1. 在 `OrderService.createOrder()` 中添加商品对象的空值检查\n2. 检查商品服务的接口是否有异常返回\n3. 考虑添加防御性编程，当商品信息缺失时返回明确错误"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**📊 诊断详情**\n置信度: 高 | 耗时: 25s | 对话轮次: 4 | Amp 线程: T-xxx"
        }
      },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**👤 负责人**: 张三, 李四"
        }
      }
    ]
  }
}
```

**未发现问题时的卡片模板**：

```json
{
  "msg_type": "interactive",
  "card": {
    "header": {
      "title": { "tag": "plain_text", "content": "🟡 故障诊断报告 — 订单服务（未定位到代码问题）" },
      "template": "yellow"
    },
    "elements": [
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**故障标题**: 订单创建接口 500 错误\n**严重程度**: Critical\n**发生时间**: 2026-02-28 10:00:00"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**📋 分析结论**\n经过对源码的全面分析，代码逻辑层面未发现明显缺陷。"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**🔍 分析依据**\n1. `OrderService.createOrder()` 方法逻辑完整，包含参数校验\n2. 异常处理链路正常，未发现遗漏的 catch\n3. 数据库操作使用了事务，无一致性问题"
        }
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**🔎 建议排查方向**\n1. 检查数据库连接池是否耗尽（Druid 监控面板）\n2. 检查商品服务是否有可用性问题\n3. 查看该时间段的服务器负载和 GC 情况\n4. 核查是否有配置变更"
        }
      }
    ]
  }
}
```

### 10.3 卡片颜色约定

| 场景 | Header Template | 含义 |
|---|---|---|
| `red` | 🔴 | 发现代码问题，置信度高 |
| `orange` | 🟠 | 发现可疑点，置信度中等 |
| `yellow` | 🟡 | 未定位到代码问题 |
| `purple` | 🟣 | 诊断异常（超时/Amp 报错等） |

### 10.4 飞书 Webhook 配置

```yaml
feishu:
  default_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx-default"
  timeout: "10s"
  retry_count: 3
  # 可选：签名校验
  sign_key: "${FEISHU_SIGN_KEY}"
```

每个项目可覆盖全局 webhook，推送到各自的飞书群。

---

## 11. 调度器

### 11.1 调度模型

```
                    故障上报 (Intake API)
                          │
                          ▼
                 ┌─────────────────┐
                 │  Priority Queue  │   按严重程度+时间排序
                 │                 │   critical > warning > info
                 └────────┬────────┘
                          │
              ┌───────────┼───────────┐
              ▼           ▼           ▼
         ┌─────────┐ ┌─────────┐ ┌─────────┐
         │Worker 1 │ │Worker 2 │ │Worker N │   (N = max_concurrency)
         │         │ │         │ │         │
         │amp -x...│ │amp -x...│ │amp -x...│
         └────┬────┘ └────┬────┘ └────┬────┘
              │           │           │
              └───────────┼───────────┘
                          ▼
               ┌──────────────────┐
               │ 结果处理 Pipeline │
               │                  │
               │ 1. 校验源码无变更 │
               │ 2. 解析诊断报告   │
               │ 3. 写入 Store     │
               │ 4. 推送飞书通知   │
               │ 5. 记录日志       │
               └──────────────────┘
```

### 11.2 优先级策略

| 严重程度 | 优先级值 | 说明 |
|---|---|---|
| `critical` | 100 | 最高优先级，插队处理 |
| `warning` | 50 | 正常优先级 |
| `info` | 10 | 最低优先级 |

相同优先级按 `occurred_at` 时间排序（先发生的先处理）。

---

## 12. 持久化方案

采用 **接口抽象 + 多后端实现** 模式，通过配置选择启用哪种后端。

### 12.1 Store 接口

```go
type Store interface {
    // Incident（故障事件）
    CreateIncident(ctx context.Context, incident *Incident) error
    GetIncident(ctx context.Context, id string) (*Incident, error)
    UpdateIncident(ctx context.Context, incident *Incident) error
    ListIncidents(ctx context.Context, filter IncidentFilter) ([]*Incident, error)

    // DiagnosisTask（诊断任务）
    CreateTask(ctx context.Context, task *DiagnosisTask) error
    GetTask(ctx context.Context, id string) (*DiagnosisTask, error)
    UpdateTask(ctx context.Context, task *DiagnosisTask) error
    ListTasks(ctx context.Context, filter TaskFilter) ([]*DiagnosisTask, error)
    CountByStatus(ctx context.Context) (map[TaskStatus]int, error)

    // DiagnosisReport（诊断报告）
    SaveReport(ctx context.Context, report *DiagnosisReport) error
    GetReport(ctx context.Context, taskID string) (*DiagnosisReport, error)

    // 去重查询
    FindRecentIncident(ctx context.Context, projectKey, errorMsg string, window time.Duration) (*Incident, error)

    // 统计
    GetUsageSummary(ctx context.Context) (*UsageSummary, error)

    // 生命周期
    Close() error
}
```

### 12.2 SQLite 实现

**适用场景**：单机部署、轻量使用、开发环境。

**依赖**：`modernc.org/sqlite`（纯 Go，无 CGO）

```sql
-- 故障事件表
CREATE TABLE IF NOT EXISTS incidents (
    id           TEXT PRIMARY KEY,
    project_key  TEXT NOT NULL,
    title        TEXT NOT NULL,
    error_type   TEXT NOT NULL DEFAULT '',
    error_msg    TEXT NOT NULL DEFAULT '',
    stacktrace   TEXT NOT NULL DEFAULT '',
    environment  TEXT NOT NULL DEFAULT 'production',
    severity     TEXT NOT NULL DEFAULT 'warning',
    url          TEXT NOT NULL DEFAULT '',
    metadata     TEXT NOT NULL DEFAULT '{}',
    source       TEXT NOT NULL DEFAULT 'custom',
    status       TEXT NOT NULL DEFAULT 'pending',
    occurred_at  DATETIME NOT NULL,
    reported_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_incidents_project_key ON incidents(project_key);
CREATE INDEX idx_incidents_status ON incidents(status);
CREATE INDEX idx_incidents_occurred_at ON incidents(occurred_at);
CREATE INDEX idx_incidents_dedup ON incidents(project_key, error_msg, occurred_at);

-- 诊断任务表
CREATE TABLE IF NOT EXISTS diagnosis_tasks (
    id           TEXT PRIMARY KEY,
    incident_id  TEXT NOT NULL REFERENCES incidents(id),
    project_key  TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    priority     INTEGER NOT NULL DEFAULT 0,
    session_id   TEXT NOT NULL DEFAULT '',
    num_turns    INTEGER NOT NULL DEFAULT 0,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    retry_count  INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at   DATETIME,
    finished_at  DATETIME
);

CREATE INDEX idx_tasks_status ON diagnosis_tasks(status);
CREATE INDEX idx_tasks_incident ON diagnosis_tasks(incident_id);

-- 诊断报告表
CREATE TABLE IF NOT EXISTS diagnosis_reports (
    id           TEXT PRIMARY KEY,
    task_id      TEXT NOT NULL REFERENCES diagnosis_tasks(id),
    incident_id  TEXT NOT NULL,
    project_key  TEXT NOT NULL,
    project_name TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    raw_result   TEXT NOT NULL DEFAULT '',
    has_issue    BOOLEAN NOT NULL DEFAULT 0,
    confidence   TEXT NOT NULL DEFAULT 'low',
    tools_used   TEXT NOT NULL DEFAULT '[]',
    skills_used  TEXT NOT NULL DEFAULT '[]',
    tainted      BOOLEAN NOT NULL DEFAULT 0,
    notified     BOOLEAN NOT NULL DEFAULT 0,
    diagnosed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_reports_task ON diagnosis_reports(task_id);
CREATE INDEX idx_reports_project ON diagnosis_reports(project_key);
```

### 12.3 MySQL 实现

**适用场景**：生产环境、多实例部署、需要外部查询分析。

**依赖**：`github.com/go-sql-driver/mysql`

```sql
-- 故障事件表
CREATE TABLE IF NOT EXISTS incidents (
    id           VARCHAR(64) PRIMARY KEY,
    project_key  VARCHAR(128) NOT NULL,
    title        VARCHAR(512) NOT NULL,
    error_type   VARCHAR(64) NOT NULL DEFAULT '',
    error_msg    TEXT NOT NULL,
    stacktrace   LONGTEXT NOT NULL,
    environment  VARCHAR(32) NOT NULL DEFAULT 'production',
    severity     VARCHAR(16) NOT NULL DEFAULT 'warning',
    url          VARCHAR(1024) NOT NULL DEFAULT '',
    metadata     JSON NOT NULL,
    source       VARCHAR(32) NOT NULL DEFAULT 'custom',
    status       VARCHAR(16) NOT NULL DEFAULT 'pending',
    occurred_at  DATETIME(3) NOT NULL,
    reported_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    INDEX idx_project_key (project_key),
    INDEX idx_status (status),
    INDEX idx_occurred_at (occurred_at),
    INDEX idx_dedup (project_key, error_msg(255), occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 诊断任务表
CREATE TABLE IF NOT EXISTS diagnosis_tasks (
    id           VARCHAR(64) PRIMARY KEY,
    incident_id  VARCHAR(64) NOT NULL,
    project_key  VARCHAR(128) NOT NULL,
    status       VARCHAR(16) NOT NULL DEFAULT 'pending',
    priority     INT NOT NULL DEFAULT 0,
    session_id   VARCHAR(128) NOT NULL DEFAULT '',
    num_turns    INT NOT NULL DEFAULT 0,
    duration_ms  BIGINT NOT NULL DEFAULT 0,
    input_tokens  INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    error        TEXT NOT NULL,
    retry_count  INT NOT NULL DEFAULT 0,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    started_at   DATETIME(3) NULL,
    finished_at  DATETIME(3) NULL,

    INDEX idx_status (status),
    INDEX idx_incident (incident_id),
    INDEX idx_project (project_key),

    FOREIGN KEY (incident_id) REFERENCES incidents(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 诊断报告表
CREATE TABLE IF NOT EXISTS diagnosis_reports (
    id           VARCHAR(64) PRIMARY KEY,
    task_id      VARCHAR(64) NOT NULL,
    incident_id  VARCHAR(64) NOT NULL,
    project_key  VARCHAR(128) NOT NULL,
    project_name VARCHAR(256) NOT NULL DEFAULT '',
    summary      TEXT NOT NULL,
    raw_result   LONGTEXT NOT NULL,
    has_issue    TINYINT(1) NOT NULL DEFAULT 0,
    confidence   VARCHAR(16) NOT NULL DEFAULT 'low',
    tools_used   JSON NOT NULL,
    skills_used  JSON NOT NULL,
    tainted      TINYINT(1) NOT NULL DEFAULT 0,
    notified     TINYINT(1) NOT NULL DEFAULT 0,
    diagnosed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    INDEX idx_task (task_id),
    INDEX idx_project (project_key),
    INDEX idx_diagnosed_at (diagnosed_at),

    FOREIGN KEY (task_id) REFERENCES diagnosis_tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

**连接配置**：

```yaml
store:
  type: "mysql"
  mysql:
    dsn: "user:password@tcp(127.0.0.1:3306)/amp_sentinel?charset=utf8mb4&parseTime=true&loc=Local"
    max_open_conns: 10
    max_idle_conns: 5
    conn_max_lifetime: "5m"
```

### 12.4 JSON 文件实现

**适用场景**：最简部署、调试、临时使用。

### 12.5 方案对比

| 特性 | SQLite | MySQL | JSON 文件 |
|---|---|---|---|
| 部署复杂度 | 零依赖 | 需要 MySQL 服务 | 零依赖 |
| 并发安全 | 单写多读 | 完全支持 | 需要加锁 |
| 查询能力 | SQL 全功能 | SQL 全功能 | 内存过滤 |
| 多实例部署 | ❌ | ✅ | ❌ |
| 适用环境 | 单机 / 开发 | 生产 / 团队 | 调试 / 临时 |

---

## 13. 日志方案

采用 **接口抽象 + 多后端实现** 模式，支持同时启用多个日志后端。

### 13.1 Logger 接口

```go
type Level int

const (
    LevelDebug Level = iota
    LevelInfo
    LevelWarn
    LevelError
)

type Logger interface {
    Debug(msg string, fields ...Field)
    Info(msg string, fields ...Field)
    Warn(msg string, fields ...Field)
    Error(msg string, fields ...Field)
    WithFields(fields ...Field) Logger
    Close() error
}

type Field struct {
    Key   string
    Value any
}

func String(key, val string) Field  { return Field{Key: key, Value: val} }
func Int(key string, val int) Field { return Field{Key: key, Value: val} }
func Err(err error) Field           { return Field{Key: "error", Value: err.Error()} }
```

### 13.2 日志后端

| 后端 | 适用场景 | 输出格式 |
|---|---|---|
| **控制台** | 开发调试 | `2026-02-28 10:00:00 [INFO] incident.received project=order-service severity=critical` |
| **文件日志** | 生产环境 | 同控制台格式，按天自动轮转，可配置保留天数和大小上限 |
| **结构化 JSON** | 日志采集（ELK/Loki） | `{"ts":"...","level":"info","msg":"incident.received","project":"order-service"}` |

### 13.3 Amp 会话日志

每个诊断任务的 Amp 完整流式输出保存为独立文件，用于调试和审计：

```
logs/
├── amp-sentinel-2026-02-28.log             # 系统日志
└── sessions/
    ├── inc-001_order-service_T-xxx.ndjson  # 故障 inc-001 的完整 Amp 对话
    └── inc-002_payment_T-yyy.ndjson       # 故障 inc-002 的完整 Amp 对话
```

### 13.4 日志事件定义

| 事件 | Level | 关键字段 | 触发时机 |
|---|---|---|---|
| `system.started` | INFO | `version`, `store_type`, `concurrency` | 系统启动 |
| `incident.received` | INFO | `incident_id`, `project_key`, `severity` | 收到故障上报 |
| `incident.deduplicated` | INFO | `incident_id`, `project_key`, `original_id` | 去重命中 |
| `incident.rate_limited` | WARN | `incident_id`, `project_key` | 触发限流 |
| `project.not_found` | WARN | `project_key` | 未匹配到注册项目 |
| `source.cloning` | INFO | `project_key`, `repo_url` | 开始克隆源码 |
| `source.pulling` | INFO | `project_key` | 开始更新源码 |
| `source.ready` | INFO | `project_key`, `commit_hash` | 源码就绪 |
| `diagnosis.started` | INFO | `task_id`, `incident_id`, `project_key` | 诊断开始 |
| `diagnosis.tool_use` | DEBUG | `task_id`, `tool_name` | Amp 调用工具 |
| `diagnosis.skill_use` | INFO | `task_id`, `skill_name` | Amp 使用 Skill |
| `diagnosis.completed` | INFO | `task_id`, `has_issue`, `confidence`, `duration_ms` | 诊断完成 |
| `diagnosis.failed` | ERROR | `task_id`, `error` | 诊断失败 |
| `diagnosis.timeout` | WARN | `task_id`, `timeout` | 诊断超时 |
| `security.tainted` | ERROR | `task_id`, `project_key` | 检测到源码被修改 |
| `security.tool_rejected` | WARN | `task_id`, `tool_name` | 拒绝危险工具调用 |
| `feishu.sent` | INFO | `task_id`, `webhook` | 飞书通知发送成功 |
| `feishu.failed` | ERROR | `task_id`, `error` | 飞书通知发送失败 |

### 13.5 文件日志配置

```yaml
logger:
  level: "info"

  console:
    enabled: true
    color: true

  file:
    enabled: true
    dir: "./logs"
    max_size_mb: 100          # 单文件上限
    max_age_days: 30          # 保留天数
    max_backups: 10           # 最大备份数

  structured:
    enabled: false
    path: "./logs/structured.ndjson"

  session:
    enabled: true             # 保存 Amp 原始会话日志
    dir: "./logs/sessions"
```

---

## 14. 配置设计

### 14.1 完整配置示例

```yaml
# config.yaml

# ============================================
# Amp CLI 配置
# ============================================
amp:
  api_key: "${AMP_API_KEY}"
  binary: "amp"
  default_mode: "smart"
  dangerously_allow_all: true

# ============================================
# 调度器配置
# ============================================
scheduler:
  max_concurrency: 3
  queue_size: 100
  default_timeout: "15m"
  retry_count: 2
  retry_delay: "10s"

# ============================================
# 故障接入配置
# ============================================
intake:
  listen: ":8080"
  dedup_window: "10m"           # 去重窗口
  rate_limit_per_hour: 10       # 每项目每小时最多诊断次数
  min_severity: "warning"       # 最低接受的严重程度
  auth_token: "${INTAKE_AUTH_TOKEN}"  # 上报接口的认证 Token

# ============================================
# 项目注册表
# ============================================
projects:
  - key: "order-service"
    name: "订单服务"
    repo_url: "git@github.com:company/order-service.git"
    branch: "main"
    language: "java"
    skills: ["query_order", "query_log", "query_user"]
    owners: ["张三", "李四"]
    feishu_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx-order"

  - key: "payment-gateway"
    name: "支付网关"
    repo_url: "git@github.com:company/payment-gateway.git"
    branch: "main"
    language: "go"
    skills: ["query_log", "query_payment"]
    owners: ["王五"]

  - key: "user-center"
    name: "用户中心"
    repo_url: "git@github.com:company/user-center.git"
    branch: "release"
    language: "python"
    skills: ["query_user", "query_log"]
    owners: ["赵六"]

# ============================================
# 源码管理配置
# ============================================
source:
  base_dir: "/data/repos"            # 源码存放根目录
  git_ssh_key: "${GIT_SSH_KEY_PATH}" # SSH 私钥路径
  max_cache_projects: 50             # 最大缓存项目数
  cleanup_interval: "24h"            # 清理检查间隔

# ============================================
# Skill 配置
# ============================================
skill:
  dir: "./skills"                    # Skill 根目录
  env:                               # Skill 全局环境变量
    ORDER_API_BASE: "${ORDER_API_BASE}"
    ORDER_API_TOKEN: "${ORDER_API_TOKEN}"
    LOG_API_BASE: "${LOG_API_BASE}"
    LOG_API_TOKEN: "${LOG_API_TOKEN}"
    USER_API_BASE: "${USER_API_BASE}"

# ============================================
# 飞书通知配置
# ============================================
feishu:
  default_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx-default"
  timeout: "10s"
  retry_count: 3
  sign_key: "${FEISHU_SIGN_KEY}"     # 签名密钥（可选）

# ============================================
# 持久化配置（三选一）
# ============================================
store:
  type: "sqlite"                     # sqlite / mysql / json

  sqlite:
    path: "./data/sentinel.db"

  mysql:
    dsn: "${MYSQL_DSN}"
    max_open_conns: 10
    max_idle_conns: 5
    conn_max_lifetime: "5m"

  json:
    path: "./data/sentinel.json"
    flush_interval: "5s"

# ============================================
# 日志配置（可同时启用多个）
# ============================================
logger:
  level: "info"

  console:
    enabled: true
    color: true

  file:
    enabled: true
    dir: "./logs"
    max_size_mb: 100
    max_age_days: 30
    max_backups: 10

  structured:
    enabled: false
    path: "./logs/structured.ndjson"

  session:
    enabled: true
    dir: "./logs/sessions"

# ============================================
# 管理 API（可选）
# ============================================
admin_api:
  enabled: true
  listen: ":8081"
```

### 14.2 配置加载优先级

```
命令行参数 > 环境变量 > config.yaml > 默认值
```

---

## 15. HTTP API 设计

系统提供两组 API：**故障接入 API** 和 **管理 API**。

### 15.1 故障接入 API（Intake）

| Method | Path | 说明 | 认证 |
|---|---|---|---|
| `POST` | `/api/v1/incidents` | 上报故障 | Bearer Token |
| `POST` | `/api/v1/incidents/sentry` | Sentry Webhook 适配 | Sentry 签名 |
| `POST` | `/api/v1/incidents/alertmanager` | AlertManager Webhook 适配 | 可选 |

### 15.2 管理 API（Admin）

| Method | Path | 说明 |
|---|---|---|
| `GET` | `/admin/v1/incidents` | 查看故障事件列表 |
| `GET` | `/admin/v1/incidents/:id` | 查看故障事件详情 |
| `GET` | `/admin/v1/tasks` | 查看诊断任务列表 |
| `GET` | `/admin/v1/tasks/:id` | 查看诊断任务详情 |
| `GET` | `/admin/v1/reports/:task_id` | 查看诊断报告 |
| `GET` | `/admin/v1/projects` | 查看注册的项目列表 |
| `POST` | `/admin/v1/incidents/:id/retry` | 重新触发诊断 |
| `GET` | `/admin/v1/stats` | 系统统计 |
| `GET` | `/admin/v1/health` | 健康检查 |

### 15.3 请求/响应示例

**上报故障**：

```bash
POST /api/v1/incidents
Authorization: Bearer <intake_auth_token>
Content-Type: application/json

{
  "project_key": "order-service",
  "title": "订单创建接口 500 错误",
  "error_type": "exception",
  "error_msg": "NullPointerException: Cannot invoke method getPrice() on null object",
  "stacktrace": "at com.example.order.service.OrderService.createOrder(OrderService.java:128)\n...",
  "environment": "production",
  "severity": "critical",
  "metadata": { "user_id": "12345", "order_no": "ORD20260228001" },
  "source": "sentry",
  "occurred_at": "2026-02-28T10:00:00Z"
}
```

**响应**：

```json
{
  "incident_id": "inc-a1b2c3d4",
  "task_id": "task-e5f6g7h8",
  "status": "queued",
  "message": "故障已受理，正在排队等待诊断"
}
```

**查看诊断报告**：

```json
{
  "task_id": "task-e5f6g7h8",
  "incident_id": "inc-a1b2c3d4",
  "project": { "key": "order-service", "name": "订单服务" },
  "status": "completed",
  "report": {
    "summary": "订单创建时因商品价格为 null 导致 NullPointerException",
    "has_issue": true,
    "confidence": "high",
    "raw_result": "经过分析..."
  },
  "session_id": "T-xxx",
  "duration_ms": 25000,
  "num_turns": 4,
  "usage": { "input_tokens": 45000, "output_tokens": 3200 },
  "notified": true,
  "diagnosed_at": "2026-02-28T10:01:25Z"
}
```

**系统统计**：

```json
{
  "uptime_seconds": 86400,
  "incidents": { "total": 156, "today": 12 },
  "tasks": { "pending": 2, "running": 1, "completed": 140, "failed": 5 },
  "projects": { "registered": 8, "active_today": 4 },
  "tokens": { "total_input": 5200000, "total_output": 380000 },
  "feishu": { "sent": 145, "failed": 2 }
}
```

---

## 16. 关键数据结构

### 16.1 诊断任务

```go
type TaskStatus string

const (
    StatusPending   TaskStatus = "pending"
    StatusQueued    TaskStatus = "queued"
    StatusCloning   TaskStatus = "cloning"     // 正在拉取源码
    StatusRunning   TaskStatus = "running"     // Amp 正在诊断
    StatusVerifying TaskStatus = "verifying"   // 正在校验源码无变更
    StatusNotifying TaskStatus = "notifying"   // 正在推送飞书
    StatusCompleted TaskStatus = "completed"
    StatusFailed    TaskStatus = "failed"
    StatusTimeout   TaskStatus = "timeout"
    StatusRetrying  TaskStatus = "retrying"
)

type DiagnosisTask struct {
    ID          string
    IncidentID  string
    ProjectKey  string
    Status      TaskStatus
    Priority    int
    SessionID   string
    NumTurns    int
    DurationMs  int64
    Usage       TokenUsage
    Error       string
    RetryCount  int
    CreatedAt   time.Time
    StartedAt   time.Time
    FinishedAt  time.Time
}

type TokenUsage struct {
    InputTokens              int
    OutputTokens             int
    CacheCreationInputTokens int
    CacheReadInputTokens     int
}
```

---

## 17. 技术选型

| 组件 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.22+ | 原生并发、静态编译、适合服务端 |
| 进程管理 | `os/exec` + `context` | 标准库，支持超时和取消 |
| JSON 解析 | `encoding/json` | 标准库，NDJSON 逐行解析 |
| 并发控制 | `chan struct{}` semaphore | 轻量，无第三方依赖 |
| HTTP 框架 | `net/http`（标准库） | 接口简单，无需额外框架 |
| SQLite | `modernc.org/sqlite` | 纯 Go，无 CGO |
| MySQL | `github.com/go-sql-driver/mysql` | 社区标准驱动 |
| 配置解析 | `gopkg.in/yaml.v3` | YAML 格式 |
| UUID | `github.com/google/uuid` | ID 生成 |
| 日志轮转 | `gopkg.in/natefinish/lumberjack.v2` | 文件日志自动轮转 |
| Git 操作 | `os/exec` 调用 `git` CLI | 简单直接，无需 Go git 库 |
| HTTP Client | `net/http`（标准库） | 飞书 Webhook 推送 |

---

## 18. 开发阶段规划

### Phase 1 — 基础设施（P0 🔴）

| 模块 | 内容 | 预估 |
|---|---|---|
| `amp/types.go` | Stream JSON 消息类型定义 | 0.5 天 |
| `amp/client.go` | CLI 封装，单次执行 + 流式解析 | 1 天 |
| `amp/permission.go` | 只读权限规则生成 | 0.5 天 |
| `logger/` | Logger 接口 + 控制台实现 | 0.5 天 |
| `main.go` | 最小可运行 demo（手动触发单次诊断） | 0.5 天 |

### Phase 2 — 核心诊断流程（P0 🔴）

| 模块 | 内容 | 预估 |
|---|---|---|
| `project/registry.go` | 项目注册表（YAML 配置加载） | 0.5 天 |
| `project/source.go` | 源码 clone/pull + 只读保护 | 1 天 |
| `diagnosis/engine.go` | 诊断流程编排（源码准备 → Amp 调用 → 结果校验） | 1.5 天 |
| `diagnosis/prompt.go` | Prompt + AGENTS.md 动态生成 | 1 天 |
| `diagnosis/report.go` | 诊断报告解析 | 0.5 天 |

### Phase 3 — 接入与通知（P1 🟡）

| 模块 | 内容 | 预估 |
|---|---|---|
| `intake/` | 故障上报 API + 去重 + 限流 | 1 天 |
| `notify/feishu.go` | 飞书 Webhook + 消息卡片模板 | 1 天 |
| `scheduler/` | Worker pool + 优先级队列 + 并发控制 | 1.5 天 |

### Phase 4 — 持久化与日志（P1 🟡）

| 模块 | 内容 | 预估 |
|---|---|---|
| `store/store.go` | Store 接口定义 | 0.5 天 |
| `store/sqlite.go` | SQLite 实现 | 1 天 |
| `store/mysql.go` | MySQL 实现 | 1 天 |
| `logger/file.go` | 文件日志（轮转） | 0.5 天 |
| `logger/structured.go` | 结构化 JSON 日志 | 0.5 天 |

### Phase 5 — Skill 系统与管理（P2 🟢）

| 模块 | 内容 | 预估 |
|---|---|---|
| `skill/` | Skill 加载、注册、生命周期管理 | 1 天 |
| `skill/builtin/` | 内置 Skill 示例（query_log, query_order） | 1 天 |
| `api/server.go` | 管理 API | 1 天 |
| 配置加载 | YAML + 环境变量 完整实现 | 0.5 天 |

### Phase 6 — 生产加固（P3 ⚪）

| 模块 | 内容 | 预估 |
|---|---|---|
| Sentry 适配 | Sentry Webhook → 统一格式转换 | 0.5 天 |
| AlertManager 适配 | AlertManager Webhook 适配 | 0.5 天 |
| `store/json.go` | JSON 文件实现 | 0.5 天 |
| 指标监控 | Prometheus metrics（可选） | 1 天 |
| Graceful Shutdown | 优雅停机 + 任务排空 | 0.5 天 |

---

## 19. 注意事项

### 19.1 安全相关

1. **只读铁律**：四层防护机制（Amp Permissions + Prompt 约束 + 文件系统权限 + 结果校验）缺一不可
2. **API Key 安全**：`AMP_API_KEY`、`INTAKE_AUTH_TOKEN`、`FEISHU_SIGN_KEY` 等敏感信息通过环境变量注入，不硬编码
3. **Skill 安全**：所有 Skill 工具只做查询，不提供写入能力；使用独立的只读 API Token
4. **Git SSH Key**：SSH 私钥权限设为 600，不暴露到日志

### 19.2 运维相关

5. **计费**：`amp -x` 模式只消耗付费额度，不消耗免费额度，注意监控 Token 消耗
6. **并发限制**：初始并发数建议设为 3，观察 Amp 平台 rate limit 后调整
7. **磁盘空间**：源码缓存 + 会话日志可能占用较大空间，配置定期清理
8. **进程清理**：超时或取消时，必须正确终止 Amp 子进程（`cmd.Process.Kill()`）

### 19.3 业务相关

9. **去重窗口**：避免同一故障短时间内反复触发诊断，浪费 Token
10. **诊断时间**：复杂项目的诊断可能需要 5-15 分钟，超时时间不宜设太短
11. **飞书限流**：飞书 Webhook 有频率限制（默认每分钟 5 条），密集告警时注意合并
12. **结论明确**：无论是否定位到问题，都必须给出明确结论，避免模糊回答

# 诊断流程详解

> 本文档详细描述 Amp Sentinel 从接收事件到输出诊断报告的完整流程。
>
> 对应代码入口：[`diagnosis/engine.go → Diagnose()`](diagnosis/engine.go)

---

## 目录

- [1. 流程总览](#1-流程总览)
- [2. 阶段 1：项目查找](#2-阶段-1项目查找)
- [3. 阶段 2：指纹复用检查（P1）](#3-阶段-2指纹复用检查p1)
- [4. 阶段 3：源码准备](#4-阶段-3源码准备)
- [5. 阶段 4：Prompt 构建](#5-阶段-4prompt-构建)
- [6. 阶段 5：Amp AI 执行](#6-阶段-5amp-ai-执行)
- [7. 阶段 6：安全校验（只读铁律）](#7-阶段-6安全校验只读铁律)
- [8. 阶段 7：结构化输出解析（P0）](#8-阶段-7结构化输出解析p0)
- [9. 阶段 8：质量评分（P0）](#9-阶段-8质量评分p0)
- [10. 阶段 9：报告生成](#10-阶段-9报告生成)
- [11. 诊断后处理](#11-诊断后处理)
- [12. 并发与锁机制](#12-并发与锁机制)
- [13. 配置参考](#13-配置参考)

---

## 1. 流程总览

```
事件接入
  │
  ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Diagnose() 主流程                             │
│                                                                     │
│  ① 项目查找 ──▶ ② 指纹复用检查 ──┬── 命中 ──▶ 返回缓存报告          │
│                                   │                                  │
│                                   └── 未命中                         │
│                                        │                             │
│  ③ 获取项目锁 + 源码准备 ──▶ ④ Prompt 构建 ──▶ ⑤ Amp AI 执行        │
│                                                       │              │
│  ⑥ 安全校验（只读铁律）◀────────────────────────────────┘              │
│       │                                                              │
│       ▼                                                              │
│  ⑦ 结构化输出解析 ──▶ 代码位置验证 ──▶ 释放项目锁                     │
│       │                                                              │
│       ▼                                                              │
│  ⑧ 质量评分 ──▶ ⑨ 报告生成                                           │
└─────────────────────────────────────────────────────────────────────┘
  │
  ▼
飞书通知 + 持久化
```

---

## 2. 阶段 1：项目查找

**代码位置**：`engine.go` 步骤 1

通过 `Registry.Lookup(projectKey)` 查找项目配置，获取仓库地址、分支、语言、Skills、负责人、飞书 Webhook 等信息。

若项目未注册，直接返回错误，不进入后续流程。

---

## 3. 阶段 2：指纹复用检查（P1）

**代码位置**：`engine.go` 步骤 2 + `fingerprint.go`

**开关**：`diagnosis.fingerprint_reuse_enabled: true`

### 目标

避免对相同故障重复执行完整 AI 诊断（15 分钟 + 大量 Token 消耗），直接复用历史高质量报告。

### 指纹计算

```
ComputeDiagnosisFingerprint(projectKey, payload, dedupFields, defaultDedupFields)
```

1. **值归一化** — 将 payload 中的动态内容替换为占位符：
   - 时间戳 → `<TS>`
   - UUID → `<UUID>`
   - 内存地址 → `<ADDR>`
   - 长数字 → `<N>`
   - 全部转小写
2. **字段提取** — 按项目配置的 `dedup.fields` 或系统默认字段 (`error_msg`, `error`, `message`, `msg`) 提取关键内容
3. **SHA-256 哈希** — 对 `projectKey + 归一化字段值` 计算指纹
4. **环境隔离** — 追加 `environment` 字段值，防止跨环境复用

### 复用判定 (`canReuse`)

| 条件 | 说明 |
|---|---|
| 报告未被污染 (`tainted=false`) | 被标记为源码变更的报告不可复用 |
| 非 `insufficient_information` | 信息不足的结论不可复用 |
| 质量分 ≥ `min_score`（默认 80） | 低质量报告不可复用 |
| 无幻觉标记 | 含 `HALLUCINATED_FILE` 或 `HALLUCINATED_LINE` 标记的不可复用 |
| Commit 一致性 | Critical 事件要求代码版本完全匹配；非 Critical 允许不匹配但标记 `REUSED_STALE_COMMIT` |

### 复用流程

1. 计算指纹
2. 查询存储层 `FindRecentReportByFingerprint(projectKey, fingerprint, since)`
3. 若命中，**短暂获取项目锁** 拉取最新代码获取 commit hash（不占用长时间锁）
4. 执行 `canReuse()` 判定
5. 通过 → 构建复用报告返回，`duration_ms=0`，`reused_from_id` 指向原始报告

---

## 4. 阶段 3：源码准备

**代码位置**：`engine.go` 步骤 3-4 + `project/source.go`

### 获取项目锁

通过 `SourceManager.Lock(projectKey)` 获取**项目级互斥锁**，确保同一项目不会有两个并发诊断操作（不同项目之间不互斥）。

### 源码获取

```
SourceManager.Prepare(ctx, project) → srcDir
```

- 首次：`git clone --depth 1 --single-branch` 到 `data/repos/{project_key}/`
- 后续：`git fetch + git reset --hard origin/{branch}` 更新到最新代码
- 拉取失败时自动删除并重新 clone
- 使用配置的 SSH Key 进行 Git 认证

### 获取 Commit Hash

记录当前 commit hash，用于：
- 指纹复用时的代码版本比较
- 写入诊断报告供审计追溯

---

## 5. 阶段 4：Prompt 构建

**代码位置**：`prompt.go`

### AGENTS.md 注入

动态生成 AGENTS.md 内容，**直接拼入 prompt 而非写入源码目录**（写文件会触发安全校验误判）：

```
┌─────────────────────────────────┐
│ 🔴 安全约束（最高优先级）        │
│ - 禁止修改/创建任何文件          │
│ - 禁止执行 git commit/push      │
│ - 只能使用只读工具              │
├─────────────────────────────────┤
│ 项目信息                        │
│ - 项目名、语言、分支             │
├─────────────────────────────────┤
│ 事件信息                        │
│ - 来源、严重程度、接收时间       │
├─────────────────────────────────┤
│ 可用 Skill 列表                 │
├─────────────────────────────────┤
│ 输出格式要求                    │
│ - 必须输出结构化 JSON            │
└─────────────────────────────────┘
```

### 主 Prompt

1. 角色设定：线上故障诊断专家
2. **安全提示**：事件数据为不可信输入，防止 prompt injection
3. 事件原始数据（payload JSON），超过 64KB 自动截断（保持 UTF-8 完整性）
4. 分析指引：可使用 Read/Grep/finder 读代码、git log/blame 查历史、Skill 查业务数据
5. **输出 JSON Schema**：完整的结构化输出格式定义和字段约束

### 结构化输出 Schema

```json
{
  "schema_version": "v1",
  "summary": "一句话故障摘要（≤200字符）",
  "conclusion": {
    "has_issue": true,
    "confidence": 0.85,
    "confidence_label": "high|medium|low"
  },
  "root_causes": [{
    "rank": 1,
    "hypothesis": "根本原因描述",
    "evidence": [{ "type": "code|log|stack|config", "detail": "...", "file": "...", "line_start": 123 }],
    "counter_evidence": ["反面证据"],
    "verification_steps": ["验证步骤"]
  }],
  "code_locations": [{ "file": "...", "line_start": 123, "line_end": 140, "reason": "..." }],
  "remediations": ["修复建议"],
  "next_actions": ["进一步排查建议"],
  "non_code_factors": ["非代码因素"]
}
```

---

## 6. 阶段 5：Amp AI 执行

**代码位置**：`engine.go` 步骤 5 + `amp/client.go`

### 执行方式

```bash
amp --execute "<prompt>" \
    --stream-json \
    --dangerously-allow-all \
    --mode smart
```

通过 `amp.Client.Execute()` 启动 Amp CLI 子进程，传入：

| 参数 | 值 |
|---|---|
| `WorkDir` | 项目源码目录 |
| `Mode` | `smart` / `rush` / `deep`（可配置） |
| `Permissions` | 只读权限规则（见安全校验） |
| `MCPServers` | 项目关联的 Skill MCP 服务器 |
| `Labels` | `["sentinel", projectKey, severity]` |

### 流式处理

通过 NDJSON 流式回调处理 Amp 输出：

- **会话日志保存**：每条消息写入 `logs/sessions/{event_id}_{project_key}_{timestamp}.ndjson`
- **Skill 使用追踪**：监听 `tool_use` 类型消息，记录 Skill 调用

### 超时控制

由调度器层 `context.WithTimeout` 控制（默认 15 分钟），超时后自动终止 Amp 进程。

---

## 7. 阶段 6：安全校验（只读铁律）

**代码位置**：`engine.go` 步骤 6 + `amp/permission.go`

### 四层防护机制

```
┌──────────────────────────────────────────────────────────────┐
│ 第 1 层：Amp Permissions                                     │
│ 通过 --permissions 参数注入规则：                              │
│ - allow: Read, Grep, glob, finder, git log/blame...          │
│ - reject: edit_file, create_file, rm, git commit/push...     │
│ - reject: Task, handoff（防止子代理写入升级）                  │
│ - 兜底: reject Bash（未明确允许的 Bash 命令全部拒绝）          │
├──────────────────────────────────────────────────────────────┤
│ 第 2 层：Prompt 约束                                         │
│ AGENTS.md 中明确声明只读规则，让 AI 理解不应修改代码           │
├──────────────────────────────────────────────────────────────┤
│ 第 3 层：文件系统权限（可选）                                  │
│ 源码目录可设置为只读文件系统权限                               │
├──────────────────────────────────────────────────────────────┤
│ 第 4 层：结果校验（Fail-closed）                              │
│ Amp 执行完毕后检测源码是否被修改：                             │
│ - git status --porcelain 检测变更                             │
│ - 若发现变更 → 标记 tainted + 自动 git reset --hard 回滚      │
│ - 若检测本身失败 → 也标记 tainted（fail-closed）              │
│ - 使用独立 context（30s 超时），不受诊断 context 取消影响      │
└──────────────────────────────────────────────────────────────┘
```

### Tainted 标记

一旦 `tainted=true`：
- 报告中明确标注
- 飞书通知卡片显示安全告警
- 指纹复用时该报告不可被复用

---

## 8. 阶段 7：结构化输出解析（P0）

**代码位置**：`structured.go` + `fixer.go`

**开关**：`diagnosis.structured_output: true`

### 解析策略（三层递进）

```
┌──────────────────────────────────────────────────────────────┐
│ 尝试 1：提取 ```json 代码块 → JSON 解析 + 验证               │
│                    │                                          │
│                    ├── 成功 → 返回结构化结果                   │
│                    └── 失败 ↓                                 │
│                                                               │
│ 尝试 2：本地确定性修复                                        │
│  - 移除尾部逗号（respecting string boundaries）              │
│  - 修复未闭合的字符串（补 "）                                 │
│  - 补全未闭合的括号（{ } [ ]）                                │
│  - 提取裸 JSON 对象（无代码块包裹）                           │
│                    │                                          │
│                    ├── 成功 → 返回结构化结果                   │
│                    └── 失败 ↓                                 │
│                                                               │
│ 尝试 3：LLM JSON 修复器（兜底，需开启 json_fixer_enabled）    │
│  - 启动新的 Amp rush 模式会话                                 │
│  - 30 秒超时，4096 token 上限                                 │
│  - 仅修复语法错误，不修改语义                                 │
│                    │                                          │
│                    ├── 成功 → 返回结构化结果                   │
│                    └── 失败 → 回退到启发式路径                 │
└──────────────────────────────────────────────────────────────┘
```

### 验证规则

解析成功后执行验证 (`validateDiagnosisJSON`)：

| 校验项 | 规则 |
|---|---|
| `summary` | 必填，超过 200 字符自动截断 |
| `confidence` | 必须在 0.0~1.0 之间 |
| `confidence_label` | 必须为 `high`/`medium`/`low`，不匹配时根据数值自动修正 |
| `root_causes` | 至少 1 项 |
| `evidence.type` | 必须为 `code`/`log`/`stack`/`config`，无效值自动修正为 `log` 并标记 `AUTO_FIXED_EVIDENCE_TYPE` |

### 代码位置验证

**在项目锁内执行**（需要读取源码目录）：

```
VerifyCodeLocations(srcDir, code_locations)
```

对每个 AI 引用的代码位置：
1. 防路径遍历：拒绝绝对路径和 `../` 相对路径
2. 文件存在性：`os.Stat()` 检查文件是否存在
3. 行号有效性：读取文件行数，验证 `line_start` 和 `line_end` 不超出范围

输出：
- `score`（0-20）：`已验证数 / 总引用数 * 20`
- `flags`：`HALLUCINATED_FILE`（文件不存在）、`HALLUCINATED_LINE`（行号越界）

特殊逻辑：若 evidence 包含 `code` 类型但 `code_locations` 为空，CodeVerify 强制为 0（惩罚遗漏）。

### 回退路径

结构化解析全部失败时，回退到**启发式检测**（旧路径）：
- `detectHasIssue()` — 6 个关键词判断是否定位到问题
- `detectConfidence()` — 13 个关键词判断置信度等级
- `extractSummary()` — 截取前 200 字符作为摘要

---

## 9. 阶段 8：质量评分（P0）

**代码位置**：`scoring.go`

**在项目锁外执行**（纯计算，无 I/O）。

### 六维评分体系

采用**动态最大分制**：不适用的维度标记 N/A（-1），从分母中排除。

| 维度 | 满分 | 评分规则 |
|---|---|---|
| **Schema** | 20 | 结构完整性：summary(5) + confidence 范围(5) + label 有效(3) + root_causes 非空(5) + evidence type 无自动修正(2) |
| **Evidence** | 20 | 证据质量：有证据(10) + 有详细证据（>30 字符或含文件引用）(10)。`insufficient_information` 时改评 verification_steps |
| **CodeVerify** | 20 或 N/A | 代码位置验证得分（由阶段 7 计算）。无代码引用时 N/A |
| **Coherence** | 15 | 内部一致性：has_issue+root_causes 一致(8) + high confidence 需 ≥2 条证据(7) |
| **Actionable** | 15 | 修复建议质量：有 remediations(8) + 详细描述（>20 字符）(7) |
| **NonCodePath** | 10 或 N/A | 非代码因素说明质量。仅当 code_locations 为空时计入 |

### 归一化得分

```
Normalized = 实际总分 / MaxPossible × 100
```

示例：
- 全维度适用（MaxPossible=100）：80/100 = 80 分
- CodeVerify N/A（MaxPossible=80）：64/80 = 80 分
- CodeVerify + NonCodePath 均 N/A（MaxPossible=70）：56/70 = 80 分

### 质量标记 (Flags)

| 标记 | 含义 |
|---|---|
| `SCHEMA_INVALID` | 结构化解析完全失败 |
| `NO_EVIDENCE` | 所有 root_causes 均无 evidence（且非 insufficient_information） |
| `HALLUCINATED_FILE` | AI 引用了不存在的文件 |
| `HALLUCINATED_LINE` | AI 引用了超出文件行数的行号 |
| `HIGH_CONF_NO_SUPPORT` | 高置信度但证据不足（<2 条） |
| `NO_CONCLUSION` | 缺少结论 |
| `EMPTY_REMEDIATION` | 无修复建议 |
| `AUTO_FIXED_EVIDENCE_TYPE` | evidence.type 为无效值，已自动修正 |
| `REUSED_STALE_COMMIT` | 复用报告的代码版本不同（仅复用路径） |

---

## 10. 阶段 9：报告生成

**代码位置**：`engine.go` 步骤 10 + `report.go`

### 报告字段

```go
type Report struct {
    IncidentID     string          // 关联事件 ID
    ProjectKey     string          // 项目标识
    ProjectName    string          // 项目名称
    Summary        string          // 一句话摘要
    RawResult      string          // AI 原始输出
    HasIssue       bool            // 是否定位到问题
    Confidence     string          // 置信度标签 (high/medium/low)
    SessionID      string          // Amp 会话 ID
    DurationMs     int64           // 诊断耗时（毫秒）
    NumTurns       int             // AI 执行轮次
    ToolsUsed      []string        // 使用的工具列表
    SkillsUsed     []string        // 使用的 Skill 列表
    Tainted        bool            // 安全标记（源码是否被修改）
    Usage          *UsageInfo      // Token 消耗

    // 结构化输出
    StructuredResult *DiagnosisJSON  // 结构化诊断结果
    QualityScore     QualityScore    // 质量评分

    // 置信度
    OriginalConfidence float64     // AI 原始置信度
    FinalConfidence    float64     // 最终置信度
    FinalConfLabel     string      // 最终置信度标签

    // 指纹复用
    Fingerprint   string           // 事件指纹
    CommitHash    string           // 诊断时的代码版本
    ReusedFromID  string           // 复用的原始报告 ID

    // 版本追踪
    PromptVersion string           // Prompt 版本（用于 A/B 测试）
}
```

### 三种生成路径

| 路径 | 条件 | 行为 |
|---|---|---|
| **异常路径** | Amp 执行报错 (`result.IsError`) | summary 设为错误信息，confidence=low |
| **结构化路径** | 解析成功 (`structuredDiag != nil`) | 从结构化结果提取所有字段 |
| **启发式路径** | 结构化解析失败 | 回退到关键词启发式检测 |

---

## 11. 诊断后处理

**代码位置**：`main.go` 中的 `diagnoseFn`

报告生成后，由 `main.go` 中的调度回调完成后续操作：

### 11.1 飞书通知

```
FeishuNotifier.Notify(ctx, project, event, report)
```

- 使用独立 context（30 秒超时），不受诊断 context 取消影响
- 发送富文本卡片消息到项目配置的飞书 Webhook
- 卡片包含：摘要、置信度、是否定位到问题、Tainted 告警、Dashboard 链接
- 支持签名验证和重试（默认 3 次）

### 11.2 持久化

使用独立 context（10 秒超时）写入存储层：

1. **保存报告** — `SaveReport(storeReport)` — 包含结构化结果、质量评分、指纹等
2. **更新事件状态** — `UpdateEvent(status="completed")`
3. **更新任务状态** — `UpdateTask(status="completed", session_id, duration_ms, token_usage)`

### 11.3 调度器重试

若 `Diagnose()` 返回错误，调度器按配置自动重试：

- 默认重试 2 次，间隔 10 秒
- 重试期间检查 scheduler context，若正在关停则放弃
- 所有重试失败后标记任务为 `failed`

---

## 12. 并发与锁机制

### 项目级锁

```
SourceManager.Lock(projectKey) → unlock()
```

- **粒度**：每个项目一把互斥锁，不同项目之间完全并行
- **持有范围**：源码准备 → Amp 执行 → 安全校验 → 代码位置验证
- **释放时机**：
  - 结构化输出模式：代码位置验证后**显式释放**，质量评分在锁外执行
  - 非结构化模式：安全校验后通过 `defer` 释放

### 指纹复用的锁优化

指纹复用检查在获取主锁**之前**执行：
- Store 查询不需要锁
- 仅在需要获取 commit hash 时**短暂**获取锁（Prepare + CommitHash），然后立即释放
- 避免持有长时间锁进行网络 I/O

### 调度器并发

- Worker pool 模型，默认 3 个并发 worker
- 优先级队列：Critical(100) > Warning(50) > Info(10)
- 每个 worker 独立从队列取任务执行

---

## 13. 配置参考

```yaml
diagnosis:
  # P0: 结构化输出
  structured_output: true       # 是否启用结构化 JSON 输出 + 质量评分
  json_fixer_enabled: true      # 是否启用 LLM JSON 修复器（兜底）
  prompt_version: "v1"          # Prompt 版本标签（用于 A/B 测试）

  # P1: 指纹复用
  fingerprint_reuse_enabled: true    # 是否启用指纹复用
  fingerprint_reuse_window: "24h"    # 回溯窗口（默认 24 小时）
  fingerprint_reuse_min_score: 80    # 复用最低质量分（默认 80）

amp:
  default_mode: "smart"          # Amp 执行模式：smart / rush / deep

scheduler:
  max_concurrency: 3             # 最大并发诊断数
  default_timeout: "15m"         # 单次诊断超时
  retry_count: 2                 # 失败重试次数
  retry_delay: "10s"             # 重试间隔
```

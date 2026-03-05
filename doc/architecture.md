# vagent 架构文档

## 1. 项目概述

### 定位

vagent 是一个 Go 语言 Agent 开发框架，用于构建基于大语言模型（LLM）的智能代理系统。支持嵌入现有系统或独立部署。

### 目标

- 提供灵活的 Agent 抽象，支持多种 Agent 类型和编排模式
- 通过 `github.com/vogo/aimodel` 统一调用多家大模型（OpenAI、Anthropic、Gemini 等）
- 支持 MCP（Model Context Protocol）协议，实现工具互操作
- 最小外部依赖（仅依赖 `aimodel` 和 `github.com/modelcontextprotocol/go-sdk`）
- 可嵌入、可独立部署

### 设计原则

| 原则         | 说明                                           |
| ------------ | ---------------------------------------------- |
| 组合优于继承 | 通过接口组合构建复杂 Agent，避免深层继承       |
| 最小依赖     | 仅依赖 `aimodel` 和 `mcp/go-sdk`，不引入 ORM、Web 框架等 |
| 上下文驱动   | 所有操作通过 `context.Context` 传递，支持取消  |
| 可观测       | 内建 Hook 机制，支持事件追踪和轨迹采集         |
| 协议兼容     | 遵循 MCP 协议规范，与外部工具生态互通          |
| 安全优先     | 内建 Guardrails 机制，输入输出安全检查前置     |

---

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Application Layer                          │
│                  (嵌入式调用 / HTTP API / CLI)                     │
├─────────────────────────────────────────────────────────────────────┤
│                          Service Layer                             │
│              (配置管理 / HTTP Server / 生命周期管理)               │
├────────────────────────────────────────────────────────────────────┤
│                        Guardrails Layer                            │
│            (InputGuard / OutputGuard / 安全检查链)                 │
├────────────────┬────────────────┬───────────────────────────────────┤
│   Agent Layer  │  Memory Layer  │         Tool Layer               │
│  ┌───────────┐ │ ┌────────────┐ │  ┌────────────┐ ┌─────────────┐ │
│  │ LLMAgent  │ │ │  Working   │ │  │ Tool Reg.  │ │ MCP Client  │ │
│  │ Workflow  │ │ │  Session   │ │  │ Tool Exec. │ │ MCP Server  │ │
│  │ Router    │ │ │  Store     │ │  │ Built-in   │ │ Transport   │ │
│  │ DAG       │ │ └────────────┘ │  └────────────┘ └─────────────┘ │
│  │ Custom    │ │                │                                  │
│  └───────────┘ │                │                                  │
├────────────────┴────────────────┴───────────────────────────────────┤
│                       Hook / Observability                         │
│              (Hook / 轨迹采集 / 日志 / 指标)                       │
├─────────────────────────────────────────────────────────────────────┤
│                     外部依赖 (aimodel / mcp go-sdk)                │
│  (ChatCompleter / ComposeClient / Stream / Protocol / MCP)         │
└─────────────────────────────────────────────────────────────────────┘
```

### 层次说明

| 层级           | 职责                                                 |
| -------------- | ---------------------------------------------------- |
| Application    | 对外入口：嵌入式 API 调用、HTTP 接口、命令行工具     |
| Service        | 配置加载、Server 启停、Agent 注册与生命周期管理      |
| Guardrails     | 安全检查层，输入输出过滤、内容审核、注入防护         |
| Agent          | 核心智能体逻辑，包含多种 Agent 类型和编排策略        |
| Memory         | 上下文管理，三级内存架构                             |
| Tool           | 工具定义、注册、执行，MCP 协议集成                   |
| Hook           | 横切关注点，事件驱动的可观测性                       |
| aimodel        | 底层大模型调用，提供 ChatCompleter 统一接口、Protocol 协议分发、ComposeClient 多模型调度 |
| mcp/go-sdk     | MCP 协议 Go SDK，提供 MCP 协议基础实现               |

---

## 3. 模块结构

```
vagent/
├── agent/          # Agent 核心定义与各类型实现
├── guard/          # Guardrails 安全检查（输入/输出过滤）
├── prompt/         # Prompt 模板引擎（变量插值/条件渲染/版本管理）
├── largemodel/     # LLM 调用中间件链（重试/缓存/限流/降级）
├── memory/         # 三级内存系统
├── tool/           # 工具注册与执行
├── mcp/            # MCP 协议支持（Client + Server）
│   ├── client/     # MCP Client — 消费外部工具
│   ├── server/     # MCP Server — 暴露 Agent 能力
│   └── transport/  # 传输层（stdio / SSE / Streamable HTTP）
├── hook/           # Hook 与可观测性
├── orchestrate/    # 编排引擎（顺序/并行/条件/DAG）
├── service/        # 服务层（配置/HTTP/生命周期）
├── eval/           # 评估框架（质量回归测试/评分）
├── schema/         # 共享数据模型定义
└── doc/            # 文档
```

### 包依赖关系

```
service ──→ guard ──→ agent ──→ memory
  │                     │──→ tool ──→ mcp/client
  │                     │──→ hook
  │                     │──→ orchestrate
  │                     │──→ prompt
  │                     └──→ largemodel ──→ aimodel
  │
  └──→ mcp/server ──→ agent
              │──→ mcp/transport

agent, tool, largemodel ──→ aimodel (ChatCompleter / ComposeClient)
mcp/* ──→ github.com/modelcontextprotocol/go-sdk (外部)
schema ←── (所有包共享)
```

### 各包职责

| 包            | 职责                                                   |
| ------------- | ------------------------------------------------------ |
| `schema`      | 共享模型定义：Message、Event、ToolDef 等               |
| `guard`       | Guardrails 安全层：输入/输出检查链、内容审核、注入防护 |
| `prompt`      | Prompt 模板引擎：变量插值、条件渲染、模板版本管理      |
| `largemodel`  | LLM 调用中间件链：重试、缓存、限流、降级、日志         |
| `agent`       | Agent 接口、基础实现、各类型 Agent                     |
| `memory`      | Working/Session/Store 三级内存接口与实现               |
| `tool`        | 工具注册表、工具执行器、与 aimodel ToolCall 的桥接     |
| `mcp/client`  | MCP 客户端，发现并调用外部 MCP 服务暴露的工具         |
| `mcp/server`  | MCP 服务端，将 Agent 能力通过 MCP 协议暴露             |
| `mcp/transport` | MCP 传输层抽象（stdio、SSE、Streamable HTTP）        |
| `hook`        | Hook 定义与分发，支持轨迹采集                          |
| `orchestrate` | 多 Agent 编排引擎，支持顺序/并行/条件/DAG 模式        |
| `service`     | 服务配置、HTTP 路由、Server 启停管理                   |
| `eval`        | 评估框架：EvalCase/EvalResult、内建评估器              |

---

## 4. 核心模型定义

### 4.1 Message

表示一条对话消息。直接复用 `aimodel.Message` 结构，增加 Agent 层元数据。

`aimodel.Message` 核心字段（直接复用）：

| 字段         | 类型              | 说明                                       |
| ------------ | ----------------- | ------------------------------------------ |
| Role         | aimodel.Role      | 消息角色：system / user / assistant / tool |
| Content      | aimodel.Content   | 消息内容（纯文本或多模态，支持 `NewTextContent` / `NewPartsContent`）|
| ToolCallID   | string            | 工具调用结果对应的 ToolCall ID             |
| ToolCalls    | []aimodel.ToolCall| 模型返回的工具调用请求列表                 |

vagent 扩展字段：

| 字段         | 类型       | 说明                                       |
| ------------ | ---------- | ------------------------------------------ |
| AgentID      | string     | 产生该消息的 Agent 标识                    |
| Timestamp    | int64      | 消息创建时间戳                             |
| Metadata     | map        | 扩展元数据（token 用量、延迟等）           |

> `aimodel.Content` 支持多模态：纯文本通过 `NewTextContent(text)` 创建，图文混合通过 `NewPartsContent(parts...)` 创建。序列化时纯文本为 JSON string，多模态为 JSON array。

### 4.2 Agent

Agent 是核心智能体抽象。

| 字段           | 类型         | 说明                                     |
| -------------- | ------------ | ---------------------------------------- |
| ID             | string       | 唯一标识                                 |
| Name           | string       | 人类可读名称                             |
| Description    | string       | Agent 能力描述，用于路由和工具暴露       |
| SystemPrompt   | PromptTemplate | 系统提示词模板（支持变量插值）         |
| Model          | string       | 使用的模型标识                           |
| Tools          | []ToolDef    | Agent 可使用的工具列表                   |
| Memory         | Memory       | Agent 关联的内存实例                     |
| MaxIterations  | int          | 最大推理-行动循环次数                    |
| MaxTokenBudget | int          | 单次 Run 最大 token 数（0 表示不限制）   |
| Temperature    | *float64     | 采样温度                                 |
| InputGuards    | []InputGuard | 输入安全检查链                           |
| OutputGuards   | []OutputGuard| 输出安全检查链                           |
| Hooks          | []Hook       | Hook 列表                                |
| Metadata       | map          | 扩展元数据                               |

### 4.3 ToolDef

工具定义，描述 Agent 可调用的工具。

| 字段          | 类型   | 说明                                         |
| ------------- | ------ | -------------------------------------------- |
| Name          | string | 工具名称（唯一标识）                         |
| Description   | string | 工具功能描述，供模型理解                     |
| Parameters    | Schema | 参数的 JSON Schema 描述                      |
| Required      | []string | 必填参数列表                               |
| Source        | string | 来源标识：local / mcp / agent                |
| MCPServerURI  | string | 若来源为 MCP，对应的 MCP 服务地址            |
| AgentID       | string | 若来源为 agent，对应的 Agent ID（Agent-as-Tool）|

### 4.4 ToolCall / ToolResult

工具调用请求与结果。

**ToolCall** — 直接复用 `aimodel.ToolCall`。

| 字段      | 类型                   | 说明                         |
| --------- | ---------------------- | ---------------------------- |
| Index     | int                    | 调用索引（流式合并用）       |
| ID        | string                 | 唯一调用标识                 |
| Type      | string                 | 工具类型（固定 "function"）  |
| Function  | aimodel.FunctionCall   | 函数名和参数                 |

> `aimodel.FunctionCall` 包含 `Name`（函数名）和 `Arguments`（JSON 格式参数字符串）。流式场景下通过 `ToolCall.Merge(delta)` 合并增量。

**ToolResult**

| 字段       | 类型           | 说明                             |
| ---------- | -------------- | -------------------------------- |
| ToolCallID | string         | 对应的 ToolCall ID               |
| Content    | []ContentPart  | 工具执行结果（支持多模态）       |
| IsError    | bool           | 是否为错误结果                   |

**ContentPart** — 内容片段，支持多种类型。

| 字段       | 类型   | 说明                                         |
| ---------- | ------ | -------------------------------------------- |
| Type       | string | 内容类型：text / json / image / file         |
| Text       | string | 文本内容（Type=text 时使用）                 |
| Data       | any    | 结构化数据（Type=json 时使用）               |
| MimeType   | string | MIME 类型（Type=image/file 时使用）          |
| URL        | string | 资源 URL（Type=image/file 时使用）           |

> 简单场景下可使用 `TextResult(text string) []ContentPart` 便捷函数构造纯文本结果。

### 4.5 RunRequest / RunResponse

Agent 执行请求与响应，支持多模态输入和结构化输出。

**RunRequest**

| 字段        | 类型         | 说明                                             |
| ----------- | ------------ | ------------------------------------------------ |
| Messages    | []Message    | 输入消息列表（支持多轮对话延续和多模态内容）     |
| SessionID   | string       | 会话标识，为空则创建新会话                       |
| Options     | *RunOptions  | 运行时参数覆盖                                   |
| Metadata    | map          | 请求级扩展元数据                                 |

**RunOptions** — 运行时参数覆盖，允许在不修改 Agent 定义的情况下调整行为。

| 字段           | 类型     | 说明                                     |
| -------------- | -------- | ---------------------------------------- |
| Model          | string   | 覆盖模型标识                             |
| Temperature    | *float64 | 覆盖采样温度                             |
| MaxIterations  | int      | 覆盖最大迭代次数                         |
| MaxTokens      | int      | 本次调用最大 token 数                    |
| Tools          | []string | 本次调用可用工具名称（白名单过滤）       |
| StopSequences  | []string | 自定义停止序列                           |

**RunResponse**

| 字段        | 类型         | 说明                                     |
| ----------- | ------------ | ---------------------------------------- |
| Messages    | []Message    | Agent 输出消息列表                       |
| SessionID   | string       | 会话标识                                 |
| Usage       | *Usage       | Token 用量统计                           |
| Duration    | int64        | 执行耗时（毫秒）                         |
| Metadata    | map          | 响应级扩展元数据                         |

**Usage** — 直接复用 `aimodel.Usage`。

| 字段             | 类型  | 说明                     |
| ---------------- | ----- | ------------------------ |
| PromptTokens     | int   | 输入 token 数            |
| CompletionTokens | int   | 输出 token 数            |
| TotalTokens      | int   | 总 token 数              |

### 4.6 Memory Entry

内存条目。

| 字段      | 类型   | 说明                                 |
| --------- | ------ | ------------------------------------ |
| Key       | string | 条目键                               |
| Value     | any    | 条目值                               |
| Scope     | string | 内存层级：working / session / store  |
| AgentID   | string | 所属 Agent                           |
| SessionID | string | 所属会话                             |
| CreatedAt | int64  | 创建时间                             |
| TTL       | int64  | 过期时间（秒），0 表示不过期         |

### 4.7 Event

系统事件，用于 Hook 和可观测性。

| 字段      | 类型   | 说明                                         |
| --------- | ------ | -------------------------------------------- |
| Type      | string | 事件类型（见 Hook 章节）                     |
| AgentID   | string | 触发事件的 Agent                             |
| SessionID | string | 会话标识                                     |
| Timestamp | int64  | 事件时间戳                                   |
| Data      | any    | 事件数据（类型随事件类型变化）               |
| ParentID  | string | 父事件 ID，用于构建事件链                    |

### 4.8 Session

会话上下文。

| 字段      | 类型      | 说明                     |
| --------- | --------- | ------------------------ |
| ID        | string    | 会话唯一标识             |
| AgentID   | string    | 关联的 Agent             |
| Messages  | []Message | 对话历史                 |
| Memory    | Memory    | 会话级内存               |
| CreatedAt | int64     | 创建时间                 |
| UpdatedAt | int64     | 最后更新时间             |
| Status    | string    | 状态：active / closed    |

---

## 5. 接口规范

### 5.1 Agent 接口

Agent 核心行为接口。

| 方法       | 参数                          | 返回值                  | 说明                                 |
| ---------- | ----------------------------- | ----------------------- | ------------------------------------ |
| Run        | ctx, req *RunRequest          | (*RunResponse, error)   | 执行 Agent，支持多模态输入           |
| RunStream  | ctx, req *RunRequest          | (*aimodel.Stream, error)| 流式执行 Agent，返回 aimodel 流式读取器（Recv/Close）|
| ID         | —                             | string                  | 返回 Agent 唯一标识                  |
| Name       | —                             | string                  | 返回 Agent 名称                      |
| Tools      | —                             | []ToolDef               | 返回 Agent 可用工具列表              |
| SetMemory  | memory Memory                 | —                       | 设置 Agent 内存实例                  |

> **便捷方法**：框架提供 `RunText(ctx, agentID, input string) (*RunResponse, error)` 辅助函数，内部将纯文本包装为 `RunRequest`，适用于简单文本输入场景。

### 5.2 Memory 接口

内存操作接口，三级内存均实现此接口。

| 方法      | 参数                          | 返回值             | 说明                           |
| --------- | ----------------------------- | ------------------ | ------------------------------ |
| Get       | ctx, key string               | (any, error)       | 读取指定键的内存值             |
| Set       | ctx, key string, val any      | error              | 写入内存值                     |
| Delete    | ctx, key string               | error              | 删除指定键                     |
| List      | ctx, prefix string            | ([]Entry, error)   | 按前缀列出内存条目             |
| Clear     | ctx                           | error              | 清空当前范围内所有内存         |
| BatchGet  | ctx, keys []string            | (map[string]any, error) | 批量读取                 |
| BatchSet  | ctx, entries map[string]any   | error              | 批量写入                       |

**SearchableMemory 接口**（扩展接口，Store Memory 可选实现）

| 方法      | 参数                          | 返回值             | 说明                               |
| --------- | ----------------------------- | ------------------ | ---------------------------------- |
| Search    | ctx, query string, topK int   | ([]Entry, error)   | 语义检索，返回最相关的 topK 条目   |
| Index     | ctx, key string, embedding []float32 | error       | 为条目建立向量索引                 |

> Store Memory 作为长期记忆层，需要语义检索能力来实现"从 Store 检索相关上下文"。实现方可选择内嵌向量索引（如 HNSW）或对接外部向量数据库。框架提供 `SearchableMemory` 扩展接口，不强制所有 Memory 实现均支持。

### 5.3 ToolExecutor 接口

工具执行器接口。

| 方法      | 参数                          | 返回值               | 说明                       |
| --------- | ----------------------------- | -------------------- | -------------------------- |
| Execute   | ctx, name string, args string | (ToolResult, error)  | 执行工具并返回结果         |

### 5.4 ToolRegistry 接口

工具注册表接口。

| 方法       | 参数                           | 返回值          | 说明                         |
| ---------- | ------------------------------ | --------------- | ---------------------------- |
| Register   | name string, handler, def      | error           | 注册工具处理函数和定义       |
| Unregister | name string                    | error           | 注销工具                     |
| Get        | name string                    | (ToolDef, bool) | 获取工具定义                 |
| List       | —                              | []ToolDef       | 列出所有已注册工具           |
| Merge      | tools []ToolDef                | —               | 合并外部工具定义（MCP 来源） |

### 5.5 Guard 接口

Guardrails 安全检查接口，分为输入检查和输出检查。

**InputGuard** — 在 Agent 处理用户输入前执行检查。

| 方法      | 参数                        | 返回值              | 说明                                   |
| --------- | --------------------------- | ------------------- | -------------------------------------- |
| Check     | ctx, input *GuardInput      | (*GuardResult, error) | 检查输入内容，返回通过/拒绝/改写     |

**OutputGuard** — 在 Agent 返回结果给用户前执行检查。

| 方法      | 参数                        | 返回值              | 说明                                   |
| --------- | --------------------------- | ------------------- | -------------------------------------- |
| Check     | ctx, output *GuardOutput    | (*GuardResult, error) | 检查输出内容，返回通过/拒绝/改写     |

**GuardInput**

| 字段      | 类型      | 说明                           |
| --------- | --------- | ------------------------------ |
| Content   | string    | 用户输入内容                   |
| AgentID   | string    | 目标 Agent                     |
| SessionID | string    | 会话标识                       |
| Metadata  | map       | 扩展上下文（如用户角色等）     |

**GuardOutput**

| 字段      | 类型      | 说明                           |
| --------- | --------- | ------------------------------ |
| Content   | string    | Agent 输出内容                 |
| AgentID   | string    | 产出 Agent                     |
| SessionID | string    | 会话标识                       |
| ToolCalls | []ToolCall| 输出中包含的工具调用           |
| Metadata  | map       | 扩展上下文                     |

**GuardResult**

| 字段      | 类型      | 说明                                           |
| --------- | --------- | ---------------------------------------------- |
| Action    | string    | 检查结果：pass / block / rewrite               |
| Content   | string    | 当 Action=rewrite 时，替换后的内容             |
| Reason    | string    | 拒绝或改写原因，用于日志和用户提示             |
| Violations| []string  | 违反的规则列表                                 |

**内建 Guard 类型**

| Guard 类型          | 说明                                               |
| ------------------- | -------------------------------------------------- |
| PromptInjectionGuard| 检测 prompt injection 攻击（越狱、角色劫持等）     |
| ContentFilterGuard  | 过滤有害内容（暴力、色情、仇恨言论等）             |
| PIIGuard            | 检测并脱敏个人身份信息（PII）                      |
| TopicGuard          | 限制对话主题范围，拒绝超出范围的请求               |
| LengthGuard         | 限制输入/输出长度，防止资源滥用                    |
| CustomGuard         | 用户自定义检查逻辑                                 |

**Guard 链执行流程**

```
用户输入
   │
   ▼
┌──────────────────┐
│  InputGuard 链   │ ──→ block? ──→ 返回拒绝响应
│  (顺序执行)      │ ──→ rewrite? ──→ 使用改写内容继续
└──────┬───────────┘
       │ pass
       ▼
   Agent.Run
       │
       ▼
┌──────────────────┐
│  OutputGuard 链  │ ──→ block? ──→ 返回安全提示
│  (顺序执行)      │ ──→ rewrite? ──→ 使用改写内容返回
└──────┬───────────┘
       │ pass
       ▼
   返回用户
```

### 5.6 Hook 接口

Hook 接口，分为同步和异步两种模式。

**Hook（同步）** — 在事件触发线程中同步执行。

| 方法       | 参数             | 返回值 | 说明                         |
| ---------- | ---------------- | ------ | ---------------------------- |
| OnEvent    | ctx, event Event | error  | 同步处理事件                 |
| Filter     | —                | []string | 返回关注的事件类型列表，空表示全部 |

**AsyncHook（异步）** — 事件通过 channel 异步分发，不阻塞主流程。

| 方法       | 参数             | 返回值 | 说明                         |
| ---------- | ---------------- | ------ | ---------------------------- |
| EventChan  | —                | chan<- Event | 返回事件接收 channel    |
| Filter     | —                | []string | 返回关注的事件类型列表     |
| Start      | ctx              | error  | 启动异步处理 goroutine      |
| Stop       | ctx              | error  | 停止异步处理                 |

> 日志、指标等轻量 Hook 使用同步模式；远程上报（如发送到 Jaeger/Prometheus）使用异步模式，避免阻塞 Agent 主流程。Hook Manager 根据 Hook 实现的接口类型自动分发。

### 5.7 Orchestrator 接口

编排器接口。

| 方法       | 参数                       | 返回值                  | 说明                       |
| ---------- | -------------------------- | ----------------------- | -------------------------- |
| Execute    | ctx, req *RunRequest       | (*RunResponse, error)   | 按编排策略执行 Agent 组    |
| AddAgent   | agent Agent                | —                       | 添加参与编排的 Agent       |
| SetStrategy | strategy Strategy         | —                       | 设置编排策略               |

### 5.8 MCPClient 接口

MCP 客户端接口，用于发现和调用外部工具。

| 方法          | 参数                          | 返回值             | 说明                         |
| ------------- | ----------------------------- | ------------------ | ---------------------------- |
| Connect       | ctx, serverURI string         | error              | 连接 MCP 服务端             |
| Disconnect    | ctx                           | error              | 断开连接                     |
| ListTools     | ctx                           | ([]ToolDef, error) | 列出服务端暴露的工具         |
| CallTool      | ctx, name string, args string | (ToolResult, error)| 调用远程工具                 |
| Ping          | ctx                           | error              | 健康检查                     |

### 5.9 MCPServer 接口

MCP 服务端接口，用于将 Agent 能力暴露为 MCP 工具。

| 方法         | 参数                   | 返回值 | 说明                             |
| ------------ | ---------------------- | ------ | -------------------------------- |
| Serve        | ctx                    | error  | 启动 MCP 服务                   |
| Shutdown     | ctx                    | error  | 优雅关停                         |
| RegisterAgent | agent Agent           | error  | 注册 Agent，暴露为 MCP 工具     |
| SetTransport | transport Transport    | —      | 设置传输层实现                   |

### 5.10 PromptTemplate 接口

Prompt 模板接口，支持变量插值和条件渲染。

| 方法      | 参数                          | 返回值          | 说明                             |
| --------- | ----------------------------- | --------------- | -------------------------------- |
| Render    | ctx, vars map[string]any      | (string, error) | 渲染模板，返回最终 prompt 字符串 |
| Name      | —                             | string          | 模板名称标识                     |
| Version   | —                             | string          | 模板版本号                       |

**PromptTemplate 模型**

| 字段      | 类型     | 说明                                         |
| --------- | -------- | -------------------------------------------- |
| Name      | string   | 模板名称                                     |
| Version   | string   | 版本号（语义化版本）                         |
| Template  | string   | 模板内容，支持 `{{.VarName}}` 变量插值       |
| Variables | []VarDef | 变量定义列表（名称、类型、默认值、是否必填） |
| Metadata  | map      | 扩展元数据                                   |

**VarDef**

| 字段      | 类型   | 说明                     |
| --------- | ------ | ------------------------ |
| Name      | string | 变量名                   |
| Type      | string | 类型：string / int / bool|
| Default   | any    | 默认值                   |
| Required  | bool   | 是否必填                 |
| Desc      | string | 变量说明                 |

> Agent 的 `SystemPrompt` 字段类型从 `string` 升级为 `PromptTemplate`。框架同时提供 `StringPrompt(s string) PromptTemplate` 便捷函数，将纯文本包装为模板，兼容简单场景。

### 5.11 LLM Middleware（LLM 调用中间件链）

在 Agent 与 aimodel 之间插入中间件层，基于 `aimodel.ChatCompleter` 接口构建，类似 HTTP middleware 模式。

**核心依赖** — `aimodel.ChatCompleter` 统一接口：

```
ChatCompleter {
    ChatCompletion(ctx, *ChatRequest) (*ChatResponse, error)
    ChatCompletionStream(ctx, *ChatRequest) (*Stream, error)
}
```

> `aimodel.Client` 和 `aimodel/composes.ComposeClient` 均实现此接口。

**LLMMiddleware 接口**

| 方法      | 参数                                              | 返回值                   | 说明                         |
| --------- | ------------------------------------------------- | ------------------------ | ---------------------------- |
| Wrap      | next aimodel.ChatCompleter                        | aimodel.ChatCompleter    | 包装下一层，返回新的 ChatCompleter |

> 中间件通过装饰器模式层层包装 `ChatCompleter`，每层可在调用前后插入逻辑。

**中间件链执行流程**

```
Agent
  │
  ▼
┌──────────┐  ┌────────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌────────────────────┐
│  Log MW  │→│ CircuitBreaker │→│ RateLimit MW│→│  Retry MW   │→│ Timeout MW  │→│  Cache MW   │→│ aimodel.Client     │
│  (日志)  │  │  (熔断)        │  │  (限流)     │  │  (重试)     │  │  (超时)     │  │  (缓存)     │  │ 或 ComposeClient   │
└──────────┘  └────────────────┘  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘  └────────────────────┘
                                                                                                        (ChatCompleter 实现)
```

**内建中间件**

| 中间件                   | 说明                                                     |
| ------------------------ | -------------------------------------------------------- |
| LogMiddleware            | 记录 LLM 调用的请求/响应/耗时/`aimodel.Usage` 用量      |
| CircuitBreakerMiddleware | 熔断保护，连续失败时自动断开 LLM 调用，支持半开探测和自动恢复 |
| RateLimitMiddleware      | 限制 LLM 调用频率，支持 token/min 和 request/min        |
| RetryMiddleware          | 自动重试失败的 LLM 调用，利用 `aimodel.APIError` 判断可重试性，支持指数退避 |
| TimeoutMiddleware        | 为 LLM 调用设置独立超时，防止单次调用阻塞过久           |
| CacheMiddleware          | 缓存相同输入的 LLM 调用结果，支持 TTL 和缓存键自定义    |

> **推荐中间件顺序**：`DefaultChain` 按上表从上到下排列（Log 最外层，Cache 最内层）。可通过 `Chain(base, ...Middleware)` 自定义顺序。

> **多模型降级**：不再需要单独的 FallbackMiddleware。直接使用 `aimodel/composes.ComposeClient`（Failover 策略）作为底层 `ChatCompleter`，内建健康管理和指数退避恢复。

**流式请求（ChatCompletionStream）行为契约**

各中间件对流式请求的处理策略不同于非流式调用，以下明确各中间件的流式行为：

| 中间件                   | 流式行为                                                   |
| ------------------------ | ---------------------------------------------------------- |
| LogMiddleware            | 记录流创建的开始和错误，不追踪流内事件                     |
| CircuitBreakerMiddleware | 通过熔断状态门控流创建，仅观察流创建错误，不观察流中数据错误 |
| RateLimitMiddleware      | 流创建前检查频率限制，但不追踪流式响应的 token 用量        |
| RetryMiddleware          | 流创建失败时按指数退避重试，不重试流中途的错误             |
| TimeoutMiddleware        | 直接透传，不对流式请求施加超时（避免中断进行中的流传输）   |
| CacheMiddleware          | 直接透传，不缓存流式请求                                   |

> **注意**：由于 `aimodel.Stream` 是具体类型，中间件无法包装流对象以观察流中途的错误或数据。所有中间件仅在流创建阶段介入。

---

## 6. Agent 类型

```
             ┌─────────────────┐
             │  Agent (接口)   │
             └───────┬─────────┘
       ┌─────────┬───┴────┬──────────┬──────────┐
       ▼         ▼        ▼          ▼          ▼
  ┌─────────┐ ┌──────┐ ┌──────┐ ┌────────┐ ┌──────┐
  │LLMAgent │ │Work- │ │Router│ │  DAG   │ │Custom│
  │         │ │flow  │ │Agent │ │ Agent  │ │Agent │
  └─────────┘ └──────┘ └──────┘ └────────┘ └──────┘
```

### 各类型说明

| Agent 类型    | 职责                                     | 行为描述                                                               |
| ------------- | ---------------------------------------- | ---------------------------------------------------------------------- |
| LLMAgent      | 基础大模型代理                           | 接收输入 → 构造 Prompt → 调用 LLM → 处理工具调用 → 返回结果。支持多轮推理-行动循环（ReAct），直至 LLM 返回最终答案或达到最大迭代次数。 |
| WorkflowAgent | 顺序流水线                               | 按预定义顺序依次执行一组子 Agent，前一个 Agent 的输出作为下一个的输入。适用于固定步骤的处理流程。 |
| RouterAgent   | 动态路由分发                             | 根据输入内容（由 LLM 判断或规则匹配）选择最合适的子 Agent 处理请求。支持 LLM 路由和基于关键词/语义的规则路由两种模式。 |
| DAGAgent      | 有向无环图编排                           | 按 DAG 定义的依赖关系执行多个 Agent，无依赖的节点并行执行，有依赖的节点等待上游完成后执行。适用于复杂任务分解。 |
| CustomAgent   | 用户自定义                               | 用户实现 Agent 接口即可注入框架。允许用户在 Run 方法中完全控制执行逻辑，适用于特殊业务需求。 |

### LLMAgent 执行流程

```
输入
 │
 ▼
┌──────────────────────────┐
│  InputGuard 链检查       │ ──→ block? ──→ 返回拒绝
└──────────┬───────────────┘
           │ pass / rewrite
           ▼
渲染 PromptTemplate（变量插值）
 + 构造用户消息
 │
 ▼
┌──────────────────────────┐
│  LLM Middleware 链       │◄──────────────┐
│  → Retry → Cache →       │               │
│  → RateLimit → aimodel  │               │
│  附带可用工具列表        │               │
└──────────┬───────────────┘               │
           │                               │
           ▼                               │
     FinishReason?                         │
     ┌────┴────┐                           │
     │         │                           │
  stop     tool_calls                      │
     │         │                           │
     │         ▼                           │
     │    执行工具 ──→ 将结果追加到消息 ───┘
     │    (ToolExecutor)
     ▼
┌──────────────────────────┐
│  OutputGuard 链检查      │ ──→ block? ──→ 返回安全提示
└──────────┬───────────────┘
           │ pass / rewrite
           ▼
       返回结果
```

---

## 7. 编排机制 (Orchestration)

### 编排模式

| 模式   | 说明                                                   | 适用场景                       |
| ------ | ------------------------------------------------------ | ------------------------------ |
| 顺序   | Agent 依次执行，输出链式传递                           | 文档处理管线、ETL              |
| 并行   | 多个 Agent 同时执行，汇聚结果                          | 多角度分析、投票表决           |
| 条件   | 根据上一步结果选择分支                                 | 审核流程、异常处理             |
| DAG    | 按有向无环图拓扑排序执行，支持并行+依赖                  | 复杂任务分解、多步推理         |

### 顺序编排

```
Input ──→ [Agent A] ──→ [Agent B] ──→ [Agent C] ──→ Output
```

### 并行编排

```
            ┌──→ [Agent A] ──┐
Input ──→ ──┼──→ [Agent B] ──┼──→ Aggregator ──→ Output
            └──→ [Agent C] ──┘
```

### 条件编排

```
                    ┌── condition=true ──→ [Agent A] ──┐
Input ──→ Evaluator┤                                   ├──→ Output
                    └── condition=false ─→ [Agent B] ──┘
```

### DAG 编排

```
[Agent A] ──→ [Agent C] ──→ [Agent E]
                  ▲              ▲
[Agent B] ────────┘              │
                                 │
[Agent D] ───────────────────────┘
```

DAG 节点定义：

| 字段         | 类型         | 说明                             |
| ------------ | ------------ | -------------------------------- |
| NodeID       | string       | 节点唯一标识                     |
| AgentID      | string       | 关联的 Agent                     |
| Dependencies | []string     | 上游依赖节点列表                 |
| InputMapper  | func         | 将上游输出映射为当前节点输入     |
| OnError      | ErrorStrategy| 节点失败时的处理策略             |
| Timeout      | Duration     | 节点执行超时时间                 |
| Retries      | int          | 最大重试次数（0 表示不重试）     |

### 编排错误处理策略

| 策略         | 说明                                                       | 适用场景               |
| ------------ | ---------------------------------------------------------- | ---------------------- |
| Abort        | 节点失败后立即终止整个编排，返回错误（默认策略）           | 关键路径、不可容错     |
| Retry        | 节点失败后重试指定次数（支持指数退避）                     | 网络抖动、临时故障     |
| Skip         | 跳过失败节点，下游节点收到空结果继续执行                   | 可选增强步骤           |
| Fallback     | 失败后路由到备用 Agent 执行                                | 主备切换               |
| Compensate   | 失败后执行补偿逻辑（回滚之前步骤的副作用）                 | 有副作用的操作链       |

**并行编排聚合策略**

并行编排中多个 Agent 的结果需要聚合，框架定义 `Aggregator` 接口：

| 方法       | 参数                                | 返回值                | 说明                         |
| ---------- | ----------------------------------- | --------------------- | ---------------------------- |
| Aggregate  | ctx, results []*RunResponse         | (*RunResponse, error) | 将多个结果聚合为一个         |

**内建聚合策略**

| 策略          | 说明                                     |
| ------------- | ---------------------------------------- |
| MergeAll      | 合并所有结果的消息列表                   |
| FirstSuccess  | 返回第一个成功的结果                     |
| MajorityVote  | 多数投票，选择出现最多的答案             |
| BestScore     | 按评分函数选择最优结果                   |

---

## 8. 内存系统 (Memory)

### 三级内存架构

```
┌─────────────────────────────────────────────────┐
│                  Memory System                  │
│                                                 │
│  ┌─────────────┐                                │
│  │   Working    │  当前推理循环的临时上下文      │
│  │   Memory     │  生命周期：单次 Run 调用       │
│  └──────┬──────┘                                │
│         │ 溢出/持久化                            │
│  ┌──────▼──────┐                                │
│  │   Session    │  会话级上下文                  │
│  │   Memory     │  生命周期：一个 Session        │
│  └──────┬──────┘                                │
│         │ 归档/检索                              │
│  ┌──────▼──────┐                                │
│  │   Store      │  持久化存储                    │
│  │   Memory     │  生命周期：跨 Session 持久     │
│  └─────────────┘                                │
└─────────────────────────────────────────────────┘
```

### 各级内存对比

| 维度       | Working Memory           | Session Memory           | Store Memory              |
| ---------- | ------------------------ | ------------------------ | ------------------------- |
| 生命周期   | 单次 Run 调用            | 一个 Session             | 跨 Session 持久           |
| 存储位置   | 进程内存                 | 进程内存 / 可选持久化    | 持久化存储                |
| 容量       | 受 LLM 上下文窗口限制   | 中等                     | 无限制                    |
| 读写频率   | 极高                     | 中等                     | 低                        |
| 典型内容   | 当前消息、工具结果       | 对话历史、中间状态       | 用户画像、知识库、长期记忆|
| 实现方式   | 内存 map                 | 内存 map + 可选序列化    | 文件 / 数据库 / KV 存储  |

### 内存流转

1. **Run 开始** — 创建 Working Memory，加载 Session 上下文
2. **推理循环中** — 工具调用结果、中间推理写入 Working Memory
3. **Run 结束** — Working Memory 中的关键信息提升到 Session Memory（由 MemoryPromoter 决策）
4. **Session 结束** — Session Memory 中的重要信息归档到 Store Memory（由 MemoryArchiver 决策）
5. **新 Session 开始** — 从 Store Memory 检索相关上下文（通过 SearchableMemory.Search）

### 内存流转策略接口

内存层级之间的数据流转由策略接口控制，框架提供默认实现，用户可替换。

**MemoryPromoter** — 决定 Working Memory 中哪些信息提升到 Session Memory。

| 方法      | 参数                              | 返回值           | 说明                             |
| --------- | --------------------------------- | ---------------- | -------------------------------- |
| Promote   | ctx, entries []Entry              | ([]Entry, error) | 从候选条目中选择需要提升的条目   |

**MemoryArchiver** — 决定 Session Memory 中哪些信息归档到 Store Memory。

| 方法      | 参数                              | 返回值           | 说明                             |
| --------- | --------------------------------- | ---------------- | -------------------------------- |
| Archive   | ctx, entries []Entry              | ([]Entry, error) | 从候选条目中选择需要归档的条目   |

### 上下文压缩策略

Working Memory 受 LLM 上下文窗口限制，当消息列表过长时需要压缩。框架定义 `ContextCompressor` 接口。

**ContextCompressor 接口**

| 方法      | 参数                              | 返回值               | 说明                         |
| --------- | --------------------------------- | -------------------- | ---------------------------- |
| Compress  | ctx, messages []Message, maxTokens int | ([]Message, error) | 将消息列表压缩到 token 限制内 |

**内建压缩策略**

| 策略               | 说明                                                     |
| ------------------ | -------------------------------------------------------- |
| SlidingWindow      | 保留最近 N 条消息，丢弃最早的消息                        |
| SummarizeAndTrunc  | 将较早的消息摘要为一条 summary 消息，保留最近消息        |
| TokenBudget        | 按 token 预算从最近消息向前保留，精确控制上下文长度      |
| ImportanceRanking  | 按消息重要性评分排序，优先保留高分消息（工具结果 > 普通对话）|

### 并发安全

多 Agent 并行编排时可能对同一 Session Memory 并发读写，框架保证以下并发安全语义：

| 内存层级         | 并发安全策略                                               |
| ---------------- | ---------------------------------------------------------- |
| Working Memory   | 每次 Run 创建独立实例，无共享，天然安全                    |
| Session Memory   | 内建读写锁（sync.RWMutex），支持并发读、互斥写             |
| Store Memory     | 依赖底层存储的并发语义（文件锁 / 数据库事务 / KV CAS）    |

---

## 9. 工具系统 (Tools)

### 工具生命周期

```
定义 ──→ 注册 ──→ 暴露给 LLM ──→ LLM 选择调用 ──→ 执行 ──→ 结果返回
```

### 工具注册与执行流程

```
┌──────────────┐     ┌──────────────┐     ┌────────────────┐
│  本地工具    │      │  MCP Client  │     │  ToolRegistry  │
│  (用户注册)  │────→ │  (远程发现)   │────→│  (统一注册表)    │
└──────────────┘     └──────────────┘     └───────┬────────┘
                                                  │
                                                  ▼
                                          ┌───────────────┐
                                          │  aimodel.Tool │
                                          │  (传给 LLM)   │
                                          └───────┬───────┘
                                                  │
                                          LLM 返回 ToolCall
                                                  │
                                                  ▼
                                          ┌───────────────┐
                                          │ ToolExecutor  │
                                          │ (路由到处理器)  │
                                          └───────┬───────┘
                                            ┌─────┴─────┐
                                            ▼           ▼
                                       本地执行     MCP CallTool
```

### 与 aimodel 集成

vagent 的工具定义与 `aimodel.Tool` / `aimodel.FunctionDefinition` 对应关系：

| vagent ToolDef     | aimodel 类型                              | 说明                     |
| ------------------ | ----------------------------------------- | ------------------------ |
| Name               | FunctionDefinition.Name                   | 工具函数名               |
| Description        | FunctionDefinition.Description            | 功能描述                 |
| Parameters         | FunctionDefinition.Parameters             | JSON Schema 参数定义     |
| —                  | Tool.Type = "function"                    | 固定值                   |

LLM 返回的 `aimodel.ToolCall`（含 `FunctionCall.Name` 和 `FunctionCall.Arguments`）直接映射到 vagent 的工具执行器。执行结果封装为 `aimodel.Message`（Role=`aimodel.RoleTool`，ToolCallID 对应原始调用 ID）回传给 LLM。

> aimodel 支持流式工具调用，通过 `ToolCall.Merge(delta)` 合并增量参数。vagent 在流式模式下利用此能力实时追踪工具调用进度。

---

## 10. MCP 支持

### 概述

vagent 同时支持 MCP Client 和 MCP Server 两种角色：

```
┌──────────────────────────────────────────────────────────┐
│                        vagent                            │
│                                                          │
│  ┌─────────────┐                     ┌─────────────┐    │
│  │  MCP Client │                     │  MCP Server │    │
│  │  消费外部工具 │                     │  暴露 Agent  │    │
│  └──────┬──────┘                     └──────┬──────┘    │
│         │                                   │            │
└─────────┼───────────────────────────────────┼────────────┘
          │                                   │
          ▼                                   ▼
  ┌───────────────┐                  ┌────────────────┐
  │ 外部 MCP 服务  │                  │ 外部 MCP 客户端  │
  │ (文件系统、    │                  │ (IDE、CLI 等)   │
  │  数据库等)     │                  │                │
  └───────────────┘                  └────────────────┘
```

### MCP Client（消费外部工具）

MCP Client 连接外部 MCP Server，发现并代理其工具。

| 操作       | 说明                                                       |
| ---------- | ---------------------------------------------------------- |
| 初始化     | 通过 initialize 握手协商能力                                |
| 工具发现   | 调用 tools/list 获取工具列表，合并到 ToolRegistry          |
| 工具调用   | 通过 tools/call 代理执行，转为 ToolResult 返回             |
| 生命周期   | 随 Agent 启停，支持重连                                    |

### MCP Server（暴露 Agent 能力）

MCP Server 将 vagent 的 Agent 暴露为 MCP 工具，供外部 MCP 客户端调用。

| 操作         | 说明                                                     |
| ------------ | -------------------------------------------------------- |
| 初始化       | 响应 initialize 请求，声明 capabilities                  |
| 工具列表     | 将已注册 Agent 的 Description 映射为 MCP 工具定义        |
| 工具执行     | 收到 tools/call 后路由到对应 Agent.Run                   |
| 资源暴露     | 可选：将 Agent 的 Memory 暴露为 MCP Resource             |

### 传输层

| 传输方式        | 说明                                         | 适用场景           |
| --------------- | -------------------------------------------- | ------------------ |
| stdio           | 标准输入输出，JSON-RPC 消息                  | 子进程模式         |
| SSE             | Server-Sent Events + HTTP POST               | Web 集成           |
| Streamable HTTP | 单 HTTP 端点，支持流式响应                   | 现代 HTTP 客户端   |

### 认证与授权

MCP 通信涉及外部系统交互，必须具备认证授权机制。

**Authenticator 接口** — 传输层认证中间件。

| 方法          | 参数                          | 返回值              | 说明                             |
| ------------- | ----------------------------- | ------------------- | -------------------------------- |
| Authenticate  | ctx, credentials Credentials  | (*Identity, error)  | 验证凭证，返回身份信息           |

**Authorizer 接口** — 工具级访问控制。

| 方法          | 参数                          | 返回值         | 说明                                   |
| ------------- | ----------------------------- | -------------- | -------------------------------------- |
| Authorize     | ctx, identity *Identity, action string | (bool, error) | 判断身份是否有权执行指定操作 |

**Credentials**

| 字段      | 类型   | 说明                                   |
| --------- | ------ | -------------------------------------- |
| Type      | string | 凭证类型：api_key / bearer / mtls      |
| Token     | string | 凭证内容                               |
| Metadata  | map    | 扩展字段（如证书指纹等）               |

**Identity**

| 字段      | 类型     | 说明                     |
| --------- | -------- | ------------------------ |
| ID        | string   | 身份标识                 |
| Name      | string   | 身份名称                 |
| Roles     | []string | 角色列表                 |
| Metadata  | map      | 扩展字段                 |

**内建认证方式**

| 认证方式         | 说明                                             | 适用场景           |
| ---------------- | ------------------------------------------------ | ------------------ |
| API Key          | 通过请求头或查询参数传递静态密钥                 | 简单部署           |
| Bearer Token     | OAuth2 / JWT Token 验证                          | 企业集成           |
| mTLS             | 双向 TLS 证书认证                                | 高安全要求         |
| NoAuth           | 不认证（仅限开发/测试环境）                      | 本地开发           |

**MCP Server 认证流程**

```
外部 MCP 客户端请求
       │
       ▼
┌──────────────────┐
│  Transport 层    │
│  提取 Credentials│
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│  Authenticator   │ ──→ 失败 ──→ 返回 401
└──────┬───────────┘
       │ 成功
       ▼
┌──────────────────┐
│  Authorizer      │ ──→ 拒绝 ──→ 返回 403
└──────┬───────────┘
       │ 允许
       ▼
   处理 MCP 请求
```

**MCP Client 凭证配置**

MCP Client 连接外部服务时，通过配置注入凭证：

| 配置项                | 类型   | 说明                             |
| --------------------- | ------ | -------------------------------- |
| MCP.Clients[].Auth.Type   | string | 认证类型                         |
| MCP.Clients[].Auth.Token  | string | 凭证内容（支持环境变量引用）     |
| MCP.Clients[].Auth.Header | string | 自定义认证头名称（默认 Authorization）|

---

## 11. Hook 与可观测性

### 事件类型

| 事件类型            | 触发时机                             | 携带数据                   |
| ------------------- | ------------------------------------ | -------------------------- |
| AgentStart          | Agent.Run 开始                       | 输入文本、Agent ID         |
| AgentEnd            | Agent.Run 结束                       | 输出结果、耗时             |
| AgentError          | Agent.Run 出错                       | 错误信息                   |
| LLMStart            | 调用 LLM 前                          | ChatRequest                |
| LLMEnd              | LLM 返回后                           | ChatResponse、token 用量   |
| LLMError            | LLM 调用失败                         | 错误信息                   |
| ToolStart           | 工具执行前                           | 工具名称、参数             |
| ToolEnd             | 工具执行后                           | ToolResult、耗时           |
| ToolError           | 工具执行失败                         | 错误信息                   |
| GuardBlock          | Guard 拦截输入或输出                 | Guard 名称、原因、违规项   |
| GuardRewrite        | Guard 改写输入或输出                 | Guard 名称、原始/改写内容  |
| MemoryRead          | 读取内存                             | 键、值、内存层级           |
| MemoryWrite         | 写入内存                             | 键、值、内存层级           |
| OrchestrationStep   | 编排步骤完成                         | 步骤索引、Agent ID、结果   |
| TokenBudgetExhausted| Token 预算耗尽                       | 已用 token 数、预算上限    |

### Hook 链

```
Event 发生
    │
    ▼
Hook Manager
    │
    ├──→ Hook A (日志记录)
    ├──→ Hook B (指标采集)
    ├──→ Hook C (轨迹追踪)
    └──→ Hook D (自定义处理)
```

同步 Hook 顺序执行，异步 Hook 通过 channel 并行分发。若某个 Hook 返回错误，记录日志但不中断主流程。

### 轨迹采集

通过 Hook 机制可完整采集 Agent 执行轨迹：

```
Trace
├── AgentStart (input="...")
├── LLMStart (model="gpt-4o", messages=[...])
├── LLMEnd (finish_reason="tool_calls", usage={...})
├── ToolStart (name="search", args="...")
├── ToolEnd (result="...")
├── LLMStart (messages=[... + tool_result])
├── LLMEnd (finish_reason="stop")
└── AgentEnd (output="...")
```

### OpenTelemetry 集成

框架内建 OpenTelemetry Exporter Hook，将轨迹数据直通标准可观测性生态。

| 组件          | OTel 映射                                | 说明                           |
| ------------- | ---------------------------------------- | ------------------------------ |
| Agent.Run     | Trace Span（root）                       | 每次 Run 创建根 Span           |
| LLM 调用      | Trace Span（child）                      | 嵌套在 Agent Span 下           |
| 工具调用      | Trace Span（child）                      | 嵌套在 Agent Span 下           |
| Token 用量    | Metric Counter                           | 累计 prompt/completion tokens  |
| 调用延迟      | Metric Histogram                         | 各环节耗时分布                 |
| 错误          | Span Status + Event                      | 错误信息附加到 Span            |

> 通过 `hook.NewOTelHook(exporter)` 创建 OTel Hook，注册到 Agent 即可启用。Hook 内部使用 AsyncHook 模式，不阻塞主流程。

### Token 预算控制

框架内建 Token 预算机制，防止单次调用或累计调用超出成本限制。

| 配置项                 | 类型   | 说明                                         |
| ---------------------- | ------ | -------------------------------------------- |
| Agent.MaxTokenBudget   | int    | 单次 Run 调用最大 token 数（0 表示不限制）   |
| Service.DailyTokenLimit| int    | 全局每日 token 上限                          |
| Service.TokenPerMinute | int    | 全局每分钟 token 速率限制                    |

当 Token 预算即将耗尽时：
1. LLM Middleware 检查剩余预算
2. 若不足以支撑下一次 LLM 调用，终止推理循环
3. 返回部分结果 + 预算耗尽提示
4. 触发 `TokenBudgetExhausted` 事件

---

## 12. 服务层 (Service)

### 使用模式

vagent 支持两种使用模式：

| 模式       | 说明                                                   | 典型场景             |
| ---------- | ------------------------------------------------------ | -------------------- |
| 嵌入式     | 作为库引入，直接调用 Agent API                         | 后端服务、CLI 工具   |
| 独立部署   | 启动 HTTP Server，通过 API 接口访问                    | 微服务、Agent 平台   |

### 嵌入式使用

```
应用程序
  │
  ├── 创建 Agent 实例
  ├── 注册工具
  ├── 配置内存
  └── 调用 agent.Run(ctx, &RunRequest{...})
      或便捷函数 RunText(ctx, agentID, input)
```

### 独立部署

```
配置文件 ──→ Service 启动 ──→ HTTP Server
                │
                ├── 加载 Agent 定义
                ├── 注册工具
                ├── 初始化 MCP Client/Server
                └── 监听 HTTP 请求
```

### 配置体系

| 配置项           | 类型     | 说明                                   |
| ---------------- | -------- | -------------------------------------- |
| Server.Addr      | string   | 监听地址，如 ":8080"                   |
| Agents           | []Agent  | Agent 定义列表                         |
| Tools            | []Tool   | 全局工具列表                           |
| Models           | []ModelConfig     | 模型配置列表（对应 aimodel.Client 实例）|
| Models[].Name    | string            | 模型标识名                    |
| Models[].APIKey  | string            | API Key（支持环境变量引用）   |
| Models[].BaseURL | string            | API 基础地址                  |
| Models[].Protocol| string            | 协议类型：openai / anthropic  |
| Compose          | ComposeConfig     | 多模型调度配置（对应 ComposeClient）|
| Compose.Strategy | string            | 调度策略：failover / random / weighted |
| Compose.Models   | []ComposeModelRef | 参与调度的模型引用及权重      |
| MCP.Clients      | []MCPClientConfig | MCP 客户端连接配置（含认证）  |
| MCP.Server       | MCPServerConfig   | MCP 服务端配置                |
| MCP.Server.Auth  | AuthConfig        | MCP 服务端认证配置            |
| Memory.Store     | StoreConfig       | 持久化存储配置                |
| Guard.Input      | []GuardConfig     | 输入 Guard 链配置              |
| Guard.Output     | []GuardConfig     | 输出 Guard 链配置              |
| Token.DailyLimit | int               | 全局每日 token 上限           |
| Token.PerMinute  | int               | 全局每分钟 token 速率限制     |
| LLM.Middlewares  | []string          | 启用的 LLM 中间件列表         |
| Hooks            | []string | 启用的 Hook 类型                       |

### HTTP 接口

| 方法   | 路径                     | 说明                               |
| ------ | ------------------------ | ---------------------------------- |
| POST   | /v1/agent/{id}/run       | 执行 Agent（同步）                 |
| POST   | /v1/agent/{id}/stream    | 执行 Agent（流式 SSE）             |
| POST   | /v1/agent/{id}/async     | 异步执行 Agent，返回 202 + taskID  |
| GET    | /v1/tasks/{taskID}       | 查询异步任务状态和结果             |
| GET    | /v1/agents               | 列出所有 Agent                     |
| GET    | /v1/agent/{id}           | 获取 Agent 详情                    |
| GET    | /v1/tools                | 列出所有已注册工具                 |
| GET    | /v1/health               | 健康检查                           |

> **异步模式**：`POST /v1/agent/{id}/async` 立即返回 `202 Accepted` 和 `taskID`，客户端通过 `GET /v1/tasks/{taskID}` 轮询结果。适用于长时间运行的 Agent 任务（如复杂 DAG 编排）。

---

## 13. Harness Engineering

### 模块化设计

- 每个模块（Agent、Memory、Tool、MCP）通过接口解耦
- 各模块可独立测试、独立替换
- 新增 Agent 类型只需实现 Agent 接口
- 新增工具只需实现 ToolHandler 并注册

### 验证循环

构建 Agent 系统的迭代验证流程：

```
定义 Agent ──→ 编写工具 ──→ 测试单步 ──→ 测试编排 ──→ 端到端验证
    ▲                                                      │
    └──────────────────── 反馈调整 ◄────────────────────────┘
```

| 验证层级     | 验证内容                               | 方法                     |
| ------------ | -------------------------------------- | ------------------------ |
| 单元         | 单个工具、单个 Agent 的行为            | 标准单元测试             |
| 集成         | Agent + 工具 + 内存协作                | Mock LLM 测试            |
| 编排         | 多 Agent 编排的正确性                  | 场景测试                 |
| 端到端       | 完整 Service 的请求-响应               | 真实 LLM 调用            |

### 上下文工程

优化 Agent 上下文质量的关键实践：

| 实践                 | 说明                                                         |
| -------------------- | ------------------------------------------------------------ |
| 系统提示词模板化     | 通过 `prompt` 包的 PromptTemplate 管理复杂提示词，支持变量注入和版本管理 |
| 上下文窗口管理       | Working Memory 自动截断过长上下文，保留关键信息              |
| 工具描述优化         | 工具的 Description 直接影响 LLM 选择准确率，需精心编写       |
| Few-shot 示例注入    | 在系统提示词中嵌入示例，引导 LLM 输出格式                   |
| 结果后处理           | 对 LLM 输出做格式校验和内容提取                             |

### 轨迹采集与分析

通过 Hook 机制采集执行轨迹，用于调试和优化：

| 采集维度       | 数据内容                                 | 用途                   |
| -------------- | ---------------------------------------- | ---------------------- |
| Token 用量     | 每次 LLM 调用的 prompt/completion tokens | 成本分析               |
| 延迟           | 各环节耗时                               | 性能瓶颈定位           |
| 工具调用链     | 工具名称、参数、结果序列                 | 行为分析               |
| 决策路径       | 路由选择、条件分支                       | Agent 行为理解         |
| 错误分布       | 错误类型、频率、上下文                   | 可靠性改进             |

### Agent-as-Tool（Agent 嵌套调用）

允许一个 Agent 被注册为另一个 Agent 的工具，实现 Agent 嵌套调用和 Handoff 模式。

```
┌─────────────────────────────────────────────┐
│  Coordinator Agent                          │
│  Tools: [search_tool, SubAgent-A, SubAgent-B]│
│                                              │
│  LLM 选择调用 SubAgent-A                    │
│       │                                      │
│       ▼                                      │
│  ┌────────────────┐                         │
│  │  SubAgent-A    │ (作为 Tool 执行)         │
│  │  独立 Run 调用 │                          │
│  └───────┬────────┘                         │
│          │ 返回 ToolResult                   │
│          ▼                                   │
│  继续推理循环...                             │
└─────────────────────────────────────────────┘
```

注册方式：

```
// 将 Agent 注册为另一个 Agent 的工具
registry.RegisterAgentAsTool(subAgent, ToolDef{
    Name:        "research_assistant",
    Description: "擅长信息检索和知识整理的助手",
    Source:      "agent",
    AgentID:     subAgent.ID(),
})
```

> 当 LLM 选择调用 Agent 类型的工具时，ToolExecutor 内部将参数包装为 RunRequest，调用目标 Agent.Run，并将 RunResponse 转为 ToolResult 返回。

### Evaluation 模块

内建评估框架，用于 Agent 质量回归测试和持续优化。

**Evaluator 接口**

| 方法      | 参数                              | 返回值              | 说明                         |
| --------- | --------------------------------- | ------------------- | ---------------------------- |
| Evaluate  | ctx, case *EvalCase               | (*EvalResult, error)| 对单个测试用例评估           |

**EvalCase**

| 字段      | 类型         | 说明                             |
| --------- | ------------ | -------------------------------- |
| ID        | string       | 用例标识                         |
| Input     | *RunRequest  | 输入请求                         |
| Expected  | *RunResponse | 期望输出（可选，用于对比）       |
| Criteria  | []string     | 评估标准列表                     |
| Tags      | []string     | 标签（用于分组和过滤）           |

**EvalResult**

| 字段      | 类型         | 说明                             |
| --------- | ------------ | -------------------------------- |
| CaseID    | string       | 对应的用例 ID                    |
| Score     | float64      | 综合评分（0-1）                  |
| Passed    | bool         | 是否通过                         |
| Details   | []EvalDetail | 各维度评分详情                   |
| Actual    | *RunResponse | 实际输出                         |
| Duration  | int64        | 评估耗时（毫秒）                 |
| Usage     | *Usage       | Token 用量                       |

**内建评估器**

| 评估器            | 说明                                       |
| ----------------- | ------------------------------------------ |
| ExactMatchEval    | 精确匹配期望输出                           |
| ContainsEval      | 检查输出是否包含指定关键词                 |
| LLMJudgeEval      | 使用 LLM 作为评判者评分                    |
| ToolCallEval      | 验证工具调用序列是否符合预期               |
| LatencyEval       | 检查响应时间是否在阈值内                   |
| CostEval          | 检查 Token 用量是否在预算内                |

---

## 14. aimodel 集成参考

vagent 通过 `github.com/vogo/aimodel` 统一调用多家大模型。以下说明 vagent 与 aimodel 的集成方式。

### 核心接口复用

vagent 直接复用 aimodel 的以下核心类型，不做二次封装：

| aimodel 类型               | vagent 使用位置                            |
| -------------------------- | ------------------------------------------ |
| `ChatCompleter`            | largemodel 中间件链的基础接口              |
| `Client`                   | 单模型后端                                 |
| `composes.ComposeClient`   | 多模型调度（Failover/Random/Weighted）     |
| `ChatRequest`              | LLM 调用请求构造                           |
| `ChatResponse`             | LLM 调用响应解析                           |
| `Stream` / `StreamChunk`   | 流式响应读取（Recv/Close）                 |
| `Message` / `Content`      | 消息和多模态内容                           |
| `Tool` / `FunctionDefinition` | 工具定义传递给 LLM                      |
| `ToolCall` / `FunctionCall`| 工具调用请求解析                           |
| `Usage`                    | Token 用量统计                             |
| `Role` / `FinishReason`    | 消息角色和终止原因常量                     |
| `APIError` / `ModelError`  | 错误处理和重试判断                         |
| `Protocol`                 | 协议类型（OpenAI / Anthropic）             |

### 集成架构

```
vagent Agent
    │
    ▼
largemodel 中间件链 (Retry → Cache → RateLimit → ...)
    │
    ▼ (ChatCompleter 接口)
    │
    ├── aimodel.Client (单模型)
    │     Protocol: openai / anthropic
    │     内部自动转换请求/响应格式
    │
    └── composes.ComposeClient (多模型调度)
          Strategy: failover / random / weight
          ├── aimodel.Client (Model A)
          ├── aimodel.Client (Model B)
          └── aimodel.Client (Model C)
          内建健康管理 + 指数退避恢复
```

### 错误处理集成

vagent 利用 aimodel 的结构化错误类型进行精细化错误处理：

| aimodel 错误类型    | vagent 处理方式                              |
| ------------------- | -------------------------------------------- |
| `APIError`          | 根据 StatusCode 判断可重试性（429/500/503 重试，400/401/403 直接失败）|
| `ModelError`        | 在 ComposeClient 场景下标记模型不健康，切换备用模型 |
| `MultiError`        | 所有模型均失败时，聚合错误信息返回           |
| `ErrNoActiveModels` | 触发 TokenBudgetExhausted 或降级事件         |

### 多模态内容构造

```
// 纯文本消息
msg := aimodel.Message{
    Role:    aimodel.RoleUser,
    Content: aimodel.NewTextContent("你好"),
}

// 图文混合消息
msg := aimodel.Message{
    Role: aimodel.RoleUser,
    Content: aimodel.NewPartsContent(
        aimodel.ContentPart{Type: "text", Text: "描述这张图片"},
        aimodel.ContentPart{Type: "image_url", ImageURL: &aimodel.ImageURL{URL: "https://..."}},
    ),
}
```

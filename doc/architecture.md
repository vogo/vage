# vagent 架构文档

## 1. 项目概述

### 1.1 定位

vagent 是一个 Go 语言 Agent 开发框架，用于构建基于大语言模型（LLM）的智能代理系统。支持嵌入现有系统或独立部署。

### 1.2 目标

- 提供灵活的 Agent 抽象，支持多种 Agent 类型和编排模式
- 通过 `github.com/vogo/aimodel` 统一调用多家大模型（OpenAI、Anthropic、Gemini 等）
- 支持 MCP（Model Context Protocol）协议，实现工具互操作
- 最小外部依赖（仅依赖 `aimodel` 和 `github.com/modelcontextprotocol/go-sdk`）
- 可嵌入、可独立部署

### 1.3 设计原则

| 原则         | 说明                                           |
| ------------ | ---------------------------------------------- |
| 组合优于继承 | 通过接口组合构建复杂 Agent，避免深层继承       |
| 最小依赖     | 仅依赖 `aimodel` 和 `mcp/go-sdk`，不引入 ORM、Web 框架等 |
| 上下文驱动   | 所有操作通过 `context.Context` 传递，支持取消  |
| 可观测       | 内建 Hook 机制，支持事件追踪和轨迹采集         |
| 协议兼容     | 遵循 MCP 协议规范和 Agent Skills 开放标准，与外部生态互通 |
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
├────────────────┬────────────────┬──────────────────┬───────────────┤
│   Agent Layer  │  Memory Layer  │   Tool Layer     │  Skill Layer  │
│  ┌───────────┐ │ ┌────────────┐ │ ┌──────────────┐ │ ┌───────────┐│
│  │ LLMAgent  │ │ │  Working   │ │ │ Tool Reg.    │ │ │ Skill Reg.││
│  │ Workflow  │ │ │  Session   │ │ │ Tool Exec.   │ │ │ Skill Mgr.││
│  │ Router    │ │ │  Store     │ │ │ Built-in/MCP │ │ │ Skill Load││
│  │ Custom    │ │ └────────────┘ │ └──────────────┘ │ └───────────┘│
│  └───────────┘ │                │                  │              │
├────────────────┴────────────────┴───────────────────────────────────┤
│                       Hook / Observability                         │
│              (Hook / 轨迹采集 / 日志 / 指标)                       │
├─────────────────────────────────────────────────────────────────────┤
│                     外部依赖 (aimodel / mcp go-sdk)                │
│  (ChatCompleter / ComposeClient / Stream / Protocol / MCP)         │
└─────────────────────────────────────────────────────────────────────┘
```

### 2.1. 层次说明

| 层级           | 职责                                                 |
| -------------- | ---------------------------------------------------- |
| Application    | 对外入口：嵌入式 API 调用、HTTP 接口、命令行工具     |
| Service        | 配置加载、Server 启停、Agent 注册与生命周期管理      |
| Guardrails     | 安全检查层，输入输出过滤、内容审核、注入防护         |
| Agent          | 核心智能体逻辑，包含多种 Agent 类型和编排策略        |
| Memory         | 上下文管理，三级内存架构                             |
| Tool           | 工具定义、注册、执行，MCP 协议集成                   |
| Skill          | Skill 发现、注册、激活与生命周期管理                 |
| Hook           | 横切关注点，事件驱动的可观测性                       |
| aimodel        | 底层大模型调用，提供 ChatCompleter 统一接口、Protocol 协议分发、ComposeClient 多模型调度 |
| mcp/go-sdk     | MCP 协议 Go SDK，提供 MCP 协议基础实现               |


### 2.2. 模块

| 模块           | 详细设计                         | 简要说明                                                                                                                                                       |
| -------------- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `schema`       | [schema.md](schema.md)           | 共享模型定义：Message、Event、ToolDef、SkillDef 等                                                                                                             |
| `agent`        | [agent.md](agent.md)             | 四种内建 Agent：LLMAgent（ReAct 式工具调用）、WorkflowAgent（顺序/DAG/循环编排）、RouterAgent（动态路由）、CustomAgent（委托用户函数），LLMAgent 原生支持流式   |
| `orchestrate`  | [orchestrate.md](orchestrate.md) | DAG 编排引擎，支持顺序/并行/条件分支、循环节点、动态生成、检查点恢复、补偿回滚（Saga）、背压控制（AIMD）、优先级调度                                            |
| `memory`       | [memory.md](memory.md)           | 三级内存架构：Working（单次 Run）→ Session（会话级）→ Persistent（跨会话持久），Promoter/Archiver 控制层级流转，ContextCompressor 管理上下文压缩                 |
| `tool`         | [tool.md](tool.md)               | 统一工具注册表（ToolRegistry），支持本地工具、MCP 远程工具、Agent-as-Tool 三种来源，ToolExecutor 路由执行                                                       |
| `skill`        | [skill.md](skill.md)             | 兼容 Agent Skills 开放标准（agentskills.io），Skill 在上下文层面注入指令引导行为，与 Tool 正交；支持发现、注册、验证、按需激活、渐进式加载，`allowed-tools` 安全控制 |
| `mcp`          | [mcp.md](mcp.md)                 | MCP Client（消费外部工具）+ MCP Server（暴露 Agent 能力），支持 stdio、SSE、Streamable HTTP 三种传输                                                           |
| `guard`        | [guard.md](guard.md)             | 输入/输出安全检查链，内建 PromptInjection、ContentFilter、PII、Topic、Length、Custom 六种 Guard                                                                |
| `hook`         | [hook.md](hook.md)               | 同步/异步 Hook 采集 Agent 执行轨迹，支持日志、指标、追踪，内建 Token 预算控制                                                                                  |
| `largemodel`   | [largemodel.md](largemodel.md)   | Agent 与 aimodel 之间的中间件层（装饰器模式），内建 Log、CircuitBreaker、RateLimit、Retry、Timeout、Cache、Metrics 七种中间件                                   |
| `service`      | [service.md](service.md)         | 支持嵌入式和独立部署，独立部署提供 RESTful HTTP 接口，支持同步/流式/异步三种 Agent 执行方式                                                                    |
| `eval`         | [eval.md](eval.md)               | 内建评估框架，支持 ExactMatch、Contains、LLMJudge、ToolCall、Latency、Cost 六种评估器和组合评估                                                                |



### 2.3. 包依赖关系

```
service ──→ guard ──→ agent ──→ memory
  │                     │──→ tool ──→ mcp/client
  │                     │──→ skill ──→ tool
  │                     │──→ hook
  │                     │──→ orchestrate
  │                     │──→ prompt
  │                     └──→ largemodel ──→ aimodel
  │
  └──→ mcp/server ──→ agent

agent, tool, largemodel ──→ aimodel (ChatCompleter / ComposeClient)
mcp/* ──→ github.com/modelcontextprotocol/go-sdk (外部)
schema ←── (所有包共享)
```

---

## 4. 核心模型定义

核心类型概览：

| 类型           | 说明                                                     |
| -------------- | -------------------------------------------------------- |
| `Message`      | 对话消息，内嵌 `aimodel.Message`，增加 AgentID/Timestamp/Metadata |
| `ContentPart`  | 内容片段，支持 text/json/image/file                      |
| `ToolDef`      | 工具定义，支持 local/mcp/agent 三种来源                  |
| `ToolResult`   | 工具执行结果                                             |
| `SkillDef`     | Skill 定义，包含指令、工具白名单、资源引用               |
| `RunRequest`   | Agent 执行请求                                           |
| `RunResponse`  | Agent 执行响应，含 StopReason                            |
| `RunOptions`   | 运行时参数覆盖                                           |
| `Event`        | 系统事件，用于 Hook 和流式输出                           |
| `RunStream`    | 流式事件流，pull 模式                                    |

---

## 5. Harness Engineering

### 5.1. 模块化设计

- 每个模块（Agent、Memory、Tool、Skill、MCP）通过接口解耦
- 各模块可独立测试、独立替换
- 新增 Agent 类型只需实现 Agent 接口
- 新增工具只需实现 ToolHandler 并注册
- 新增 Skill 只需编写 SKILL.md 并放入发现目录

### 5.2. 验证循环

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

### 5.3. 上下文工程

| 实践                 | 说明                                                         |
| -------------------- | ------------------------------------------------------------ |
| 系统提示词模板化     | 通过 `prompt` 包的 PromptTemplate 管理复杂提示词             |
| 上下文窗口管理       | Working Memory 自动截断过长上下文，保留关键信息              |
| 工具描述优化         | 工具的 Description 直接影响 LLM 选择准确率，需精心编写       |
| Few-shot 示例注入    | 在系统提示词中嵌入示例，引导 LLM 输出格式                   |
| 结果后处理           | 对 LLM 输出做格式校验和内容提取                             |

---

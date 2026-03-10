# 工具系统 (Tool)

`tool` 包提供工具注册、执行和 Agent-as-Tool 机制。

## 1. 核心接口

### ToolHandler

工具处理函数类型。

```go
type ToolHandler func(ctx context.Context, name, args string) (schema.ToolResult, error)
```

### ToolExecutor

工具执行器接口。

```go
type ToolExecutor interface {
    Execute(ctx context.Context, name, args string) (schema.ToolResult, error)
}
```

### ExternalToolCaller

外部工具调用接口（MCP 远程工具）。

```go
type ExternalToolCaller interface {
    CallTool(ctx context.Context, name, args string) (schema.ToolResult, error)
}
```

### ToolRegistry

工具注册表接口，内嵌 ToolExecutor。

```go
type ToolRegistry interface {
    ToolExecutor
    Register(def schema.ToolDef, handler ToolHandler) error
    Unregister(name string) error
    Get(name string) (schema.ToolDef, bool)
    List() []schema.ToolDef
    Merge(defs []schema.ToolDef)  // 合并外部工具定义（MCP 来源）
}
```

## 2. Registry 实现

线程安全的内存工具注册表。

```go
func NewRegistry(opts ...RegistryOption) *Registry
func WithExternalCaller(c ExternalToolCaller) RegistryOption
```

| 方法                  | 说明                                                       |
| --------------------- | ---------------------------------------------------------- |
| `Register`            | 注册工具定义和处理函数                                     |
| `Unregister`          | 注销工具                                                   |
| `Get`                 | 获取工具定义                                               |
| `List`                | 列出所有已注册工具                                         |
| `Merge`               | 合并外部工具定义，跳过已存在的（不覆盖本地工具）           |
| `Execute`             | 执行工具：优先本地 handler，回退到 ExternalToolCaller      |
| `SetExternalCaller`   | 设置外部工具调用器（MCP Client）                           |
| `RegisterIfAbsent`    | 原子检查重复并注册工具                                     |

## 3. 工具生命周期

```
定义 ──→ 注册 ──→ 暴露给 LLM ──→ LLM 选择调用 ──→ 执行 ──→ 结果返回
```

### 注册与执行流程

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

## 4. 与 aimodel 集成

### 工具定义映射

| vagent ToolDef     | aimodel 类型                              | 说明                     |
| ------------------ | ----------------------------------------- | ------------------------ |
| Name               | FunctionDefinition.Name                   | 工具函数名               |
| Description        | FunctionDefinition.Description            | 功能描述                 |
| Parameters         | FunctionDefinition.Parameters             | JSON Schema 参数定义     |
| —                  | Tool.Type = "function"                    | 固定值                   |

### 辅助函数

```go
func ToAIModelTools(defs []schema.ToolDef) []aimodel.Tool    // 转为 aimodel.Tool 格式
func FilterTools(defs []schema.ToolDef, names []string) []schema.ToolDef  // 白名单过滤
```

LLM 返回的 `aimodel.ToolCall` 直接映射到工具执行器。执行结果封装为 `aimodel.Message`（Role=tool）回传给 LLM。

## 5. Agent-as-Tool

允许一个 Agent 被注册为另一个 Agent 的工具，实现 Agent 嵌套调用。

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

### 注册方式

```go
agenttool.Register(registry, subAgent, opts ...agenttool.Option)
```

### agenttool.Option

```go
func WithName(name string) Option              // 覆盖工具名（默认 agent.Name()）
func WithDescription(desc string) Option       // 覆盖描述（默认 agent.Description()）
func WithParameters(params any) Option         // 覆盖 JSON Schema 参数
func WithArgExtractor(fn ArgExtractor) Option  // 自定义参数提取
```

### ArgExtractor

```go
type ArgExtractor func(parsed map[string]any) (string, error)
```

默认提取 `"input"` 字段。默认参数 Schema 为 `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`。

### 错误处理策略

Agent 执行错误转为 `ToolResult{IsError: true}`，而非 Go error，保持 LLM 工具调用循环可见性。

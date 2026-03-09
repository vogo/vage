# Skill 系统设计文档

## 1. 概述

### 1.1 背景

Agent Skills 是 Anthropic 于 2025 年 10 月提出、2025 年 12 月捐赠给 Linux 基金会旗下 Agentic AI Foundation (AAIF) 的开放标准。Microsoft、OpenAI、Atlassian、Figma、Cursor、GitHub 等 26+ 平台已采纳。Skill 为 AI Agent 提供结构化的可复用领域专业知识包，是 Agent 生态的重要补充。

### 1.2 Skill 与 Tool 的区别

| 维度       | Tool                       | Skill                                    |
| ---------- | -------------------------- | ---------------------------------------- |
| 本质       | 可执行代码（函数/API）     | 结构化指令 + 可选脚本/资源               |
| 执行方式   | 确定性 API 调用            | 模型解读指令并遵循                       |
| 作用层     | 执行层（调用函数返回结果） | 上下文层（注入 Prompt 引导行为）         |
| 粒度       | 单一函数                   | 打包的领域专家知识（指令+脚本+参考资料） |
| 可靠性     | 确定性输入输出 Schema      | 取决于模型对指令的理解和遵循             |
| 加载方式   | 注册后常驻                 | 按需激活，渐进式披露                     |

**核心洞察**：Skill 与 Tool 正交——Skill 在 Prompt/上下文层面操作，Tool 在执行层面操作。Skill 可以声明它需要使用哪些 Tool（通过 `allowed-tools`），但 Skill 本身是塑造 Agent 行为的指令，Tool 是执行原语。

### 1.3 设计目标

- 兼容 Agent Skills 开放标准（agentskills.io）规范
- 支持 Skill 的发现、注册、激活、执行、卸载完整生命周期
- 渐进式上下文加载，避免上下文窗口浪费
- 与 vagent 现有 Tool、Agent、Guard、Memory 体系无缝集成
- 安全优先，支持工具白名单、沙箱执行、权限控制

---

## 2. Agent Skills 开放标准

### 2.1 目录结构

```
my-skill/
  SKILL.md          # 必需：YAML frontmatter + Markdown 指令
  scripts/          # 可选：可执行自动化脚本
  references/       # 可选：按需加载的参考文档
  assets/           # 可选：模板和静态资源
```

### 2.2 SKILL.md 格式

```markdown
---
name: pdf-processing
description: Process and analyze PDF documents
license: Apache-2.0
allowed-tools:
  - bash
  - read_file
metadata:
  author: example
  version: 1.0.0
---

## Instructions
[分步指导、示例、边界情况处理...]
```

### 2.3 规范约束

| 约束               | 说明                                                 |
| ------------------ | ---------------------------------------------------- |
| 命名规则           | 目录名须与 `name` 字段一致，仅允许小写字母、数字、连字符 |
| 必填字段           | `name` 和 `description` 为必填 frontmatter 字段      |
| 长度限制           | SKILL.md 建议不超过 500 行                           |
| 加载策略           | SKILL.md 正文在激活时全量加载；scripts/references/assets 按需加载 |
| 路径规则           | 使用相对于 Skill 根目录的路径，保持一级深度           |

---

## 3. 核心模型

### 3.1 SkillDef — Skill 定义

```go
// SkillDef describes a skill that can be discovered, registered and activated.
type SkillDef struct {
    Name         string            `json:"name"`
    Description  string            `json:"description"`
    License      string            `json:"license,omitempty"`
    AllowedTools []string          `json:"allowed_tools,omitempty"`
    Metadata     map[string]string `json:"metadata,omitempty"`
    Instructions string            `json:"instructions"`      // SKILL.md 正文
    BasePath     string            `json:"base_path,omitempty"` // Skill 目录路径
}
```

| 字段           | 类型                | 说明                                   |
| -------------- | ------------------- | -------------------------------------- |
| Name           | string              | Skill 唯一标识（小写+连字符）          |
| Description    | string              | Skill 功能描述，用于匹配和展示         |
| License        | string              | 许可证类型                             |
| AllowedTools   | []string            | 允许使用的工具白名单                   |
| Metadata       | map[string]string   | 扩展元数据（author、version 等）       |
| Instructions   | string              | 解析后的 Markdown 指令正文             |
| BasePath       | string              | Skill 目录根路径，用于定位 scripts/references/assets |

### 3.2 SkillResource — 资源引用

```go
// SkillResource represents a loadable resource within a skill.
type SkillResource struct {
    Type    string `json:"type"` // "script", "reference", "asset"
    Name    string `json:"name"`
    Path    string `json:"path"`
    Content string `json:"content,omitempty"` // 懒加载，激活后填充
}
```

### 3.3 SkillActivation — 激活状态

```go
// SkillActivation tracks the activation state of a skill within a session.
type SkillActivation struct {
    SkillName   string
    SessionID   string
    ActivatedAt time.Time
    Resources   []SkillResource // 已加载的资源
}
```

---

## 4. 核心接口

### 4.1 SkillLoader — 加载器

```go
// SkillLoader discovers and loads skill definitions from a source.
type SkillLoader interface {
    // Load loads a single skill from the given path.
    Load(path string) (*SkillDef, error)
    // Discover scans a directory for skills and returns their definitions.
    Discover(dir string) ([]*SkillDef, error)
}
```

**实现**：`FileSkillLoader` — 从文件系统加载，解析 SKILL.md 的 YAML frontmatter 和 Markdown 正文。

### 4.2 SkillRegistry — 注册表

```go
// SkillRegistry manages skill definitions and their lifecycle.
type SkillRegistry interface {
    // Register adds a skill definition to the registry.
    Register(def *SkillDef) error
    // Unregister removes a skill from the registry.
    Unregister(name string) error
    // Get returns a skill definition by name.
    Get(name string) (*SkillDef, bool)
    // List returns all registered skill definitions.
    List() []*SkillDef
    // Match returns skills whose description matches the query (for dynamic activation).
    Match(query string) []*SkillDef
}
```

### 4.3 SkillManager — 管理器

```go
// SkillManager orchestrates skill activation and deactivation within agent runs.
type SkillManager interface {
    // Activate loads a skill's instructions into the agent's context.
    Activate(ctx context.Context, name string, sessionID string) (*SkillActivation, error)
    // Deactivate removes a skill's context from the session.
    Deactivate(ctx context.Context, name string, sessionID string) error
    // ActiveSkills returns currently active skills for a session.
    ActiveSkills(sessionID string) []*SkillActivation
    // LoadResource lazily loads a script/reference/asset from an active skill.
    LoadResource(ctx context.Context, name string, resourceType string, resourceName string) (*SkillResource, error)
}
```

---

## 5. Skill 生命周期

```
发现 ──→ 注册 ──→ 验证 ──→ 激活 ──→ 执行 ──→ 卸载
                    │                   │
                    │ frontmatter 校验   │ 指令注入 SystemPrompt
                    │ 工具白名单验证     │ 脚本/资源按需加载
                    │ 目录结构检查       │ Guard 安全检查
```

### 5.1 发现 (Discovery)

`SkillLoader.Discover(dir)` 扫描指定目录，查找包含 `SKILL.md` 的子目录。支持：
- 本地文件系统目录扫描
- 项目内 `.skills/` 约定目录
- 可扩展支持远程仓库（未来）

### 5.2 注册 (Registration)

`SkillRegistry.Register(def)` 将 SkillDef 加入注册表：
- 校验 `name` 和 `description` 必填
- 校验命名规范（小写字母、数字、连字符）
- 检查名称唯一性，防止冲突

### 5.3 验证 (Validation)

注册时执行验证：
- Frontmatter 必填字段完整性
- `allowed-tools` 中的工具是否在 ToolRegistry 中存在
- SKILL.md 行数是否超过 500 行限制
- 目录结构合规性（scripts/、references/、assets/ 规范）

### 5.4 激活 (Activation)

`SkillManager.Activate()` 将 Skill 指令注入 Agent 上下文：
- 将 `Instructions` 追加到 Agent 的 SystemPrompt
- 记录激活状态（SkillActivation）
- 触发 `SkillActivate` 事件（Hook 系统）
- 限制 Agent 仅使用 `allowed-tools` 中声明的工具

### 5.5 执行 (Execution)

Agent 在 Skill 指令引导下执行任务：
- 按指令步骤操作
- 按需加载 scripts/references/assets（`LoadResource`）
- 脚本通过沙箱环境执行
- Guard 检查 Skill 上下文中的输入输出

### 5.6 卸载 (Deactivation)

任务完成后释放 Skill 上下文：
- 从 SystemPrompt 中移除 Skill 指令
- 清理已加载的资源
- 触发 `SkillDeactivate` 事件
- 释放上下文窗口空间

---

## 6. 与 vagent 现有体系集成

### 6.1 架构位置

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Application Layer                          │
├─────────────────────────────────────────────────────────────────────┤
│                          Service Layer                             │
├─────────────────────────────────────────────────────────────────────┤
│                        Guardrails Layer                            │
├────────────────┬────────────────┬────────────────┬─────────────────┤
│   Agent Layer  │  Memory Layer  │  Tool Layer    │  Skill Layer    │
│  ┌───────────┐ │ ┌────────────┐ │ ┌────────────┐ │ ┌────────────┐ │
│  │ LLMAgent  │ │ │  Working   │ │ │ Tool Reg.  │ │ │ Skill Reg. │ │
│  │ Workflow  │ │ │  Session   │ │ │ Tool Exec. │ │ │ Skill Mgr. │ │
│  │ Router    │ │ │  Store     │ │ │ Built-in   │ │ │ Skill Load │ │
│  │ Custom    │ │ └────────────┘ │ └────────────┘ │ └────────────┘ │
│  └───────────┘ │                │                │                 │
├────────────────┴────────────────┴────────────────┴─────────────────┤
│                         MCP / Hook Layer                           │
└─────────────────────────────────────────────────────────────────────┘
```

### 6.2 与 Agent 集成

**LLMAgent**：Skill 的核心消费者。激活 Skill 时将指令注入 SystemPrompt，影响 LLM 的推理行为。

```go
// LLMAgent 扩展
type LLMAgent struct {
    // ... 现有字段 ...
    skillManager  SkillManager  // Skill 管理器
    activeSkills  []string      // 当前激活的 Skill 名称列表
}
```

**RouterAgent**：可根据 Skill 匹配结果路由请求。RouteFunc 可参考 SkillRegistry.Match() 选择最合适的 Agent + Skill 组合。

**WorkflowAgent**：DAG 节点可在不同步骤激活/卸载不同 Skill，实现渐进式专家切换。

### 6.3 与 Tool 集成

Skill 通过 `allowed-tools` 声明可使用的工具。激活 Skill 时：

1. 从 ToolRegistry 中筛选 `allowed-tools` 列表中的工具
2. 仅将这些工具暴露给当前 Agent 迭代
3. 未在白名单中的工具调用被拒绝

```go
// SkillToolFilter 根据激活的 Skill 过滤可用工具
func SkillToolFilter(registry ToolRegistry, activation *SkillActivation) []ToolDef {
    if len(activation.AllowedTools) == 0 {
        return registry.List() // 无限制
    }
    var filtered []ToolDef
    for _, name := range activation.AllowedTools {
        if def, ok := registry.Get(name); ok {
            filtered = append(filtered, def)
        }
    }
    return filtered
}
```

### 6.4 与 Memory 集成

- **Session Memory**：记录 Skill 激活历史，支持跨 Run 保持 Skill 状态
- **Working Memory**：Skill 指令在 Working Memory 中占用空间，需纳入 ContextCompressor 管理
- **Persistent Store**：可存储 Skill 执行效果评估，用于优化后续 Skill 选择

### 6.5 与 Guard 集成

Skill 输入输出同样经过 Guard 检查链：
- InputGuard：检查 Skill 激活请求的合法性
- OutputGuard：检查 Skill 引导下生成内容的安全性
- 新增 `SkillGuard`：验证 Skill 来源可信度、目录结构合规性

### 6.6 与 Hook 集成

新增事件类型：

| 事件类型             | 说明                  | 数据                              |
| -------------------- | --------------------- | --------------------------------- |
| `SkillDiscover`      | Skill 发现            | 目录路径、发现数量                |
| `SkillActivate`      | Skill 激活            | SkillName、SessionID、Timestamp   |
| `SkillDeactivate`    | Skill 卸载            | SkillName、SessionID、Duration    |
| `SkillResourceLoad`  | 资源按需加载          | SkillName、ResourceType、ResourceName |

---

## 7. 安全设计

### 7.1 威胁分析

> 行业审计显示 41.7% 的 Skill 包含安全漏洞，82.4% 的 LLM 会在 peer agent 请求下执行恶意工具调用。

| 威胁               | 风险等级 | 缓解措施                             |
| ------------------ | -------- | ------------------------------------ |
| 恶意 Skill 注入    | 高       | 来源验证、签名校验                   |
| 工具越权调用       | 高       | allowed-tools 白名单强制执行         |
| 文件系统逃逸       | 高       | 沙箱执行、路径限制、符号链接防护     |
| Prompt 注入        | 中       | 现有 PromptInjectionGuard 覆盖       |
| 上下文窗口耗尽     | 中       | 500 行限制、上下文压缩器管理         |
| 网络外联           | 中       | 脚本网络访问控制                     |
| 配置篡改           | 中       | 禁止 Skill 修改 Hook、配置、其他 Skill |

### 7.2 安全控制

```go
// SkillValidator validates skill definitions before registration.
type SkillValidator interface {
    Validate(def *SkillDef) error
}
```

**内建校验器**：
- `NameValidator`：命名规范校验
- `ToolWhitelistValidator`：工具白名单合法性
- `SizeValidator`：指令长度限制
- `StructureValidator`：目录结构合规
- `SignatureValidator`：来源签名验证（可选）

### 7.3 脚本沙箱

Skill 脚本执行通过沙箱环境隔离：
- 限制文件系统访问范围（仅 Skill 目录和工作目录）
- 限制网络访问（可配置）
- 执行超时控制
- 资源使用限制（CPU、内存）

---

## 8. 模块结构

```
vagent/
├── skill/                # Skill 系统
│   ├── skill.go          # SkillDef、SkillResource、SkillActivation 模型定义
│   ├── loader.go         # SkillLoader 接口与 FileSkillLoader 实现
│   ├── registry.go       # SkillRegistry 接口与内存实现
│   ├── manager.go        # SkillManager 接口与实现
│   ├── validator.go      # SkillValidator 接口与内建校验器
│   ├── sandbox.go        # 脚本沙箱执行
│   └── loader_test.go    # 单元测试
└── schema/
    └── event.go          # 新增 Skill 相关事件类型
```

### 包依赖关系

```
agent ──→ skill (SkillManager)
skill ──→ tool  (ToolRegistry, 工具白名单验证)
skill ──→ schema (SkillDef, Event)
service ──→ skill (Skill 注册与发现)
```

---

## 9. 使用示例

### 9.1 基础用法

```go
// 1. 创建 Skill 加载器和注册表
loader := skill.NewFileSkillLoader()
registry := skill.NewRegistry()

// 2. 发现并注册项目 Skills
skills, _ := loader.Discover(".skills/")
for _, s := range skills {
    registry.Register(s)
}

// 3. 创建 Skill 管理器
manager := skill.NewManager(registry, toolRegistry)

// 4. 创建 Agent 并关联 Skill 管理器
agent := llmagent.New(
    llmagent.WithSkillManager(manager),
    // ... 其他配置
)

// 5. 激活 Skill（在 Run 前或 Run 过程中动态激活）
manager.Activate(ctx, "pdf-processing", sessionID)

// 6. 执行 Agent
resp, _ := agent.Run(ctx, req)
```

### 9.2 动态 Skill 选择

```go
// RouterAgent 根据任务描述匹配 Skill
routeFunc := func(ctx context.Context, req *schema.RunRequest) (*routeragent.RouteResult, error) {
    query := req.Messages[len(req.Messages)-1].Content
    matched := skillRegistry.Match(query)
    if len(matched) > 0 {
        manager.Activate(ctx, matched[0].Name, req.SessionID)
    }
    return &routeragent.RouteResult{Agent: targetAgent}, nil
}
```

### 9.3 Workflow 中的 Skill 切换

```go
// DAG 节点间切换不同 Skill
nodes := []orchestrate.Node{
    {ID: "analyze", Runner: analysisAgent, /* 激活 data-analysis skill */},
    {ID: "visualize", Runner: vizAgent, Deps: []string{"analyze"}, /* 激活 visualization skill */},
    {ID: "report", Runner: reportAgent, Deps: []string{"visualize"}, /* 激活 report-writing skill */},
}
```

---

## 10. Skill 组合模式

| 模式         | 说明                                       | vagent 对应                      |
| ------------ | ------------------------------------------ | -------------------------------- |
| 顺序链       | Skill 按序激活，前一个的输出作为后一个输入  | WorkflowAgent 顺序模式           |
| 并行执行     | 多个 Skill 同时激活处理独立子任务           | DAG 并行节点                     |
| 层级嵌套     | 父 Skill 分解目标为子 Skill                | 嵌套 Agent + Skill 组合          |
| 路由分派     | 根据意图选择合适的 Skill                   | RouterAgent + Match()            |
| 渐进式披露   | 按需加载 Skill，避免上下文浪费             | SkillManager.Activate/Deactivate |
| 生成-评审    | 一个 Skill 生成内容，另一个验证             | 循环节点 + 双 Skill 切换         |

---

## 11. 参考资料

- [Agent Skills 规范](https://agentskills.io/specification)
- [Agent Skills GitHub 仓库](https://github.com/agentskills/agentskills)
- [Anthropic Agent Skills 文档](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview)
- [Microsoft Semantic Kernel Plugins](https://learn.microsoft.com/en-us/semantic-kernel/concepts/plugins/)
- [LangChain Skills](https://blog.langchain.com/langchain-skills/)
- [OpenAI Agent Skills (Codex)](https://developers.openai.com/codex/skills/)

# 领域:tooling(工具、MCP 与技能)

| 元数据 | 值 |
|--------|----|
| 业务组 | capability |
| 一句话 | Agent 可调用能力的注册、执行与治理:本地工具、MCP 协议、Agent Skills |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | agent-core(`schema`)、session/sessionview(部分工具)、security(MCP 边界脱敏) |
| 对外 API | 是(Go 库 API + MCP 协议) |
| 覆盖包 | `tool`、`mcp`、`skill` |

## 概述

本领域是 Agent "会做什么"的来源。

- `tool`:工具注册表与执行,及一批内建工具(文件读写/编辑/查找、bash、web、todo、workspace、sessiontree、vectorsearch、agenttool、askuser 等)。
- `mcp`:Model Context Protocol 的 client(消费外部工具)与 server(把 Agent 能力暴露为 MCP 工具)。
- `skill`:兼容 [Agent Skills](https://agentskills.io) 开放标准的技能包,向 Agent 注入提示并过滤可用工具。

**边界(不做):** 工具只声明"做什么"与参数 schema,不决定"何时被调用"(那是 Agent 的 ReAct 决策);技能不实现工具,只声明激活条件、注入提示与筛选工具。

## 核心实体(概念层)

- **ToolRegistry(工具注册表)**:工具的注册与按名查找/执行入口。有截断变体,对过大工具输出做治理。
- **ExecuteMiddleware(工具执行中间件)**:Registry 级装饰器,包裹完整分派(本地 handler / 外部 caller / 注册表自身错误)。是工具调用级横切策略(审计、权限阻断、结果/错误改写)的入口;不合并 Agent / Stream / 模型中间件。
- **结果取值与截断助手**:工具结果取文本(`ToolResult.Text`)与字节级 UTF-8 截断(`TruncateUTF8`)是框架自带的通用能力,与"多大算大"的治理策略分开;前者只解释数据,后者才是策略。
- **工具三来源**:本地函数、MCP 远程、agent-as-tool(把一个 Agent 当工具)。
- **ResourceTracker(资源追踪)**:工具声明其读/写的资源(如文件),供上下文编辑判定 stale_resource、供编排做资源限流。
- **内建工具族**:文件类(read/write/edit/glob/grep)、执行类(bash,进程隔离)、协作类(agenttool 子代理、askuser 询问用户)、状态类(todo、workspace、sessiontree)、检索类(vectorsearch、webfetch、websearch)。 `askuser` has two non-substitutable collaboration paths; see TOOL-9 and [tooling-design](tooling-design.md).
- **MCPClient / MCPServer**:MCP 协议两端,带生命周期管理与凭证扫描(ScanEvent)。
- **Skill(技能)**:Def(定义)+ Resource(资源)+ Activation(激活条件);经 Loader 加载、Registry 索引、Manager 激活、Validator 校验。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| TOOL-1 | **参数按 schema 校验**:工具调用参数以其 JSON Schema 校验;内建工具做严格参数校验,多数不接受任意路径参数。 |
| TOOL-2 | **bash 进程隔离**:执行外部命令的工具必须进程隔离,不共享框架进程权限(章程安全基线)。 |
| TOOL-3 | **MCP 边界脱敏**:跨 MCP client/server 的第三方 I/O 是攻击面,须经凭证扫描/脱敏(见 [guard](../../platform/guard/guard.md))。 |
| TOOL-4 | **工具输出可截断**:过大工具输出经截断注册表治理,避免撑爆上下文。 |
| TOOL-5 | **技能只注入不实现**:技能向 Agent 注入提示、筛选可用工具集,不承载工具执行逻辑。 |
| TOOL-6 | **agent-as-tool 隔离**:子代理通过 `sessionview` 只读快照运行,scratch 隔离(见 [session](../../memory/session/session.md) SES-6)。 |
| TOOL-6b | **agent-as-tool 不支持 nested HITL**:子代理返回挂起响应(`StopReasonInterrupted` 或带 `Interrupt`)时,handler 在提取 assistant 文本与 child-session 注记之前拒绝,返回 `IsError=true` 的错误结果 —— 沿用 TOOL 既有的「子 Agent 执行问题以错误 ToolResult 暴露给父模型」契约,Registry 调用本身仍不改成 Go error。错误不复制半截文本、不转发 `interrupt_id`,因为父模型既无法据此询问人类也无法 resume(见 [agent-core](../../agent/agent-core/agent-core.md) AC-16)。 |
| TOOL-7 | **取文本与截断走框架入口**:"从工具结果取文本"与"按字节安全截断"是通用能力,只有一个推荐入口(`schema.ToolResult.Text` / `tool.TruncateUTF8`),调用方不再自建重复助手。字节上限与 token 预算是两件事,不可互换(详见 [tooling-design](tooling-design.md))。 |
| TOOL-8 | **执行中间件包裹完整分派**:`WithExecuteMiddleware` 装饰 `Registry.Execute` 的整段查找与执行;本地、外部与"未找到/无 handler"等分派错误都在链内可观察可改写。第一个注册者最外层;nil 跳过;链在构造时固定。并发 `Execute` 共享同一组中间件实例,有状态实现须自保并发安全。直接调用某个 `ToolHandler` 会绕过该链。 |
| TOOL-9 | **askuser's two collaboration paths do not substitute**: blocking mode (`tool/askuser.Register` + `UserInteractor`) waits inside the handler; in-process, timeout or process exit ends it, with no framework suspend record. The cross-process path is `taskagent.WithInterruptPolicy` taking over the same `ask_user` name — on a hit the handler does not run; the framework persists an `interrupt.Record` and returns `StopReasonInterrupted`, resumed by `ResumeInterrupt`. Pick one path; do not wrap the blocking mode as a fake cross-process Resume (see [agent-core](../../agent/agent-core/agent-core.md) AC-14, [orchestration](../../agent/orchestration/orchestration.md) OR-9). |
| TOOL-10 | **TaskAgent 工具披露 fail-closed**:一次新 Run 的模型可见工具集是「`RunOptions` 请求范围 ∩ 活跃 skill `AllowedTools` ∩ `EnabledFunc`」的交集,任一层只能收紧。`ToolMode=none`、`allow` 加空名单、交集为空,都使模型看到空工具表,不得回退注册表全集。`tool.FilterTools` 对其他调用方仍保持「空名单 = 不限制」;`prepareFrozenAITools` 才是冻结空集路径。`EnabledFunc` 返回 error 时排除该工具并发脱敏 `tool_excluded` 事件,不得 fail-open。ResumeInterrupt 使用 v2 快照中的最终工具名,不再查询当前 skill 或重跑谓词(见 [agent-core](../../agent/agent-core/agent-core.md) AC-19)。 |

## 状态与转换

MCP client/server 有生命周期(连接/就绪/关闭)。技能有激活状态(按 Activation 条件决定是否对当前请求生效)。工具调用本身无状态,每次独立执行。

## 与其他领域的交互

- **agent-core**:工具经注册表暴露给任务型 Agent;技能管理器向其注入提示与工具过滤。
- **model**:工具的 ResourceTracker 支撑上下文编辑的 stale_resource 判定。
- **security**:MCP 边界的凭证脱敏。
- **session/sessionview**:状态类与协作类工具消费会话/视图。
- **vector**:vectorsearch 工具经 `vector` 领域做召回。

技术实现(注册表、内建工具目录、MCP 协议实现)见 [tooling-design](tooling-design.md)。

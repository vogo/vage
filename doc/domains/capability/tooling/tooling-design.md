# 设计:tooling

对应领域行为见 [tooling.md](tooling.md)。

## 组件与职责

| 包/关键类型 | 设计角色 |
|-------------|----------|
| `tool`(`Registry`/`ToolRegistry`/`ToolExecutor`/`ExternalToolCaller`) | 工具注册与执行契约 |
| `tool`(`ExecuteMiddleware`/`WithExecuteMiddleware`) | Registry 执行链装饰:审计、权限、结果/错误改写 |
| `tool`(`TruncatingToolRegistry`) | 过大输出截断治理(token 级);位于 Registry 中间件之外 |
| `tool`(`TruncateUTF8`) | 字节级 UTF-8 安全截断的统一入口 |
| `schema`(`ToolResult.Text`) | 工具结果取文本的唯一推荐入口(契约层,见 [agent-core](../../agent/agent-core/agent-core.md)) |
| `tool`(`ResourceTracker`/`ResourceRef`) | 工具资源语义,供上下文编辑与编排限流 |
| `tool/bash` | 进程隔离的命令执行 |
| `tool/read`/`write`/`edit`/`glob`/`grep` | 文件类工具,`tool/toolkit` 提供共享路径校验 |
| `tool/agenttool` | agent-as-tool,发布 `sessionview` 到子代理 context |
| `tool/askuser` | 向用户提问 |
| `tool/todo`/`workspace`/`sessiontree` | 会话级状态工具 |
| `tool/vectorsearch`/`webfetch`/`websearch` | 检索类工具 |
| `mcp/client`(`MCPClient`/`Lifecycle`/`ScanEvent`) | 消费外部 MCP 工具,带生命周期与凭证扫描 |
| `mcp/server`(`MCPServer`/`ToolRegistration`/`ScanEvent`) | 暴露 Agent 能力为 MCP 工具 |
| `skill`(`Loader`/`Registry`/`Manager`/`Validator`/`Def`/`Activation`) | 技能加载、索引、激活、校验 |

## 关键设计决策

- **注册表作为唯一工具入口**:Agent 只认 `ToolRegistry` 接口,三种来源(本地/MCP/agent-as-tool)对 Agent 无差别。
- **执行中间件包裹完整分派,而非逐 handler 装饰**:`ExecuteMiddleware` 以 `func(next ToolHandler) ToolHandler` 装饰 `Registry.Execute` 的查找+执行路径,因此同一条策略覆盖本地、MCP、agent-as-tool,也能审计被拒绝或无法路由的尝试。第一个注册者最外层(`A.before → B.before → dispatch → B.after → A.after`);`nil` 跳过;链在 `NewRegistry` 后固定,分派仍每次读取当下注册表状态。`TruncatingToolRegistry` 仍是外层结果治理:Registry 中间件完成后再截断。不向 `ToolExecutor`/`ToolRegistry` 接口加方法。
- **并发与绕过责任在实现者**:并发 `Execute` 共享同一组中间件实例,可变状态须自保安全;锁只用于取得分派快照。直接调用某个 `ToolHandler` 绕过中间件 —— 需要强制策略时须在统一构造 Registry 时配置。中间件可见原始参数与结果,审计不得无差别记录敏感载荷。
- **内建工具刻意收窄参数面**:多数文件/状态工具做严格校验、不接受任意路径,把危险操作面缩到最小(纵深防御,配合 bash 进程隔离)。
- **资源语义显式化**:工具通过 `ResourceTracker` 声明读/写资源,使上下文编辑能识别"后写作废前读",使编排能按资源标签限流 —— 一处声明,多处复用。
- **MCP 边界是攻击面**:client/server 两端都内置 `ScanEvent` 凭证扫描,与 `security`(credscrub)协作,防止第三方 I/O 泄露凭证。
- **通用取值收在框架,策略留在治理层**:"从结果取文本"和"按字节安全截断"是每个调用方都要写一遍的通用逻辑,散落各处就会各自漂移,因此收成两个稳定入口 —— `schema.ToolResult.Text()` 与 `tool.TruncateUTF8()`。而"多大算大、截断后留什么标记、错误结果要不要处理"是策略,仍归 `TruncatingToolRegistry`:它复用 `TruncateUTF8` 做边界裁切,但不把 token 阈值与标记格式下沉进通用助手。
- **技能四件套**:Loader(从文件加载)/ Registry(索引)/ Manager(激活)/ Validator(名称、大小、结构、组合校验)职责分离,兼容 Agent Skills 开放标准。

## 工具结果的取文本与截断

调用方(含示例与应用代码)应优先使用框架入口,不要再自建助手:

| 需求 | 用 | 语义要点 |
|------|----|----------|
| 从 `ToolResult` 取文本 | `result.Text()` | 只返回**第一个** `text` part;首个文本为空即返回空串。与 `Message.Text()`(拼接全部文本)刻意不同 —— 它保证读到的就是框架发给模型的那段内容。 |
| 把字符串压到长度上限 | `tool.TruncateUTF8(s, maxBytes)` | 上限单位是**字节**,不是字符、rune 或 token;结果是不超限的最长有效 UTF-8 前缀,不加省略号。 |
| 防止工具输出撑爆上下文 | `TruncatingToolRegistry` | 按**估算 token** 阈值治理,跳过错误结果并附加可观察的截断标记。 |

两类截断不可互换:字节截断服务于日志、展示与命令输出这类有硬字节边界的 sink;上下文预算必须走 token 级治理。误把字节上限当成 token 数,是这组 API 最主要的失效模式。

历史助手 `tool/toolkit.TruncateUTF8` 与 `tool/toolkit.ResultText` 已 deprecated,保留为薄委托以免既有调用方编译失败;新代码直接用上表的入口。多文本 part 或多模态结果需要完整表示时,仍应直接读 `ToolResult.Content` —— `Text()` 只承诺便捷读取,不承诺完整性。

## Two collaboration paths for ask_user

`tool/askuser` implements only the blocking path. The cross-process path is not extra code in that package — it is a TaskAgent policy layered on the same tool name. The choice is made at Agent construction, not auto-switched at runtime:

- Same process, a human waiting, timeout acceptable → `tool/askuser.Register(registry, interactor)`. The handler calls `UserInteractor.AskUser` and blocks; timeout or process exit ends it, with no record another process can resume.
- Cross-process / cross-session approval that may wait minutes or hours → `taskagent.WithInterruptStore(store)` + `taskagent.WithInterruptToolNames("ask_user")` (or a custom `InterruptPolicy`). When the model requests `ask_user`, `runReactLoop` intercepts before the handler: the handler does not run; after `interrupt.Store.Create` succeeds the call returns `StopReasonInterrupted` and an `interrupt_id`. Another process submits the human text as that `tool_call_id`'s decision via `TaskAgent.ResumeInterrupt` and continues the original batch.

Both paths share the `ask_user` name and parameter schema. The only difference is whether the Agent's `InterruptPolicy` flags it: a hit takes over and the handler never runs; a miss (or no interrupt configured) is the existing blocking path, with no implicit switch. Registering both is pointless — once `ask_user` is flagged, the `tool/askuser` handler is never reached.

## 非功能考量

- **安全**:bash 进程隔离;文件工具路径校验集中在 `toolkit`;MCP I/O 脱敏;执行中间件是可选策略接缝而非自动安全边界。
- **上下文治理**:截断注册表防止单个工具输出撑爆提示;通用字节截断与之分工明确,不承担 token 预算职责。
- **可扩展**:新增工具 = 实现执行契约并注册;新增技能 = 提供符合标准的技能包;横切策略 = `WithExecuteMiddleware`。

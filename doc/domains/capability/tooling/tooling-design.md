# 设计:tooling

对应领域行为见 [tooling.md](tooling.md)。

## 组件与职责

| 包/关键类型 | 设计角色 |
|-------------|----------|
| `tool`(`Registry`/`ToolRegistry`/`ToolExecutor`/`ExternalToolCaller`) | 工具注册与执行契约 |
| `tool`(`TruncatingToolRegistry`) | 过大输出截断治理 |
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
- **内建工具刻意收窄参数面**:多数文件/状态工具做严格校验、不接受任意路径,把危险操作面缩到最小(纵深防御,配合 bash 进程隔离)。
- **资源语义显式化**:工具通过 `ResourceTracker` 声明读/写资源,使上下文编辑能识别"后写作废前读",使编排能按资源标签限流 —— 一处声明,多处复用。
- **MCP 边界是攻击面**:client/server 两端都内置 `ScanEvent` 凭证扫描,与 `security`(credscrub)协作,防止第三方 I/O 泄露凭证。
- **技能四件套**:Loader(从文件加载)/ Registry(索引)/ Manager(激活)/ Validator(名称、大小、结构、组合校验)职责分离,兼容 Agent Skills 开放标准。

## 非功能考量

- **安全**:bash 进程隔离;文件工具路径校验集中在 `toolkit`;MCP I/O 脱敏。
- **上下文治理**:截断注册表防止单个工具输出撑爆提示。
- **可扩展**:新增工具 = 实现执行契约并注册;新增技能 = 提供符合标准的技能包。

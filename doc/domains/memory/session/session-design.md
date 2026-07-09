# 设计:session

对应领域行为见 [session.md](session.md)。

## 组件与职责

| 包/关键类型 | 设计角色 |
|-------------|----------|
| `session`(Session 实体、事件流、状态 KV、存储后端) | 一等会话,可插拔存储 |
| `session/tree`(`SessionTreeStore`,分层会话树) | 每会话分层记忆:游标、缩放、提升 |
| `session/metrics*.go` | 会话级指标与指标 hook |
| `workspace`(plan + notes 草稿区) | 每会话持久工作区,原子写 + 会话锁 |
| `sessionview`(只读快照 + scratch 槽 + 资源预算) | agent-as-tool 分发载体,经 context 传递 |

## 关键设计决策

- **会话即实体,而非标签**:从"session_id 是记忆条目上的字符串"演进为可寻址实体,使对话能被持久、列出、跨运行恢复。
- **刻意的关注点分离**:会话不揽检查点语义;工作区刻意保持窄形状(MVP);视图只搬运快照不构建它。每个包都用"不做什么"守住边界。
- **视图走 context 而非结构字段**:子代理视图通过 `context.Context` 注入,避免污染 `agent.Agent` 接口,保持 Agent 抽象稳定。
- **文件后端的运维友好**:工作区与会话遵循一致约定(名称校验、原子写、`0o700/0o600` 权限、per-session 进程内锁),便于本地与生产一致排查。

## SessionTree 工具面

`session/tree` 通过 `tool/sessiontree` 暴露给 LLM 的操作刻意收窄:tree_add / tree_update / tree_cursor / tree_promote / tree_zoom_in,严格参数校验、无路径参数、事件发射委托底层 store。设计意图是让模型只能做受控的树操作,不能任意读写文件系统。

## 非功能考量

- **并发**:同一会话的写入经进程内锁串行化;跨会话并发安全。
- **隔离**:子代理 scratch 与父 notes 物理隔离,retry 清空,防止失败尝试污染父状态(章程 AI 工程原则:上下文隔离)。
- **可恢复**:会话可列出、可跨运行恢复;与 `checkpoint` 的续跑快照互补而非重叠。

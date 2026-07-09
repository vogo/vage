# 领域:session(会话、工作区与子代理视图)

| 元数据 | 值 |
|--------|----|
| 业务组 | memory |
| 一句话 | 一等会话实体、每会话持久工作区、agent-as-tool 分发时的父会话只读视图 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | 无内部强依赖(被 memory、agent-core、tooling 消费) |
| 对外 API | 是(Go 库 API) |
| 覆盖包 | `session`、`workspace`、`sessionview` |

## 概述

本领域把"对话"提升为可寻址、可持久、可跨运行恢复的一等实体,并为多代理协作提供状态载体。

- `session`:会话实体 —— 身份 + 追加型事件流 + 结构化状态 KV + 可插拔存储后端。
- `workspace`:每会话的持久"计划 + 笔记"草稿区(plan.md 单文档 + notes/ 扁平目录)。
- `sessionview`:分发子代理(agent-as-tool)时,交给子代理的一份父会话**只读冻结快照** + scratch 槽 + 可选资源预算。

**边界(不做):** `session` 刻意**不含**检查点/快照语义(那是 `checkpoint` 领域);`workspace` 刻意窄 —— plan 只整体替换不打补丁,notes 只按名索引,不做工件/子任务/schema 校验;`sessionview` 只承载快照,不规定快照如何构建。

## 核心实体(概念层)

- **Session(会话)**:身份 + 追加型事件流 + 结构化状态 KV。
- **事件流**:追加型,记录对话中发生的事实。
- **结构化状态**:KV,覆盖语义(同键后写覆盖先写)。
- **SessionTree(会话树)**:分层的会话记忆模型(每会话一棵树,支持游标、缩放、提升)。
- **Workspace(工作区)**:plan.md(单一 Markdown 计划,整体替换)+ notes/(扁平笔记目录)。
- **SessionView(会话视图)**:子代理拿到的只读快照 —— 子会话身份(指回父会话)、界定子目标的自然语言、per-subtask scratch 槽、可选资源预算、父状态冻结快照(计划正文 + 笔记索引)。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| SES-1 | **事件只追加**:会话事件流不可修改或删除已有事件。 |
| SES-2 | **结构化状态覆盖语义**:同键写入覆盖旧值。 |
| SES-3 | **会话不含检查点**:续跑快照属 `checkpoint` 领域,不混入会话。 |
| SES-4 | **工作区原子写 + 隔离**:文件原子写、`0o700/0o600` 权限、按会话进程内加锁;名称必须过模式校验。 |
| SES-5 | **计划整体替换**:plan.md 只能整体替换,不做增量打补丁。 |
| SES-6 | **子代理视图只读**:`sessionview` 交给子代理的父状态是冻结只读快照;子代理的 scratch 写入不得污染父会话 notes,重试时被清空。 |
| SES-7 | **视图经 context 传递**:视图随 `context.Context` 流动,不改变 `agent.Agent` 的结构字段。 |

## 状态与转换

会话生命周期:创建 → 追加事件 / 覆盖状态(对话进行中)→ 可列出、可跨运行恢复。工作区随会话存在;子代理视图在 agent-as-tool 分发时由父会话冻结生成,随子代理运行消费,retry 时 scratch 清空。

## 与其他领域的交互

- **memory**:会话事件与状态是上下文装配 Source 的事实来源。
- **agent-core / tooling**:agent-as-tool 分发时,`sessionview` 把父快照发布进子代理 context;子代理侧的 scratch/tree 工具消费它。
- **checkpoint**:与本领域互补 —— 会话是"事实流",检查点是"续跑快照",读路径与生命周期各自独立。

技术实现(存储后端、SessionTree 工具、原子写)见 [session-design](session-design.md)。

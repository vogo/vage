# 领域:memory(记忆与上下文装配)

| 元数据 | 值 |
|--------|----|
| 业务组 | memory |
| 一句话 | 三层记忆模型、把事实装配成 LLM 提示的可插拔管线、历史压缩策略 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | session(事实来源)、model(压缩用到的模型能力)、vector(召回) |
| 对外 API | 是(Go 库 API) |
| 覆盖包 | `memory`、`context`(import 名 `vctx`) |

## 概述

本领域回答:**Agent 记住什么、记多久,以及每一轮到底把什么发给模型。**

- `memory`:三层记忆模型与压缩。把"session_id 只是记忆条目上的字符串标签"提升为有层级的记忆管理。
- `context`(`vctx`):显式的 Builder / Source 抽象,把"存在哪些事实"(会话、记忆、状态存储)组装为"发给 LLM 的消息序列"。装配过程可插拔、可审计。

**边界(不做):** `context` 不做记忆存储本身,只做装配;`memory` 不做会话实体建模(那是 `session` 领域)。

## 核心实体(概念层)

- **记忆三层**:
  - **WorkingMemory(工作记忆)** —— 单次请求内的临时上下文。
  - **SessionMemory(会话记忆)** —— 单次对话内的多轮历史。
  - **Store(长期记忆)** —— 跨对话的长期事实;「长期」指跨会话,是否跨进程重启存活由后端决定。
- **Memory / Store 接口**:记忆读写的统一抽象,带批量变体。
- **Promoter(提升器)**:把低层记忆提升到高层(working→session→store)。
- **ContextCompressor(压缩器)**:在 token 约束下压缩历史。内置多种策略:滑动窗口、重要度排序、摘要+截断、token 预算、按需摘要(`SummarizeWhenOverBudget`)、以及可组合的压缩链。预算感知入口接收完整 `Budget`,旧 `Compress(..., maxTokens)` 仍可用。
- **ConversationCompactor(会话压紧)/ Archiver(归档器)**:压紧多轮对话、把旧内容归档(可配合向量存储)。
- **Builder / Source(上下文装配)**:Builder 产出最终消息序列;每个 Source 贡献一段(系统提示、会话记忆、额外来源、请求消息、向量召回……)。**唯一窗口裁决点是 Builder**:Source 可做摘要、TopK、单文档/plan 字节上限,但不得把模型总窗口当作独占额度。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| MEM-1 | **层级单向提升**:记忆只能 working→session→store 向上提升,不可逆向流动。 |
| MEM-2 | **统一窗口预算**:一次 context build 共用 `memory.Budget`。固定内容(system / request / tools / 输出预留)先计费且不可淘汰;可选 Source 共享 `AvailableHistory`。超额时 Builder 按声明顺序从可丢候选头部删除(对按时间排序的 session history 即最旧优先),丢弃写入 `BuildReport` 与 `EventContextBuilt`。固定内容本身超窗则 fail-closed,不得静默超窗调用模型。 |
| MEM-3 | **装配顺序确定**:默认 Builder 按 系统提示 → 会话记忆 → 额外来源 → 请求消息 的稳定顺序拼装。 |
| MEM-4 | **事实与提示分离**:Source 只读取事实来源,不改写它们;装配结果不回写记忆。 |
| MEM-5 | **压缩可组合**:多个压缩器可串成链,按序施加,每个只负责单一维度。 |
| MEM-6 | **选择性提升/归档不丢源**:`PromoteWhen`/`ArchiveWhen` 只过滤不删除——不匹配谓词的条目留在原层,归档不触碰 session 源数据;谓词元数据由 `Value` 自行携带(`Importance() float64` / `Tags() []string`),未携带者在选择性谓词下不匹配。 |
| MEM-7 | **会话记忆按声明的二元组隔离**:不同 `(agentID, sessionID)` 的 session 记忆在读、写、提升和清理上互不可见。scope 是调用方传入的身份声明,不是 TenantID/UserID 授权。空 SessionID 跳过 session 记忆(Run 照常执行)。同一二元组下并发 Run 仍可能用相同 `msg:%06d` 偏移互相覆盖——该限制本阶段不修。 |
| MEM-8 | **装配预算由调用方传入**:`vctx.BuildInput.Budget` 非零时,Builder 只裁剪 optional Source;system/request 等 must-include 来源仍完整保留。`0` 表示无限。TaskAgent 把解析后的 `RunTokenBudget` 接到该字段(旧字段 `0` 仍是 Agent 默认/无限,只有 `Limits.RunTokenBudget = ptr(0)` 才是新契约下的显式无限)。 |

## 状态与转换

记忆条目生命周期:产生(working)→ 对话内累积(session)→ 满足提升条件时上升(store)→ 超预算时被压缩/归档。压缩与 Builder 裁剪只改变"发给模型的表示",不删除 session/workspace 中的事实。

## 非目标(本领域)

- 不跨 Run 持久化 `Budget` 或上次 `AvailableHistory`;每次 build 从当前窗口、输出限制、tools 和 Source 内容重算。
- 不自动切换更大窗口模型,不引入 provider 专属 tokenizer,估算不是账单 usage。
- token 估算是集中式启发(默认 `len/4`);应保留安全余量,`TargetUtilization(0.8)` 是推荐默认而非精确保证。

## 与其他领域的交互

- **session**:会话事件与结构化状态是 Source 的主要事实来源。
- **model**:摘要类压缩器调用模型能力生成摘要。
- **vector**:向量召回 Source 通过 `vector` 领域拉取相关历史片段。
- **agent-core**:任务型 Agent 通过 memory.Manager 读写会话记忆,通过 Builder 装配每轮提示,并把本次 Run 的有效 token 预算传入 `BuildInput.Budget`。

技术实现(压缩器组合、Source 目录、token 估算)见 [memory-design](memory-design.md)。

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
  - **Store(持久记忆)** —— 跨对话的持久事实。
- **Memory / Store 接口**:记忆读写的统一抽象,带批量变体。
- **Promoter(提升器)**:把低层记忆提升到高层(working→session→store)。
- **ContextCompressor(压缩器)**:在 token 约束下压缩历史。内置多种策略:滑动窗口、重要度排序、摘要+截断、token 预算、以及可组合的压缩链。
- **ConversationCompactor(会话压紧)/ Archiver(归档器)**:压紧多轮对话、把旧内容归档(可配合向量存储)。
- **Builder / Source(上下文装配)**:Builder 产出最终消息序列;每个 Source 贡献一段(系统提示、会话记忆、额外来源、请求消息、向量召回……)。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| MEM-1 | **层级单向提升**:记忆只能 working→session→store 向上提升,不可逆向流动。 |
| MEM-2 | **token 预算不可超**:压缩在预算约束下进行;预算耗尽触发压缩而非静默截断丢失。 |
| MEM-3 | **装配顺序确定**:默认 Builder 按 系统提示 → 会话记忆 → 额外来源 → 请求消息 的稳定顺序拼装。 |
| MEM-4 | **事实与提示分离**:Source 只读取事实来源,不改写它们;装配结果不回写记忆。 |
| MEM-5 | **压缩可组合**:多个压缩器可串成链,按序施加,每个只负责单一维度。 |

## 状态与转换

记忆条目生命周期:产生(working)→ 对话内累积(session)→ 满足提升条件时上升(store)→ 超预算时被压缩/归档。压缩不改变事实层级,只改变"发给模型的表示"。

## 与其他领域的交互

- **session**:会话事件与结构化状态是 Source 的主要事实来源。
- **model**:摘要类压缩器调用模型能力生成摘要。
- **vector**:向量召回 Source 通过 `vector` 领域拉取相关历史片段。
- **agent-core**:任务型 Agent 通过 memory.Manager 读写会话记忆,通过 Builder 装配每轮提示。

技术实现(压缩器组合、Source 目录、token 估算)见 [memory-design](memory-design.md)。

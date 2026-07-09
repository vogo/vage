# 业务组:memory(记忆与会话状态)

本组负责 Agent 的"记忆"与"状态":从单次请求内的临时上下文,到跨对话的持久事实,以及把这些事实装配成发给模型的提示。

## 领域清单

| 领域 | 职责 | 覆盖包 |
|------|------|--------|
| [memory](memory/memory.md) | 三层记忆模型、上下文装配管线与压缩策略 | `memory`、`context` |
| [session](session/session.md) | 一等会话实体、持久工作区、子代理只读视图 | `session`、`workspace`、`sessionview` |

## 共享上下文

- **记忆三层**:working(请求)→ session(对话)→ store(持久),层级只能向上提升。
- **"事实"与"提示"分离**:`session`/`memory` 存"存在哪些事实",`context` 的 Builder 决定"发什么消息给 LLM"。二者解耦、可审计。
- 上下文膨胀由两条互补路径治理:`memory` 的历史压缩 + `model` 组 largemodel 的上下文编辑(折叠旧工具结果)。

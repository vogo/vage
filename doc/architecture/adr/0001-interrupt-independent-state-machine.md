# 0001. Independent Interrupt State Machine (vs. Extending Checkpoint)

- **Status:** proposed
- **Date:** 2026-08-28

## Context

TaskAgent 需要一种能跨进程恢复的人机协作暂停点——模型产生需要外部决策的工具调用后，在执行工具处理器之前挂起，把未完成工具批和 continuation 持久化，并允许另一进程按人工决策精确恢复。

现有 `checkpoint.IterationStore` 是面向"崩溃后从最近完整轮次重放"的迭代快照，其不变式是"这是一轮已完成的完整快照"；`tool/askuser` 是进程内同步阻塞、无框架级持久记录，超时或进程退出即结束。两者都不能安全地承载"执行前挂起、待决集合、租约、外部决定注入"这套语义：把执行前的挂起状态塞进 checkpoint 会打破其"完整轮次"假设，把跨进程恢复包装成阻塞 `ask_user` 的另一层则是伪装 API，不具备真实可恢复语义。

## Decision

新增独立的 `interrupt` 包，定义 `Record`/`Store` 持久化契约与 Pending → Ready → Resuming → Completed 状态机，由 TaskAgent 通过 `WithInterruptStore`/`WithInterruptPolicy` 组合使用；不把 interrupt 记录写入 `IterationStore`，也不让 `Resume(sessionID)` 兼管两种语义。

挂起判定点固定在共享 `runReactLoop` 内——取得完整 assistant 工具调用消息之后、预算检查之后、`executeToolBatch` 之前，同步与流式共用同一判定；命中后整批冻结，等待所有待决调用都有决定后才执行未命中的同批调用。`ResumeInterrupt(ctx, req)` 是新增的公共入口，按 `interrupt_id + tool_call_id` 精确寻址，不进 Agent middleware 链，不重跑输入护栏，Run 值从空表开始。

## Rationale

checkpoint 的"完整轮次快照"不变式与 interrupt 的"执行前挂起、待决未完成"不变式互斥，合并会污染两者各自的正确性假设；独立接口让两者可分别配置持久化后端、保留期与恢复语义。代价是多一套持久契约（`interrupt.Store`）和状态机，但换来：挂起前零同批副作用、可解释的恢复边界、以及不会让 `Resume(sessionID)` 的调用方猜测意图。

选择冻结整批工具调用而非逐个放行，牺牲部分并行度，换取挂起前零同批副作用、稳定的结果顺序和可解释的恢复边界。选择保存已解析的有效 Run 参数（`interrupt.EffectiveParams`）而非恢复时重新取 Agent 默认值，记录体更大，但新进程不会因默认配置变化而悄然改变剩余预算、工具范围或模型行为。选择租约（持有者 + 到期时间）而非永久锁，允许恢复进程崩溃后再次被接管，代价是不承诺端到端 exactly-once。

## Consequences

- 正面：`ask_user`（阻塞）、checkpoint（崩溃重放）、interrupt（人机协作挂起）三条路径边界清晰，互不替代，文档与代码可分别演进（见 [agent-core](../../domains/agent/agent-core/agent-core.md) AC-14、[orchestration](../../domains/agent/orchestration/orchestration.md) OR-9、[tooling](../../domains/capability/tooling/tooling.md) TOOL-9）。
- 正面：`interrupt.Store` 独立配置存储后端（`MapStore`/`FileStore`）与保留期，不受 checkpoint 生命周期约束；`FileStore` 用 `<id>.lock` 文件提供跨进程 `AcquireLease` 互斥，两个独立进程可安全竞争同一条记录的恢复权。
- 负面：多一个需要维护的持久契约与状态机；调用方需要理解三种相邻机制的边界。
- 负面：不承诺端到端 exactly-once——租约过期后被接管的普通工具可能重放，需要调用方为有副作用的工具提供幂等键或补偿。

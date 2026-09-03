# 领域:orchestration(DAG 编排与断点)

| 元数据 | 值 |
|--------|----|
| 业务组 | agent |
| 一句话 | DAG 编排多个 Runner,强类型工作流状态,及执行级检查点/挂起 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | agent-core(Runner 由 Agent 满足;仅依赖 `schema`) |
| 对外 API | 是(Go 库 API) |
| 覆盖包 | `orchestrate`、`workflow`、`checkpoint`、`interrupt` |

## 概述

本领域把"多个 Agent/工作单元按依赖关系跑起来"做成两套增量、互不替代的能力,并提供三条独立的断点/挂起续跑路径。

- `orchestrate`:DAG 执行引擎 —— 以 `RunRequest`/`RunResponse` 串联 Runner 的简单 Agent pipeline;并行调度、条件分支、循环、补偿(Saga)、背压、优先级调度、DAG 级检查点。`agent/workflowagent` 是它的调用方,不是 `workflow` 包。
- `workflow`: typed workflow state — callers define application struct `S`, nodes read `Snapshot[S]` and return `Patch[S]`, and the scheduler merges a logical batch only when Field write-sets are disjoint. Opt-in via `workflow.New[S]`; existing DAG callers do not migrate.
- `checkpoint`: ReAct **iteration-level** snapshot — crash/restart/SIGTERM replay of the latest completed turn.
- `interrupt`: ReAct **pre-tool-batch** human-in-the-loop suspend state machine — freeze a flagged batch, let another process inject decisions by `interrupt_id + tool_call_id`, and resume. This is an intentional pause for a human decision, not a crash.

**边界(不做):** 不做持久化/分布式工作流平台(无跨进程状态格式、无可视化编辑器、无 CRDT/自动冲突消解)。`orchestrate` 不定义业务节点语义(节点只是"一个 Runner")。`workflow` 首版不移植条件、循环、动态派生、重试、优先级、背压、补偿、early exit、checkpoint 或 replay。`checkpoint` (iteration snapshot), DAG-level checkpoints inside `orchestrate`, and `interrupt` (suspend state machine) are **three different mechanisms** with different consumers and read paths; they are not interchangeable. Typed workflow state lives only for one in-process `Run`.

## 核心实体(概念层)

- **Runner**:执行一个工作单元的抽象;`agent.Agent` 天然满足它。`workflow` 只依赖这一最小 `Run` 面,以免 L1 反向依赖 `agent`。
- **Node(节点)**:`orchestrate.Node` 是 DAG 中的一个 Runner 执行点,携带依赖、输入映射、可选性、条件、超时、重试、资源标签、优先级。`workflow.Node[S]` 是强类型图中的一个执行点:稳定 ID、依赖、以及 `Snapshot[S] → Patch[S]`。
- **Field / Snapshot / Patch**: typed handles, a read-only view of one committed `S`, and the explicit write-set a node submits. Conflict detection uses Field identity, not diagnostic name strings. `RunResponse.Metadata` is not the internal patch channel.
- **Logical batch**: every node whose dependencies are satisfied on the same committed state version. The batch shares one Snapshot; the scheduler waits for the whole batch before merge or the next version. Max concurrency only limits goroutines.
- **Edge(边)**:节点间的依赖关系。
- **LoopNode(循环节点)**:带循环体、终止条件、最大迭代与收敛判定的重复执行。
- **DynamicSpawnNode(动态派生节点)**:运行期按上游结果动态展开子任务。
- **ErrorStrategy(错误策略)**:节点失败时的处置 —— Abort(中止)/ Skip(跳过)/ Compensate(补偿回滚)。
- **Aggregator(聚合器)**:把多个节点结果汇聚为最终结果。
- **补偿(Compensation / Saga)**:对已成功节点执行回滚,配合幂等检查器保证重复补偿安全。
- **背压(Backpressure)**:按负载自适应调节并发度。
- **Checkpoint(检查点)**:某次迭代的完整可恢复快照(消息列表、累计用量、Final/StopReason 标记)。
- **Interrupt**: `interrupt.Record` — a pre-tool-batch suspend snapshot: full batch, pending unique subset, committed decisions, continuation, and the already-resolved effective Run parameters. State machine: Pending → Ready → Resuming → Completed. Submit never demotes Resuming to Ready.
- **interrupt.Store**: persistence contract. `MapStore` (single-process tests) and `FileStore` (cross-process; an OS advisory lock on `<id>.lock` serializes every mutation of that record, including `AcquireLease` and `Delete` — a live holder is never preempted by age, and a dead one releases on process exit).

## 业务规则与不变式

| ID | 规则 |
|----|------|
| OR-1 | **依赖满足才可调度**:节点仅在其全部依赖完成后进入就绪集。 |
| OR-2 | **无环**:DAG 构造期即校验环、缺依赖、重复 ID,违反则构造失败。 |
| OR-3 | **错误策略决定收尾**:Abort 立即停止并收敛;Skip 跳过失败的非关键(Optional)节点继续;Compensate 触发已提交步骤的回滚。 |
| OR-4 | **锁契约硬化**:执行器对共享执行状态的加锁/解锁在错误路径与取消路径上必须成对收尾,不得因提前返回而遗留持锁或重复解锁(近期硬化项)。 |
| OR-5 | **取消/错误收尾单一路径**:节点错误与外部取消收敛到同一收尾路径,保证无论何种终止都执行一致的清理与事件通知。 |
| OR-6 | **补偿幂等**:补偿动作经幂等检查器守护,重复触发不产生重复副作用。 |
| OR-7 | **检查点双轨分离**:迭代级检查点与 DAG 级检查点地址不同、读路径不同,不得相互引用或替代。 |
| OR-8 | **回放不执行**:DAG 回放模式(ReplayMode)从检查点重建状态而不真正执行 Runner。 |
| OR-9 | **Three persistence mechanisms do not substitute**: `ask_user` (handler already running, in-process blocking wait, no framework suspend record), `checkpoint` (crash-replay snapshot after a completed turn or a finished Run; `Resume(sessionID)` takes the latest complete turn and accepts no external decisions), `interrupt` (pre-tool-batch suspend state machine; `ResumeInterrupt` injects decisions by exact ID). Trigger point, persisted content, and resume semantics all differ. Do not write interrupt records into `IterationStore`, do not let `Resume(sessionID)` guess whether the caller wanted a checkpoint or an interrupt, and do not wrap `ask_user` as a fake cross-process Resume. |
| OR-10 | **挂起响应不是节点输出**:每个真正调用 `Runner.Run` 的执行边界(DAG 节点、循环体、条件节点、DynamicSpawn 的父与子 Runner、补偿的前向恢复、`workflow.AdaptRunner`)在把响应交给消费者前判定 `StopReason == interrupted` 或 `Interrupt != nil`,任一命中即转为节点执行错误(`errors.Is(err, orchestrate.ErrInterruptedRunner)` 或 `errors.Is(err, workflow.ErrInterruptedRunner)`)。命中的响应不写入 `NodeResults`、不写 DAG checkpoint,也不作为成功输入交给 InputMapper、Condition、Spawner、下一轮循环、下游 Runner、Aggregator、early-exit 判定或 typed-workflow 输出 mapper(不得产生 `Patch`)。该错误**不可自动重试**:节点 `Retries` 与前向恢复都不得再次调用同一 Runner,因为重新 Run 不是 `ResumeInterrupt`,只会另起一次执行并可能再创建一条 Pending 记录。错误照常进入既有失败收尾与 `ErrorStrategy`(Abort / Skip / Compensate),但即使调用方选择跳过或补偿,处理的也只是错误状态,绝不能重新消费该挂起响应。恢复通路只在顶层:见 agent-core AC-16。 |
| OR-11 | **Typed workflow merge is a batch barrier**: nodes in one logical batch see the same committed Snapshot and cannot observe sibling Patches. Merge inspects write-sets before any Field setter runs. The same Field written twice in one Patch, or by two or more nodes in the same batch, is a zero-commit `WriteConflictError` (`errors.Is` / `errors.As`), carrying the Field diagnostic name and stable-sorted node IDs. Distinct batches may write the same Field in order. There is no last-writer-wins, reducer, lock, or CRDT. A node failure or context cancel cancels and drains the current batch, commits nothing from it, and does not start later batches; the error unwraps to the node ID, and cancel remains `context.Canceled` / `DeadlineExceeded`. The scheduler only guarantees writes declared on a Patch — mutating Snapshot through shared references, setters that touch other fields, or mutating a reference after return are contract violations. |

## 状态与转换

节点状态在执行器内经历:等待依赖 → 就绪 → 运行 → (完成 | 失败 | 跳过 | 取消)。失败节点依 ErrorStrategy 进入中止 / 跳过 / 补偿。完整的触发条件、守卫与后置动作见 [orchestration-design](orchestration-design.md)。

## 领域事件

DAG 执行通过事件处理器发出:节点开始、节点完成(带状态与错误)、检查点错误。所有事件方法要求并发安全。

## 与其他领域的交互

- **agent-core**:节点里的 Runner 通常就是一个 Agent;工作流型 Agent 是本引擎的主要调用方。TaskAgent uses `checkpoint` for crash resume and `interrupt.Store` for tool-batch suspend/resume. The suspend gate, `ResumeInterrupt` contract, and events belong to agent-core AC-14; see [agent-core](../agent-core/agent-core.md). 本引擎只是这类 Agent 的**父层消费者**,不持有 `interrupt_id`、不提供 resume 入口:把配置了 interrupt 的 TaskAgent 放进节点或循环体,得到的是 OR-10 的可见执行错误,而不是一次可恢复的挂起。
- 本领域仅依赖 `schema`,不反向依赖具体 Agent 实现。`workflow` 与 `orchestrate` 同属 L1 但分属不同组件,互不 import;图校验可以各自实现,执行状态与结果契约必须分离。

技术实现(调度器、优先级队列、资源限流、补偿流程、typed workflow 批次合并)见 [orchestration-design](orchestration-design.md)。

# 设计:orchestration

对应领域行为见 [orchestration.md](orchestration.md)。

## 组件与职责

| 文件/关键类型 | 设计角色 |
|---------------|----------|
| `orchestrate/dag.go`(DAG 执行器) | 就绪集调度、并发控制、错误策略分派、收尾 |
| `orchestrate/orchestrate.go`(`Node`/`DAGConfig`/`ErrorStrategy`/`DAGOption`) | 图模型与函数式配置项 |
| `orchestrate/priority.go` | 优先级队列调度,可自动按关键路径计算优先级 |
| `orchestrate/backpressure.go` | 自适应并发度 |
| `orchestrate/resource.go` | 按资源标签的并发上限与速率限制 |
| `orchestrate/compensate.go`(`Compensatable`/`IdempotentChecker`) | 补偿(Saga)与幂等守护 |
| `orchestrate/conditional.go`/`loop.go`/`spawn.go` | 条件、循环、动态派生节点 |
| `orchestrate/aggregator.go` | 结果聚合 |
| `orchestrate/checkpoint.go`(`CheckpointStore`/`InMemoryCheckpointStore`) | DAG 级存/取与回放 |
| `checkpoint` 包(`IterationStore`) | ReAct 迭代级快照,文件系统后端 |
| `interrupt` 包(`Store`/`Record`/`MapStore`/`FileStore`) | 工具批执行前的挂起状态机;`FileStore` 用 `<id>.lock` 文件提供跨进程 `AcquireLease` 互斥 |

## 关键设计决策

- **函数式配置(DAGOption)**:并发度、错误策略、优先级调度、背压、资源限额、补偿、事件处理器全部经 `With*` 选项组合,默认 FIFO 调度、无背压、无补偿。
- **调度模型**:默认按就绪先入先出;开启优先级调度后用优先级队列,可自动按关键路径赋优先级以缩短总时长。
- **资源治理**:节点用资源标签声明其占用,执行器按标签施加并发上限与速率限制,实现跨节点的资源公平与保护。
- **收尾单点(锁契约)**:错误与取消路径收敛到同一收尾逻辑,确保加锁成对释放、事件一致通知 —— 这是近期"锁契约硬化 / 收敛错误与取消收尾路径"的核心改动,防止提前返回导致的持锁或重复解锁。
- **双轨检查点**:迭代级(`checkpoint`)服务单个长时 Agent 的续跑;DAG 级(`orchestrate`)服务整图回放。两者刻意分离,仅目录布局巧合相似,不共享读路径。
- **中断是独立状态机,不是 checkpoint 的标志位**:`interrupt.Store` 与 `checkpoint.IterationStore` 是两个持久接口,各自的不变式互斥——checkpoint 假设"这是一轮已完成的完整快照",interrupt 假设"这是执行前的挂起状态、带未完成的待决集合与租约"。把后者塞进前者会打破 checkpoint 的完整性假设,也让 `Resume(sessionID)` 无法区分调用方到底想恢复哪一种。选择新增独立接口而非扩展字段,代价是多一套持久契约与状态机,换来两者可分别配置保留期、后端与恢复语义,互不牵连。
- **租约优先于永久锁**:`interrupt.Store.AcquireLease` 用「持有者 + 到期时间」而非独占标志位表达恢复执行者身份,允许恢复进程崩溃后被其他调用方接管;代价是不提供端到端 exactly-once——租约到期后被接管的普通工具可能重放,有外部副作用的工具仍需调用方自带幂等键或补偿。`FileStore` 用 `O_CREATE\|O_EXCL` 创建的 `<id>.lock` 文件做跨进程互斥的临界区门,只在一次读-改-写期间持有,租约本身的身份与到期时间记在 Record 内,不依赖锁文件的存在与否。

## 状态机

```mermaid
stateDiagram-v2
    [*] --> 等待依赖
    等待依赖 --> 就绪: 依赖全部完成
    就绪 --> 运行: 调度器取出(FIFO/优先级)
    运行 --> 完成: 成功
    运行 --> 失败: 出错/超时且重试耗尽
    运行 --> 取消: 外部取消
    失败 --> 中止: Abort
    失败 --> 跳过: Skip 且 Optional
    失败 --> 补偿: Compensate
    完成 --> [*]
    中止 --> [*]
    跳过 --> [*]
    补偿 --> [*]
    取消 --> [*]
```

## 非功能考量

- **并发安全**:DAG 事件处理器所有方法必须并发安全;共享状态的锁必须成对收尾(见领域规则 OR-4/OR-5)。
- **可恢复性**:回放模式从检查点重建而不执行,用于崩溃恢复与调试。
- **背压**:在高负载下自适应收缩并发,避免下游(模型/工具)过载。

## 中断状态机

```mermaid
stateDiagram-v2
    [*] --> Pending: interrupt 原子持久化
    Pending --> Pending: 提交部分决定
    Pending --> Ready: 全部待决调用已有决定
    Ready --> Resuming: Resume 获取带期限租约
    Resuming --> Completed: 到达正常终态或下一持久 interrupt
    Resuming --> Ready: 执行失败或租约到期
    Completed --> [*]
```

## 架构决策提示

"迭代级与 DAG 级检查点双轨分离""补偿幂等契约""锁收尾单点""interrupt 独立状态机而非扩展 checkpoint"均为架构级决策,建议后续补记为 ADR(见 `architecture/adr/`)。

# 设计:memory

对应领域行为见 [memory.md](memory.md)。

## 组件与职责

| 文件/关键类型 | 设计角色 |
|---------------|----------|
| `memory/working.go`(`WorkingMemory`) | 请求级临时记忆 |
| `memory/session.go`(`SessionMemory`) | 会话级多轮记忆 |
| `memory/longterm.go`(`LongTermMemory`) | 跨会话(store)记忆 tier;是否 durable 由注入的 Store 决定 |
| `memory/mapstore.go`(`MapStore`) | 进程内 Store 后端;不实现任何能力接口,`Require*` 按缺失拒绝 |
| `memory/store_capability.go`(`DurableStore`/`AtomicStore`/`Require*`) | 后端能力契约与构造期 fail-fast 校验 |
| `memory/manager.go`(`Manager`, `WithDurableStore`/`WithAtomicStore`) | 三层记忆统一编排;声明式能力装配入口 |
| `memory/promoter.go`/`selector.go` | 层间提升策略与可组合谓词(`PromoteWhen`/`ArchiveWhen` 等) |
| `memory/archiver.go` | 归档策略 |
| `memory/compressor*.go`(`ContextCompressor` 及各实现) | 滑动窗口 / 重要度排序 / 摘要+截断 / token 预算 / 压缩链 |
| `memory/compactor.go` | 会话压紧 |
| `memory/token_estimate.go` | token 估算(压缩决策依据) |
| `context/source.go`(`Builder`/`Source`) | 装配管线契约 |
| `context/sources_*.go` | 各内置 Source(系统提示、会话记忆、向量召回等) |

## 关键设计决策

- **Builder/Source 显式化**:把提示装配从"散落在 Agent 里的拼接逻辑"提升为一等、可插拔、可审计的管线。新增上下文来源 = 新增一个 Source,不改 Agent。
- **压缩器职责单一 + 可链式组合**:每个压缩器只处理一个维度(窗口、重要度、摘要、预算),用压缩链组合,避免单个巨型压缩器。
- **`vctx` 命名**:包名避开标准库 `context`,导入路径仍为 `github.com/vogo/vage/context`。
- **token 估算集中**:压缩决策统一依赖 `token_estimate`,避免各处各估一套。
- **长期记忆 ≠ 持久化**:`LongTermMemory` 是 store tier 的名字,只承诺跨会话;是否跨进程重启存活由注入的 `Store` 后端决定。默认 `MapStore` 后端进程内即丢,故 `NewInMemoryLongTermMemory()` 的 godoc 明示非 durable;需要 durable 的装配方用 `NewLongTermMemory(store)` + `Require*` 或 `Manager.WithDurableStore` 显式声明。
- **能力契约与构造期 fail-fast**:`DurableStore`(`Durability()` 等级化自报)与 `AtomicStore`(`CompareAndSwap`)是可选接口;`RequireDurableStore`/`RequireAtomicStore` 缺能力时返回点名能力、后端类型与出路的可行动错误。`MapStore` 不实现新接口也不自我声明——缺失即被拒绝。等级校验要求自报 ≥ `DurabilityRestart`,防止"实现了接口却只自报进程内"的绕行。
- **selector 元数据来源 = 结构化 Value(duck-typed)**:`Entry` 是底层记录重建的只读投影、`Memory.Set` 无 metadata 通道,故不扩 `Entry`。谓词从 `e.Value` 读取 `Importance() float64` / `Tags() []string` 可选接口;未实现的值在选择性谓词下**不匹配**(被过滤)。谁配置选择性 Promoter/Archiver,谁就让对应值携带元数据。
- **组合谓词的恒等律与装配期失败**:`And()` 空参恒真、`Or()` 空参恒假(保持组合恒等律);`PromoteWhen(nil)`/`ArchiveWhen(nil)` 及 `And`/`Or` 的 nil 成员在构造期 panic,把运行期 nil deref 提前到装配期。
- **ForSession 视图,而非 ScopedStore**:`Manager.ForSession(agentID, sessionID)` 返回轻量视图,共享原 Manager 的后端、promoter、archiver、compressor 以及 session 层互斥锁,只把 session 数据访问 rebound 到该二元组。原 Manager 不被修改;对视图再 `ForSession` 是替换身份,不叠加前缀。`Store` 接口保持原样,不引入 adapter。
- **逻辑键与物理键分离**:Memory API 继续使用逻辑键(如 `msg:000001`)。底层键为 `mem:<tier>:<agent>:<session>:<logical-key>`,其中 agent/session 段是 ID UTF-8 字节的无填充 base64url,避免分隔符伪造与前缀碰撞。session / working 必须带两个身份段;长期 store tier 身份段为空,因而仍跨 session,但不会与 session 数据或无关键落在同一前缀。List / BatchGet 对调用方只返回逻辑键。
- **作用域清理**:session/store 层的 `Clear` 只 List 当前物理前缀再逐项 Delete,禁止 `store.Clear()`,以免清空共享后端或其他 scope。List 失败则不删除;Delete 失败向调用方返回,已成功的删除不回滚。
- **空 SessionID 与 checkpoint 命名空间**:读侧(`SessionMemorySource`)与写侧(TaskAgent 提升)在空 SessionID 时跳过 session 记忆,不降级到裸 `msg:` 命名空间。`Resume` 仍用查询参数定位 checkpoint,但加载成功后以记录的 `Checkpoint.SessionID` 为恢复后 memory scope / 事件 / 响应的权威值;与查询参数冲突时 checkpoint 赢并 `slog.Warn`。
- **同 session 并发 Run 的 offset 覆盖(已知限制)**:新消息的 `msg:%06d` 以当前视图读到的历史条数为偏移。同一 `(agentID, sessionID)` 下两个并发 Run 可能读到相同长度并写入同一逻辑键,后者覆盖前者。第一阶段不把 RunID 纳入隔离键,也不在此修复该竞态。
- **有意的 keyspace 断裂**:升级后旧版本写入的裸 `msg:` 键不会被新读路径发现,也没有存量迁移或读侧 fallback。需要保留历史的部署须在升级前自行按新 scope 重写。决策记录见 [0002-session-memory-namespace](../../../architecture/adr/0002-session-memory-namespace.md)。

## 与上下文编辑的分工

历史膨胀由两条互补路径治理,分属不同领域:

| 机制 | 所在领域 | 作用对象 | 时机 |
|------|----------|----------|------|
| 记忆压缩 | memory | 会话历史(事实层) | 装配前,决定"保留哪些历史" |
| 上下文编辑 | model(largemodel) | 出站请求里的旧工具结果 | 请求到达模型前,折叠为占位符 |

两者不重叠:压缩管"记多少",编辑管"每轮少付多少 token"。

## 非功能考量

- **性能**:token 估算与压缩在装配路径上,须保持轻量;召回类 Source 应可降级(后端不可用时跳过)。
- **可审计**:装配结果可追溯到各 Source 的贡献,便于排查"为什么模型看到了/没看到某段上下文"。

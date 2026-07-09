# 设计:memory

对应领域行为见 [memory.md](memory.md)。

## 组件与职责

| 文件/关键类型 | 设计角色 |
|---------------|----------|
| `memory/working.go`(`WorkingMemory`) | 请求级临时记忆 |
| `memory/session.go`(`SessionMemory`) | 对话级多轮记忆 |
| `memory/persistent.go`(`PersistentMemory`)/`mapstore.go`(`MapStore`) | 持久记忆与内存实现 |
| `memory/manager.go`(`Manager`) | 三层记忆的统一编排入口 |
| `memory/promoter.go`(`Promoter`) | 层间提升策略 |
| `memory/compressor*.go`(`ContextCompressor` 及各实现) | 滑动窗口 / 重要度排序 / 摘要+截断 / token 预算 / 压缩链 |
| `memory/compactor.go`/`archiver.go` | 会话压紧与归档 |
| `memory/token_estimate.go` | token 估算(压缩决策依据) |
| `context/source.go`(`Builder`/`Source`) | 装配管线契约 |
| `context/sources_*.go` | 各内置 Source(系统提示、会话记忆、向量召回等) |

## 关键设计决策

- **Builder/Source 显式化**:把提示装配从"散落在 Agent 里的拼接逻辑"提升为一等、可插拔、可审计的管线。新增上下文来源 = 新增一个 Source,不改 Agent。
- **压缩器职责单一 + 可链式组合**:每个压缩器只处理一个维度(窗口、重要度、摘要、预算),用压缩链组合,避免单个巨型压缩器。
- **`vctx` 命名**:包名避开标准库 `context`,导入路径仍为 `github.com/vogo/vage/context`。
- **token 估算集中**:压缩决策统一依赖 `token_estimate`,避免各处各估一套。

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

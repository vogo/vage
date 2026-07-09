# 设计:model

对应领域行为见 [model.md](model.md)。

## 中间件清单

| 文件 | 中间件 | 作用 |
|------|--------|------|
| `largemodel/retry.go` | 重试 | 失败退避重试 |
| `largemodel/timeout.go` | 超时 | 单次调用时限 |
| `largemodel/circuitbreaker.go` | 熔断 | 失败率超阈快速失败,半开试探恢复 |
| `largemodel/ratelimit.go` | 限流 | 调用速率控制 |
| `largemodel/cache.go` | 缓存 | 相同请求复用响应 |
| `largemodel/log.go`/`debug.go`/`metrics.go` | 可观测 | 日志、调试、指标 |
| `largemodel/budget_middleware.go` | 预算 | token 消耗核算,配合 Agent 预算终止 |
| `largemodel/overflow.go` | 溢出 | 上下文超限处置 |
| `largemodel/context_editor.go` | 上下文编辑 | 折叠旧工具结果、外置超大结果 |
| `largemodel/context_editor_compat.go` | V1 兼容层 | 隔离旧版占位符行为 |
| `largemodel/middleware.go`/`model.go` | 链装配 | 中间件组合入口 |

## 关键设计决策

- **装饰器链而非配置开关**:每个可靠性/治理关注点是一个独立中间件,使用方按需组合、自定排序。语义由组合顺序显式表达,而非隐藏在标志位里。
- **上下文编辑:收敛策略单一判定点**:折叠哪些工具结果的判定收敛到单一入口,V1 旧行为被隔离到兼容层(`context_editor_compat.go`)。这是近期"收敛策略优先级为单一判定点,隔离 V1 兼容层"的核心 —— 避免多处判定漂移。
- **浅拷贝编辑**:编辑作用于 `ChatRequest` 的浅拷贝,绝不篡改调用方原始请求。
- **资源感知折叠**:stale_resource 判定通过 `ResourceLookupFunc` 查询工具资源语义(每个被检查的工具调用查一次,须廉价,在热路径上),识别被后续写操作作废的旧读结果。
- **工件外置**:超过单条字节上限的工具结果经 `ArtifactWriter` 按 (sessionID, name) 外置,提示里留短引用;写入须对跨会话并发安全。

## 折叠原因(占位符语义)

| 原因 | 含义 |
|------|------|
| keep_last_k | 仅保留最近 k 条工具结果,更早的折叠 |
| stale_resource | 某次读结果被后续写操作作废,折叠该旧读 |

占位符默认把原因内联呈现,便于人读提示时立即判断折叠是由"就近保留"还是"后写作废"驱动。

## 非功能考量

- **热路径廉价**:资源查询、session ID 提取等编辑辅助函数在每请求路径上,须保持低开销。
- **可退化**:无 session、无工件写入器时,编辑回退为内联形式而非报错。

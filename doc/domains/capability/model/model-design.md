# 设计:model

对应领域行为见 [model.md](model.md)。**多端点路由**详见 [router-design.md](router-design.md)。

## 协议调用层

| 文件 | 职责 |
|------|------|
| `largemodel/call.go` | `Caller` 接口与 `Request`/`Response`/`Chunk` 信封;中间件只看见这一层 |
| `largemodel/openai_chat.go` | OpenAI Chat Completions:`OpenAIChatBackend` 接口 + wire 类型双向转换,默认后端是 `openai` native 客户端 |
| `largemodel/anthropic_messages.go` | Anthropic Messages:`AnthropicMessagesBackend` 接口 + wire 转换;吸收 system 提升、content block、必填 max_tokens 三处结构差异 |
| `largemodel/router/` | 协议中立路由核:策略、健康、重试、failover |
| `largemodel/composes/openais/`、`composes/anthropics/` | 各协议的路由 Backend 绑定 |
| `largemodel/compose.go` | 池化 Caller:把 router 池接到 Backend 接口上,以池集合承载并发,并合并各池的端点健康视图 |
| `largemodel/endpoint_config.go` | 统一端点配置与公开 API(`OpenAIConfig`、`WithRetryPolicy` 等) |
| `largemodel/stream.go` | `Stream` 生命周期(close 一次、终态 usage 捕获)与 `StreamAccumulator` 增量合并 |
| `largemodel/errors.go` | `APIError` 归一化与 `IsRetryable` 错误判读,供溢出处理与上层决策使用 |
| `largemodel/fake.go` | `FakeCaller` 脚本化测试替身,跨包共用 |

## 中间件清单

| 文件 | 中间件 | 作用 |
|------|--------|------|
| `largemodel/timeout.go` | 超时 | 单次调用时限 |
| `largemodel/ratelimit.go` | 限流 | 调用速率控制 |
| `largemodel/cache.go` | 缓存 | 相同请求复用响应 |
| `largemodel/log.go`/`debug.go`/`metrics.go` | 可观测 | 日志、调试、指标 |
| `largemodel/budget_middleware.go` | 预算 | token 消耗核算,配合 Agent 预算终止 |
| `largemodel/overflow.go` | 溢出 | 上下文超限处置 |
| `largemodel/context_editor.go` | 上下文编辑 | 折叠旧工具结果、外置超大结果 |
| `largemodel/context_editor_compat.go` | V1 兼容层 | 隔离旧版占位符行为 |
| `largemodel/middleware.go`/`model.go` | 链装配 | 中间件组合入口 |

## 关键设计决策

- **协议直连而非中立抽象**:每种厂商协议一个 `Caller` 实现,发出的是该 provider 的 native 请求。代价是调用层感知协议差异,收益是不必为了统一而做有损归一,厂商新能力也不必先等一层中立抽象补齐。
- **后端接口化,重试与路由整块外包给 router**:`Caller` 持有的不是具体客户端而是最小方法集接口,而 `largemodel/composes` 路由池实现的正是同一组方法。于是重试、端点存活判定、多端点选择与故障转移整块取自 `largemodel/router`,vage 删除了自己的 Retry / CircuitBreaker 中间件 —— 两套机制并存只会把尝试次数相乘。单端点走"一个端点的池",与多端点同形,可靠性行为不因端点数量而换一套说法。
- **已知代价:400 被当作可重试**。router 只把 401/403 判为不可重试,其余(含确定性的 400/404/422)一律重试满再判死端点。一个格式错误的请求因此要付掉整轮退避,并让端点进入恢复窗口。这是"取单一来源"的代价,vage 侧可通过可注入 failure classifier 改进(待做)。`IsRetryable` 保留了 vage 更窄的判读供上层使用。
- **池集合而非单池**:router 池一次只服务一个调用(并发调用被拒为 `ErrCallInProgress`),而 vage 把一个 `Caller` 注入给并行运行的 Agent。Caller 因此按需建池、用完归还(one pool per concurrent worker)。代价是健康状态按池分散;`EndpointStats()` 在读取时按别名合并出整体视图。流式调用在流建立后立即归还池 —— 路由只覆盖建流那一次。
- **信封隔离厂商类型**:`Request`/`Response`/`Chunk` 是 vage 自己的调用信封,厂商 wire 类型只出现在 provider 实现内部,因此中间件对协议无感、可跨协议复用。
- **prompt caching 下沉到 provider**:调用层只在请求上表达"要缓存"的意图,cache_control 断点由 Anthropic provider 渲染;OpenAI 自动缓存相同前缀,该意图无 wire 效果。
- **装饰器链而非配置开关**:每个治理关注点是一个独立中间件,使用方按需组合、自定排序。语义由组合顺序显式表达,而非隐藏在标志位里。
- **上下文编辑:收敛策略单一判定点**:折叠哪些工具结果的判定收敛到单一入口,V1 旧行为被隔离到兼容层(`context_editor_compat.go`)。这是近期"收敛策略优先级为单一判定点,隔离 V1 兼容层"的核心 —— 避免多处判定漂移。
- **浅拷贝编辑**:编辑作用于 `Request` 的浅拷贝(`Request.Clone`),绝不篡改调用方原始请求。
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

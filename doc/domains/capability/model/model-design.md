# 设计:model

对应领域行为见 [model.md](model.md)。**多端点路由**详见 [router-design.md](router-design.md)。

## 协议调用层

| 文件 | 职责 |
|------|------|
| `largemodel/call.go` | `Caller` 接口与 `Request`/`Response`/`Chunk` 信封;中间件只看见这一层 |
| `largemodel/openai_chat.go` | OpenAI Chat Completions:`OpenAIChatBackend` 接口 + wire 类型双向转换,默认后端是 `openai` native 客户端 |
| `largemodel/anthropic_messages.go` | Anthropic Messages:`AnthropicMessagesBackend` 接口 + wire 转换;吸收 system 提升、content block、必填 max_tokens 三处结构差异 |
| `largemodel/router/` | 协议中立路由核:策略、健康、重试、failover |
| `largemodel/provider/openais/`、`provider/anthropics/` | 各 provider 的路由 Backend 绑定与 message codec |
| `largemodel/compose_options.go` | 池化 Caller 的并发、重试、恢复时间及 provider client options |
| `largemodel/compose_pool.go` | provider-neutral 池集合与端点健康视图合并 |
| `largemodel/openai_compose.go`、`anthropic_compose.go` | 把对应 provider router 池接到根包 Backend 接口与 `Caller` facade |
| `largemodel/endpoint_config.go` | 统一端点配置与公开 API(`OpenAIConfig`、`WithRetryPolicy` 等) |
| `largemodel/stream.go` | `Stream` 生命周期(close 一次、终态 usage 捕获)与 `StreamAccumulator` 增量合并 |
| `largemodel/errors.go` | `APIError` 归一化与 `IsRetryable` 错误判读,供溢出处理与上层决策使用 |
| `largemodel/fake.go` | `FakeCaller` 脚本化测试替身,跨包共用 |
| `largemodel/response_schema.go` | `DegradeResponseSchemaPrompt`:无原生结构化输出映射的 codec 的通用降级路径 |

## 多模态消息编码(image/file)

`schema.MessagePartImage`/`MessagePartFile` 只在用户消息里有效(见 [agent-core AC-15](../../agent/agent-core/agent-core.md))。`EncodeOpenAIMessage`/`EncodeAnthropicMessage` 按 canonical part 顺序把它们渲染为各自的原生 wire 形态;下表是固定映射,不按模型名或运行时探测切换:

| canonical 来源 | OpenAI Chat Completions | Anthropic Messages |
|---|---|---|
| image URL | `image_url.url` | `image` block,`source.type=url` |
| image Data + MimeType | `image_url.url` data URI(`data:<mime>;base64,<data>`) | `image` block,`source.type=base64`,携带 `media_type` |
| file Data + MimeType + Filename | `file.file_data` data URI,携带 `filename` | `document` block,`source.type=base64`;`Filename` 无对应 wire 字段,被丢弃(唯一允许的已知降级 —— 字节与 MIME 类型仍完整送达) |
| file FileID | `file.file_id` | 编码前明确报错,不支持 |
| file URL | 编码前明确报错,不支持 | `document` block,`source.type=url` |

- **辅助字段只跟随有 wire 字段的来源**:MimeType 与 Filename 只出现在内联 Data 行;URL / FileID 行没有可承载它们的 wire 字段,所以 canonical 层就不允许携带(见 [agent-core AC-15](../../agent/agent-core/agent-core.md)),codec 无需、也不会做丢弃决定。
- **只在需要时切换 wire 形态**:用户消息含任一媒体 part 时才编码为结构化 content 数组;纯文本消息继续走标量 `content` 字符串,避免无关请求发生变化,也不影响缓存键。
- **OpenAI 内联文件强制要求 Filename**:`file.file_data` 无 filename 时 OpenAI 会拒绝识别文件类型,codec 因此在编码期(而非等 backend 4xx)报错。
- **失败前置到编码期**:结构错误(canonical 校验)与不可表示的组合(上表两处"编码前明确报错")都在任何网络 I/O 之前返回,`Call`/`CallStream` 共享同一 `buildRequest`,两条路径得到相同结果。
- **vision capability 复用现有检测**:`openais.chatRequiresVision`/`anthropics.messagesRequireVision` 读的是已编码的 wire content(`image_url`/`image` block),对 schema 层无感知,因此本变更不需要新增判定逻辑。文件不新增 capability 标签——模型是否接受某类文档、多大、哪些 MIME,由 provider 在调用时报错,vage 不做本地猜测或限流。
- **vage 不做的事**:不下载 URL、不读取本地路径、不上传文件、不管理 FileID 生命周期,也不提供统一文件存储服务——URL、字节、外部 ID 均由调用方提供并对其正确性负责。

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
- **后端接口化,重试与路由整块外包给 router**:`Caller` 持有的不是具体客户端而是最小方法集接口,而 `largemodel/provider/{openais,anthropics}` 路由池实现的正是同一组方法。于是重试、端点存活判定、多端点选择与故障转移整块取自 `largemodel/router`,vage 删除了自己的 Retry / CircuitBreaker 中间件 —— 两套机制并存只会把尝试次数相乘。单端点走"一个端点的池",与多端点同形,可靠性行为不因端点数量而换一套说法。
- **Caller facade 与 provider backend 分层**:`largemodel/provider/*` 只拥有 native wire codec、endpoint construction 与 routed backend,不反向依赖根包。使用 `Request`/`Response`/`Stream` 实现公开 `Caller` 的 adapter 留在 `largemodel`,避免形成 `largemodel → provider → largemodel` import cycle。为保持 provider 代码聚合,OpenAI 与 Anthropic 的池化 adapter 分别放在同 package 的独立文件中。
- **已知代价:400 被当作可重试**。router 只把 401/403 判为不可重试,其余(含确定性的 400/404/422)一律重试满再判死端点。一个格式错误的请求因此要付掉整轮退避,并让端点进入恢复窗口。这是"取单一来源"的代价,vage 侧可通过可注入 failure classifier 改进(待做)。`IsRetryable` 保留了 vage 更窄的判读供上层使用。
- **池集合而非单池**:router 池一次只服务一个调用(并发调用被拒为 `ErrCallInProgress`),而 vage 把一个 `Caller` 注入给并行运行的 Agent。Caller 因此按需建池、用完归还(one pool per concurrent worker)。代价是健康状态按池分散;`EndpointStats()` 在读取时按别名合并出整体视图。流式调用在流建立后立即归还池 —— 路由只覆盖建流那一次。
- **信封隔离厂商类型**:`Request`/`Response`/`Chunk` 是 vage 自己的调用信封,厂商 wire 类型只出现在 provider 实现内部,因此中间件对协议无感、可跨协议复用。
- **prompt caching 下沉到 provider**:调用层只在请求上表达"要缓存"的意图,cache_control 断点由 Anthropic provider 渲染;OpenAI 自动缓存相同前缀,该意图无 wire 效果。
- **装饰器链而非配置开关**:每个治理关注点是一个独立中间件,使用方按需组合、自定排序。语义由组合顺序显式表达,而非隐藏在标志位里。
- **上下文编辑:收敛策略单一判定点**:折叠哪些工具结果的判定收敛到单一入口,V1 旧行为被隔离到兼容层(`context_editor_compat.go`)。这是近期"收敛策略优先级为单一判定点,隔离 V1 兼容层"的核心 —— 避免多处判定漂移。
- **浅拷贝编辑**:编辑作用于 `Request` 的浅拷贝(`Request.Clone`),绝不篡改调用方原始请求。
- **资源感知折叠**:stale_resource 判定通过 `ResourceLookupFunc` 查询工具资源语义(每个被检查的工具调用查一次,须廉价,在热路径上),识别被后续写操作作废的旧读结果。
- **工件外置**:超过单条字节上限的工具结果经 `ArtifactWriter` 按 (sessionID, name) 外置,提示里留短引用;写入须对跨会话并发安全。
- **ResponseSchema:按 codec 静态选择、只选一种表达**:`Request.ResponseSchema` 是 `any`,与 `schema.ToolDef.Parameters` 同形,公共层不引入厂商专属包装类型,也不改写或裁剪 schema。是否走原生映射由 provider codec 静态决定(不按模型名猜测,不发探测请求):
  - **OpenAI Chat**(`openai_chat.go`):`response_format = {"type":"json_schema","json_schema":{"name":"vage_response_schema","schema":<原样>,"strict":true}}`,固定名称保证同一 schema 产生同一 wire 形状。
  - **Anthropic Messages**(`anthropic_messages.go`):`output_config.format = {"type":"json_schema","schema":<原样>}`,复用/保留 `OutputConfig` 上已有的其他配置(如 `Effort`),不使用已废弃的顶层 `output_format`。
  - **无原生映射的 codec**:调用 `DegradeResponseSchemaPrompt(proto, req)`,在请求副本的消息列表里插入一条确定性 framework system 指令(要求裸 JSON、内嵌 schema),插入点在已有的连续前导 system 消息之后,不改变它们的相对顺序,也不修改调用方原始 `Request`/`Messages`;返回的副本上 `ResponseSchema` 被清空,因为约束已完全表达为消息。schema 编不出 JSON 时在网络调用前返回错误。
  - 该字段只约束最终助手文本,与 `Tools` 互不干扰,可同时设置;两种表达都会被缓存键(`cache.go` 的 `cacheKeyData.ResponseSchema`)纳入,防止不同输出约束命中同一响应。
  - 当前无第三个已接入的 provider codec,`DegradeResponseSchemaPrompt` 的契约由测试内合成的不支持 codec 场景固定(`response_schema_test.go`),留给未来新增 codec 直接复用。

## 折叠原因(占位符语义)

| 原因 | 含义 |
|------|------|
| keep_last_k | 仅保留最近 k 条工具结果,更早的折叠 |
| stale_resource | 某次读结果被后续写操作作废,折叠该旧读 |

占位符默认把原因内联呈现,便于人读提示时立即判断折叠是由"就近保留"还是"后写作废"驱动。

## 非功能考量

- **热路径廉价**:资源查询、session ID 提取等编辑辅助函数在每请求路径上,须保持低开销。
- **可退化**:无 session、无工件写入器时,编辑回退为内联形式而非报错。

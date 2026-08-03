# 领域:model(模型接入与中间件)

| 元数据 | 值 |
|--------|----|
| 业务组 | capability |
| 一句话 | 保真于各厂商 native 协议地调用大模型:路由与重试交给 `aimodel/composes`,治理与上下文编辑由中间件链承担 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | agent-core(`schema`)、tooling(上下文编辑感知工具资源) |
| 对外 API | 是(Go 库 API) |
| 覆盖包 | `largemodel` |

## 概述

本领域回答:**如何可靠、可控、可观测地调用大模型,同时保真于各厂商自己的协议。**

`largemodel` 定义 `Caller` —— 按协议分派的模型调用接缝。每种协议有各自的实现,后端是 `aimodel` 对应 provider 的 native 客户端,并负责 vage 的 `Request`/`Response` 信封与该厂商 wire 类型之间的双向转换。中间件只看见信封,永远看不到厂商类型。

后端以接口形态注入(`OpenAIChatBackend` / `AnthropicMessagesBackend`),其方法集恰好是 native 客户端与 `aimodel/composes` 池共有的那一组。**所有 Caller 都经由 composes 池请求模型** —— 单端点是"一个端点的池",多端点是"多个端点的池",两者只差池怎么建。重试、端点存活判定与故障转移因此统一归 `composes`,vage 不再持有第二套。

在 `Caller` 之上是一条**装饰器中间件链**:日志、缓存、限流、超时、指标、token 预算、上下文编辑、溢出处理。每个中间件包裹下一个,组成可插拔的治理栈,并透传被包裹 Caller 的协议。重试与熔断不在其中。

**边界(不做):** 不实现模型推理(委托底层 `aimodel` 的 provider 客户端);不自研重试、熔断、路由与健康判定 —— 全部取 `aimodel/composes` 的实现;不做跨协议的路由或故障转移(OpenAI 池与 Anthropic 池各自独立,请求模型不共享);不做提示词内容治理(那是 `prompt`/`skill`)。

## 核心实体(概念层)

- **Protocol(协议)**:模型绑定的厂商 wire 协议,配置期确定。取值为 openai-chat / openai-responses / anthropic-messages。
- **Caller(调用接缝)**:一次模型调用的协议无关接口;每种协议一个实现,拥有该厂商的请求构造、响应解析、流解码与错误归一化。
- **Backend(后端接口)**:Caller 调用的最小方法集,由 `aimodel/composes` 池实现(也可由使用方注入裸 native 客户端以绕过路由);是所有调用路径的共同接缝。
- **compose Caller(池化调用接缝)**:vage 唯一的 Caller 实现形态,持有一个或多个端点。端点选择策略(failover/random/weighted/cost/latency)、调用内指数重试、端点存活判定与恢复窗口全部来自 `aimodel/composes`;每个端点声明自己的模型名,发出请求时覆盖信封中的模型。
- **池集合(pool set)**:compose Caller 持有的多个 `aimodel` 池。一个池一次只服务一个调用,而 vage 的 Caller 被并行 Agent 共享,故按需借还;并发上限即池数上限,超出时等待而非失败。
- **中间件**:`Wrap(next Caller) Caller` 的装饰器,可任意组合与排序。
- **治理中间件**:超时、限流、缓存 —— 约束对上游模型的调用(重试与熔断不在此处,归 `composes`)。
- **可观测中间件**:日志、指标、debug。
- **预算中间件(budget)**:在调用前后核算 token 消耗,配合 Agent 的预算终止。
- **溢出处理(overflow)**:上下文超限时的处置。
- **上下文编辑器(ContextEditor)**:请求到达模型前,把较早的工具结果折叠为短占位符,并可将超大工具结果外置到工件存储。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| MOD-1 | **唯一接入点**:模型调用只经 `largemodel.Caller`;厂商 wire 细节只允许出现在对应的 provider 实现里,不得散落到中间件或上层业务包。 |
| MOD-7 | **协议随调用全程一致**:中间件必须透传被包裹 Caller 的 Protocol;请求中的消息若与 Caller 协议不符,在发出前失败(`ErrProtocolMismatch`),不做隐式转换。 |
| MOD-8 | **usage 归一化单点**:各厂商用量字段在 provider 边界归一化为 `schema.Usage`;流式用量只在厂商给出最终统计时记账一次,缺失时保持零增量,不用文本长度估算冒充。 |
| MOD-2 | **中间件顺序有意义**:链的组合顺序决定语义(如缓存应在超时之内,限流应在日志之内),由使用方显式组装。 |
| MOD-3 | **上下文编辑作用于浅拷贝**:编辑只改出站请求的浅拷贝,不篡改调用方持有的原始请求。 |
| MOD-4 | **收敛策略单一判定点**:哪些工具结果被折叠由单一收敛判定决定(如"保留最后 k 个"、"被后续写操作作废的旧读结果"),避免多处各判一套导致的漂移。 |
| MOD-5 | **占位符自解释**:被折叠的工具结果留下说明"为何被折叠"(recency / stale_resource 等)的占位符,便于人读提示时理解。 |
| MOD-6 | **超大结果外置**:单条工具结果超过字节上限时外置到工件存储,提示里只留短引用;外置失败则回退为内联提示。 |
| MOD-9 | **重试与存活判定单一来源**:归 `composes`,vage 不提供 Retry / CircuitBreaker 中间件,以免尝试次数相乘。代价须知悉:`composes` 只把 401/403 视为不可重试,确定性的 400 也会被重试满并使端点进入恢复窗口。`largemodel.IsRetryable` 保留 vage 自己更窄的错误判读,供上层决策使用,但不驱动任何重试循环。 |
| MOD-10 | **池不共享**:一个 `composes` 池归属一个会话、一次只服务一个调用;并发由多个池承担,每个池独立学习端点健康。跨池的健康视图只在读取时按别名合并,不回写。 |

## 状态与转换

本领域唯一的显式状态机是**端点存活**,由 `composes` 拥有:可用 → 死亡(调用内重试耗尽,或凭证被拒即刻判死)→ 恢复窗口到期后重新成为候选。恢复不抢占当前 active 端点。单端点池同样走这个状态机,端点死亡后直到恢复窗口到期,调用以 `ErrNoActiveModels` 快速失败 —— 这正是熔断的作用。

上下文编辑无长期状态,每次请求独立判定。

## 与其他领域的交互

- **agent-core**:任务型 Agent 通过组装好的 Caller 链发起每轮 LLM 调用,并接入上下文编辑中间件与预算中间件;Agent 的 Protocol 决定其消息以哪种 wire 形态构造与存储。
- **tooling**:上下文编辑的 stale_resource 判定需查询工具的资源语义(ResourceTracker),识别"被后续写作废的旧读结果"。
- **memory**:与记忆压缩互补 —— 编辑管"每轮少付 token",压缩管"记多少历史"(见 [memory-design](../../memory/memory/memory-design.md))。

技术实现(中间件清单、上下文编辑 V1/V2 兼容层)见 [model-design](model-design.md)。

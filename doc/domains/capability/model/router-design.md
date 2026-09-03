# 设计:多端点路由 (router)

对应领域行为见 [model.md](model.md);Caller 与中间件装配见 [model-design.md](model-design.md)。

## 分层

多端点 dispatch 分两层:**路由机制**与**协议语义**分离,池不跨协议混用。

```
largemodel/provider/openais     ChatCompletions · ChatCompletionsStream · message codec · internal Responses route
largemodel/provider/anthropics  Messages · MessagesStream · message codec
        │  opaque labels + per-endpoint closure              endpoint index
        ▼                                                              ▲
largemodel/router              capability filter → active endpoint → retries → failover
                               health · aliases · observers · EndpointStat · MultiError
        ▲
largemodel/compose_pool.go     池集合:并行 Agent 借还多个 router 池,合并 EndpointStats
largemodel/*_compose.go        provider 池 → Backend 接口 → Caller facade
largemodel/endpoint_config.go  公开 API: OpenAIConfig / WithRetryPolicy / …
```

| 层 | 包 | 职责 |
|---|---|---|
| 路由核 | `largemodel/router` | 策略、健康三态、调用内重试、failover;不见 request/response 类型 |
| provider 绑定 | `largemodel/provider/openais`、`provider/anthropics` | wire 类型复制、canonical message codec、model 覆盖、capability 谓词 |
| 并发池 | `largemodel/compose_pool.go` | 一 router 池同时只服务一次调用;Caller 按需建池、读时合并健康 |
| Caller adapter | `largemodel/openai_compose.go`、`anthropic_compose.go` | 把 provider router 池适配到根包 Backend 接口与公开 `Caller` facade |
| 构造入口 | `largemodel/compose_caller.go` | `NewCaller` / `BuildCaller` / `WrapCaller` 与 `ComposeCaller` 契约 |

底层 HTTP 仍由 `github.com/vogo/aimodel/openai`、`anthropic` 发出;aimodel **不含** retry 与路由。

## 公开 API

应用代码 import `largemodel` 构建 Caller 与配置;观测路由健康或自定义 routed backend 时 import `largemodel/router`:

```go
import (
    "github.com/vogo/vage/largemodel"
    "github.com/vogo/vage/largemodel/router"
)
caller, err := largemodel.BuildCaller(largemodel.OpenAIConfig{
    Strategy: largemodel.StrategyFailover,
    Endpoints: []largemodel.OpenAIEndpoint{
        {Alias: "primary", APIKey: k1, BaseURL: u1, Model: "gpt-4o"},
        {Alias: "backup",  APIKey: k2, BaseURL: u2, Model: "gpt-4o-mini"},
    },
},
    largemodel.WithRetryPolicy(time.Second, 2),
    largemodel.WithRecoverTime(5*time.Minute),
    largemodel.WithConcurrency(8),
)
stats := caller.EndpointStats() // []router.EndpointStat
```

单端点 = `Endpoints` 长度为 1 的同一条路径。

### 三个构造入口

| 入口 | 输入 | 返回 | 协议如何选定 |
|---|---|---|---|
| `NewCaller(ep, opts...)` | 单 endpoint | `ComposeCaller` | 类型参数,编译期 |
| `BuildCaller(cfg, opts...)` | 多 endpoint / 自定 `Strategy` | `ComposeCaller` | 类型参数,编译期 |
| `WrapCaller(backend)` | 自建 client | `Caller` | backend 方法集,运行时 |

**协议由参数类型选定,不由入口名选定**:输入类型本身已经决定了协议(`OpenAIEndpoint` 只可能是 OpenAI Chat),入口不再要求调用方把同一个信息声明第二遍。`OpenAIEndpoint` / `OpenAIConfig` 是 struct,可进入类型 union,故 `NewCaller` / `BuildCaller` 把这个选择做成编译期约束:传入不受支持的类型无法通过编译,`switch` 的 `default` 分支不可达。**协议不做运行时推断** —— 不从 BaseURL、不从 API key 前缀猜。

`NewCaller` 是 `BuildCaller` 的便捷入口:把一个 endpoint 包成单元素配置再委托同一条构造路径,不构成第二套实现 —— 默认值、协议推导与选项演进仍只有一个事实来源(与 taskagent `Quick` 同样的薄包装取舍)。它唯一新增的行为是 **Alias 留空时填 `DefaultEndpointAlias`**:alias 是健康快照与路由错误里的运维身份,provider 层强制要求,但只有一个端点时它没有需要被区分的对象,所以命名是调用方的选项而非义务。有第二个端点或要声明非默认 `Strategy` 时回到 `BuildCaller` —— 便捷入口刻意不承接这些。`vv` 的 `configs.NewLLMClient` 即 `NewCaller` 这条路径。

`WrapCaller` 是唯一的运行时分派:`OpenAIChatBackend` / `AnthropicMessagesBackend` 是**带方法的接口**,Go 不允许它们出现在类型 union(`cannot use A in union (A contains methods)`),没有能表达这一选择的约束。代价被限制在可判定的范围内 —— 两个方法集不相交,所以分派是确定的,只有边界情形需要报错:`nil` → `ErrNoBackend`,两个协议都不实现 → `ErrUnsupportedBackend`,两个都实现 → `ErrAmbiguousBackend`(此时 wire 形态是调用方的决策,静默挑一个比报错更糟)。被包的 backend 原样使用:不路由、不重试、不做健康跟踪,所以返回 `Caller` 而非 `ComposeCaller`。

**`ComposeCaller` 是泛型入口的统一返回类型**:`Caller` 加 `EndpointStats() []router.EndpointStat`。泛型函数只能有一个返回类型,而 endpoint 健康是两个具体 caller 中值得保留在这个宽度上的能力。具体类型 `*OpenAIChatComposeCaller` / `*AnthropicMessagesComposeCaller` 仍导出,需要 `Stats()` 或更多时由调用方类型断言。

**根包 re-export(Caller 契约):** `Strategy`、`EndpointCost`、`StrategyFailover` / `StrategyWeight` / …、`ErrNoActiveEndpoints`、`DefaultEndpointAlias`、`ComposeCaller`。

**router 包(观测与扩展):** `EndpointStat`、`AttemptResult`、`StatusAvailable` / `StatusDead` / `StatusProbation`、`WithAttemptObserver` 回调参数类型等 —— 由 `largemodel/router` 直接 import,根包不再再导出以免与 router 演进漂移。

## Active endpoint 与 dispatch 链

一个 router **池**在任一时刻由一个 **active endpoint** 服务;策略只在「尚无 active」或「当前 active 判死」时运行,成功复用时不重抽。

每次 dispatch:

1. **Caller 层严格能力筛选(可选)** — `RequireNativeCapabilities` 按「单个候选满足全部要求」收窄 alias,写入 `router.Call.Eligible`;未开启严格策略时 Eligible 为空,不改变选路。这与下一步的 label filter 是两层:前者看统一 `Capabilities` 声明,后者看 provider 既有 tools/vision 谓词。
2. **Capability filter** — 按 opaque label 排除不声明能力的端点;全不满足则 `CapabilityError`(无网络 I/O)
3. **复用 active** — 健康且 capable 则继续用
4. **冻结策略序** — 否则按策略排一次序,沿序 failover
5. **调用内重试** — 对当前端点指数退避,耗尽则判 dead 并换下一个

流式:仅**建流**受路由保护;流建立后 release 池 slot,中途错误不 retry、不改 health。

## 策略

| Strategy | 选人时机的行为 |
|---|---|
| `failover` | 声明顺序第一个可用 |
| `random` | 均匀随机(非 per-request 负载均衡) |
| `weighted` | 按 Weight 比例(≤0 当 1) |
| `cost` | 静态 `EndpointCost` 最低 |
| `latency` | 注入 `Latency` 最低 |

策略决定「谁成为 active」,不是「每个请求抽谁」;连续成功调用落在同一 backend。

## 选路可观测

router 在每次选定或复用 active endpoint 时同步通知只读 `RouteObserver`。`ComposeCaller` 默认安装观察器,把协议无关的 `router.RouteSelection` 转成 `schema.EventRouteSelected`,经 `schema.DispatchEvent` 投递。同步 `Run` 走 ctx 上的 hook dispatcher;流式 `RunStream` 优先走 stream Emitter(避免与 hook 双投),因此 `route_selected` 会出现在用户可见的事件流里、紧随该轮 `iteration_start` 之后。载荷只有公开运维身份:

| 字段 | 含义 |
|---|---|
| `alias` | 端点别名;调用方不得把凭证写进 alias |
| `strategy` | 池策略(failover / random / …) |
| `reason` | `initial` / `reuse` / `failover` / `probation` |
| `stream` | 是否流式建流 |

不含 endpoint 配置、标签、Base URL、API key、请求体或原始错误。观察器失败不得改变路由结果、重试或 failover;多池并发调用各自独立发事件。

自定义观察器用 `largemodel.WithRouteObserver` / `router.WithRouteObserver`;默认事件观察器仍会安装,两者并存。

## 宿主绑定 Caller,而不是 per-request 换池

租户识别与凭证读取属于 service 的宿主/集成层,不属于 router。宿主在构造 TaskAgent(或在 Run 入口挑选已构造的 Agent)时,用 `BuildCaller` 把同一协议的多 endpoint 收成一个可共享的 `ComposeCaller`,再 `taskagent.WithCaller`。ReAct 循环内不换 Caller;`RunRequest` 不携带 endpoint 或路由策略。从 `NewCaller` 迁移时,把原单 endpoint 作为 `OpenAIConfig`/`AnthropicConfig` 的第一项,再追加同协议备用项。

## 健康三态

| 状态 | 含义 |
|---|---|
| `available` | 最近有成功 |
| `dead` | 重试耗尽或 401/403;recover 窗口内不可选 |
| `probation` | recover 窗口已过、尚未确认;可选但**只尝试一次**,不走重试策略 |

失败分类(router):仅 **401/403** 立即判死;其余(含 429、5xx、400)走重试。`largemodel.IsRetryable` 是 vage 更窄的判读,供上层决策,不驱动 router。

取消上下文:不算失败,不 poison health。

## 并发模型

- 一个 **router 池** = 一个会话语义、**一次调用**;并发第二次 → `ErrCallInProgress`
- 一个 **Caller** 被并行 Agent 共享 → `composePool` 维护最多 N 个池(`WithConcurrency`,默认 8),借还而非 queue 失败
- **EndpointStats** 读时按 alias 合并:available > probation > dead

## 错误

| 错误 | 何时 |
|---|---|
| `ErrNoActiveEndpoints` | 无可选端点 |
| `*MultiError` | 各端点都试过,每端点一条 `EndpointError`;支持 `errors.As` 穿透到 vendor 错误 |
| `*CapabilityError` | 能力 filter 无解 |
| `ErrCallInProgress` | 单池并发(通常被 composePool 吸收) |

## 边界

- **无跨协议 failover** — OpenAI 池与 Anthropic 池独立
- **中间件链不含 retry** — 避免与 router 重试相乘
- **Responses API** — `largemodel/provider/openais` 保留包内 Responses 路由(无公开入口);vage 公开 `Caller` 当前仅接 Chat / Messages

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

底层 HTTP 仍由 `github.com/vogo/aimodel/openai`、`anthropic` 发出;aimodel **不含** retry 与路由。

## 公开 API

应用代码 import `largemodel` 构建 Caller 与配置;观测路由健康或自定义 routed backend 时 import `largemodel/router`:

```go
import (
    "github.com/vogo/vage/largemodel"
    "github.com/vogo/vage/largemodel/router"
)
caller, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
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

单端点 = `Endpoints` 长度为 1 的同一构造函数。`vv` 的 `configs.NewLLMClient` 即此路径。

**根包 re-export(Caller 契约):** `Strategy`、`EndpointCost`、`StrategyFailover` / `StrategyWeight` / …、`ErrNoActiveEndpoints`。

**router 包(观测与扩展):** `EndpointStat`、`AttemptResult`、`StatusAvailable` / `StatusDead` / `StatusProbation`、`WithAttemptObserver` 回调参数类型等 —— 由 `largemodel/router` 直接 import,根包不再再导出以免与 router 演进漂移。

## Active endpoint 与 dispatch 链

一个 router **池**在任一时刻由一个 **active endpoint** 服务;策略只在「尚无 active」或「当前 active 判死」时运行,成功复用时不重抽。

每次 dispatch:

1. **Capability filter** — 按 opaque label 排除不声明能力的端点;全不满足则 `CapabilityError`(无网络 I/O)
2. **复用 active** — 健康且 capable 则继续用
3. **冻结策略序** — 否则按策略排一次序,沿序 failover
4. **调用内重试** — 对当前端点指数退避,耗尽则判 dead 并换下一个

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

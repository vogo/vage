# 业务组:platform(安全护栏与运行平台)

本组承载"保障"与"运行":让 Agent 安全,并让它能作为服务跑起来、被观测、被评测。

## 领域清单

| 领域 | 职责 | 覆盖包 |
|------|------|--------|
| [guard](guard/guard.md) | 输入/输出/工具结果三处安全护栏、跨边界凭证脱敏 | `guard`、`security` |
| [service](service/service.md) | HTTP 服务(sync/streaming/async)、hook 事件总线、评测器、向量召回 | `service`、`hook`、`eval`、`vector` |

## 共享上下文

- 护栏动作只有三态:Pass / Rewrite / Block(章程安全基线)。
- 可观测的统一载体是 `schema.Event`,由 hook 分发。
- HTTP 服务对外必须同时支持 sync、streaming、async 三种执行语义(章程运维原则)。

# 领域:service(运行时、事件、评测与召回)

| 元数据 | 值 |
|--------|----|
| 业务组 | platform |
| 一句话 | 把 Agent 作为 HTTP 服务运行,并提供事件总线、评测器与向量召回 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | agent-core、memory、model(评测与召回) |
| 对外 API | 是(HTTP REST + Go 库 API) |
| 覆盖包 | `service`、`hook`、`eval`、`vector` |

## 概述

本领域让组装好的 Agent "跑起来、被观测、被度量、能召回"。

- `service`:HTTP 服务,以 REST 端点暴露 Agent 执行,支持同步、流式、异步三种语义。
- `hook`:事件总线 —— 把 Agent 生命周期事件分发给同步/异步订阅者。
- `eval`:评测器 —— 对 Agent 输出按多种标准打分(精确匹配、包含、LLM 裁判、工具调用、延迟、成本),可组合加权。
- `vector`:可插拔的向量召回面 —— 定义最小接口,自带内存实现,外部后端(qdrant)与嵌入器(OpenAI)独立实现。

**边界(不做):** `service` 不做账户/计费/多租户;`vector` 不内置向量数据库,只定义接口;`eval` 不做训练,只做离线/在线评测打分。

## 核心实体(概念层)

- **Service / Task**:HTTP 服务与异步任务;TaskStore 保存异步任务状态。
- **执行三语义**:sync(阻塞返回)、streaming(增量事件流)、async(提交任务 + 轮询/回调)。
- **Hook / AsyncHook**:事件订阅者;Manager 负责注册与分发。
- **Evaluator(评测器)**:对一个评测用例产出评分;家族含 ExactMatch、Contains、LLMJudge、ToolCall、Latency、Cost,及 Composite/Weighted 组合。
- **EvalCase / EvalResult / EvalReport**:评测的输入用例、单项结果、汇总报告(支持批量)。
- **VectorStore / Embedder**:向量存取与嵌入;MapVectorStore 为内存实现;archivehook/vectorhook 把会话内容归档进向量库。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| SVC-1 | **三语义齐全**:对外执行接口必须同时提供 sync、streaming、async(章程运维原则)。 |
| SVC-2 | **事件不丢主流程**:hook 分发失败不得打断 Agent 主流程;异步 hook 与主流程解耦。 |
| SVC-3 | **评测标准显式**:每个评测器有明确、可复现的判定标准;组合评测按权重汇总。 |
| SVC-4 | **向量接口最小化**:`vector` 只暴露最小接口,使多种后端可无扭曲实现;后端缺省时召回类功能可降级。 |
| SVC-5 | **异步任务可查询**:异步执行返回可查询的任务标识,状态经 TaskStore 保存。 |

## 状态与转换

异步任务状态机:提交 → 运行 → (完成 | 失败),状态由 TaskStore 持有、经 HTTP 查询。流式执行随 `schema.RunStream` 推进,以 EOF 结束。

## 领域事件

hook 消费 `schema.Event`(Agent 全生命周期事件),向订阅者分发;可用于日志、指标、归档(vectorhook 把会话写入向量库)。

## 与其他领域的交互

- **agent-core**:service 调用 Agent 执行;hook 消费其生命周期事件。
- **model**:LLMJudge 评测器与嵌入器调用模型能力。
- **memory**:向量召回为上下文装配的 Source 提供检索(见 [memory](../../memory/memory/memory.md))。

技术实现(端点、任务存储、评测器与向量后端)见 [service-design](service-design.md)。

> **集成测试**:跨领域行为的集成矩阵位于仓库 `integrations/`(agent/context/eval/guard/largemodel/mcp/memory/metrics/orchestrate/service/skill/tool/vector 各一套),是本领域及全框架的端到端验证入口。

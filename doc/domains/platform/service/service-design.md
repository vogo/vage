# 设计:service

对应领域行为见 [service.md](service.md)。

## 组件与职责

| 包/关键类型 | 设计角色 |
|-------------|----------|
| `service`(`Service`/`Config`/`handler`) | HTTP 服务与路由 |
| `service`(`Task`/`TaskStore`) | 异步任务与状态存储 |
| `hook`(`Manager`/`Hook`/`AsyncHook`/`HookFunc`) | 事件订阅与分发 |
| `eval`(`Evaluator` 及各实现) | 评测器家族 |
| `eval`(`CompositeEvaluator`/`WeightedEvaluator`) | 评测组合与加权 |
| `eval`(`EvalCase`/`EvalResult`/`EvalReport`/`batch`) | 评测数据模型与批量 |
| `vector`(`VectorStore`/`Embedder`/`MapVectorStore`) | 向量召回最小接口 + 内存实现 |
| `vector/qdrant` | qdrant REST 后端(v1.x,thin HTTP 客户端) |
| `vector/openai` | OpenAI 嵌入器(独立 thin HTTP 客户端) |
| `vector/archivehook`/`session/tree/vectorhook` | 把会话内容归档进向量库 |

## 关键设计决策

- **执行三语义一等**:sync/streaming/async 在服务层平级支持,async 经 TaskStore 记录状态、可查询,满足章程"三语义齐全"。
- **hook 主流程解耦**:同步 hook 与异步 hook 分离;异步 hook 不阻塞 Agent 主流程,分发失败不上抛打断运行。
- **评测器可组合**:每个评测器单一标准,Composite/Weighted 把多标准汇总为综合评分,支持批量报告。
- **向量接口刻意最小**:只定义存取与嵌入的最小面,让 qdrant/pgvector/chroma/pinecone 等无扭曲实现;内存 MapVectorStore 覆盖测试与本地实验。
- **后端为 thin HTTP 客户端**:qdrant、openai 后端刻意是 net/http + JSON 的薄封装,不引重依赖,便于替换。

## 非功能考量

- **可观测**:hook 是全框架可观测的分发中枢;指标经 hook 输出。
- **可降级**:向量后端或嵌入 API 不可用时(如 key 未配置),召回类功能跳过而非报错。
- **可运维**:HTTP 服务可独立部署为代理运行时;异步任务状态可查询。

## 集成测试布局

`integrations/` 下按领域分套(agent/context/eval/guard/largemodel/mcp/memory/metrics/orchestrate/service/skill/tool/vector),承载跨包端到端矩阵。它们只 mock 模型层,真实运行其余构件,是验证本文所述契约的权威入口。

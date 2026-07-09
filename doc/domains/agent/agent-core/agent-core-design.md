# 设计:agent-core

对应领域行为见 [agent-core.md](agent-core.md)。本文件记录技术实现层面的设计决策与契约,不重述代码可还原的细节。

## 组件与职责

| 包 | 关键类型 | 设计角色 |
|----|----------|----------|
| `schema` | `RunRequest`/`RunResponse`/`RunOptions`/`Message`/`ToolDef`/`ToolResult`/`ContentPart`/`Event`/`RunStream`/`StopReason` | 供应商中立契约,唯一外部依赖 `aimodel`;提供与底层类型的双向转换辅助 |
| `agent` | `Agent`/`StreamAgent` 接口、`Base`/`Config`、`RunFunc`、`CustomAgent`、`StreamMiddleware`、`RunText`/`RunStreamText`/`RunToStream` | 统一接口 + 非流式↔流式适配胶水 |
| `agent/taskagent` | `New` + 一整套 `With*` 选项、`Run`/`RunStream`/`Resume` | ReAct 循环实现,集成中枢 |
| `agent/routeragent` | `Route`/`RouteFunc`、内置 `FirstFunc`/`IndexFunc`/`KeywordFunc`/`RandomFunc`/`LLMFunc` | 分发策略 |
| `agent/workflowagent` | `New`/`NewDAG`/`NewDAGWithEdges`/`NewLoop` | 顺序/图/循环编排,委托 `orchestrate` |
| `prompt` | `PromptTemplate`、`StringPrompt`、`NewPromptTemplate` | 基于 `text/template` 的提示词渲染 |

## 关键设计决策

- **依赖注入而非硬编码**:TaskAgent 的模型、工具、记忆、护栏、技能、检查点、hook、上下文构建器全部通过 `With*` 选项以接口注入。默认值缺省时行为退化(如无迭代存储则不可 Resume),而非报错崩溃。
- **通过 context.Context 传递会话身份与 Emitter**:深层工具处理器无需显式参数即可读取 SessionID、向流写事件。这使工具签名保持稳定。
- **流通道语义**:`RunStream` 为拉取式;成功结束返回 `io.EOF`,生产者错误在缓冲事件排空后浮现,关闭后再读返回专用错误。
- **构造期校验**:WorkflowAgent 的 DAG 构造在建图时即校验环、缺依赖、重复 ID;RouterAgent 构造期要求候选 Agent 非 nil。把错误尽量前移到构造期而非运行期。

## LLM 路由输出契约

`LLMFunc` 要求模型仅回一个索引数字;解析失败 / 越界 / 调用失败时按 fallback 索引兜底(fallback <0 则报错)。这是把不可靠的自然语言输出收敛为确定分支的关键约束。

## 非功能考量

- **性能**:消息累积为追加,避免重排;工具批可并行(受最大并行度选项约束)。
- **可恢复性**:Resume 复用与 Run 相同的循环与 finalize 路径,保证续跑与首跑行为一致。
- **可观测**:全生命周期事件不可被业务逻辑省略(章程运维原则)。

## 依赖与降级

- 上游依赖 `aimodel`(模型 SDK 抽象),核心不绑定任何厂商。
- 下游各能力缺省即降级:无检查点则不写快照、无技能管理器则不注入技能提示,均不影响主答案产出。

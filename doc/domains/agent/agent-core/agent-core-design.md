# 设计:agent-core

对应领域行为见 [agent-core.md](agent-core.md)。本文件记录技术实现层面的设计决策与契约,不重述代码可还原的细节。

## 组件与职责

| 包 | 关键类型 | 设计角色 |
|----|----------|----------|
| `schema` | `Protocol`/`RunRequest`/`RunResponse`/`RunOptions`/`Message`/`MessagePart`/`Usage`/`ToolCall`/`ToolDef`/`ToolResult`/`ContentPart`/`Event`/`RunStream`/`StopReason` | provider-neutral 契约层;`Message` 以私有 canonical 状态和访问器统一读写,不解释 provider wire |
| `largemodel/provider/openais`、`provider/anthropics` | provider 路由绑定与 message codec | 按 provider 聚合 native wire ↔ canonical message 转换、原生回放、协议结构校验与 Backend 路由绑定 |
| `agent` | `Agent`/`StreamAgent` 接口、`Base`/`Config`、`RunFunc`、`CustomAgent`、`StreamMiddleware`、`Middleware`/`MiddlewareFunc`/`ChainMiddleware`、`RunText`/`RunStreamText`/`RunToStream` | 统一接口 + 非流式↔流式适配胶水 + Agent Run 中间件契约 |
| `agent/taskagent` | `New` + 一整套 `With*` 选项、`Quick`、`Run`/`RunStream`/`Resume`、`WithMiddleware` | ReAct 循环实现,集成中枢;`New + Option` 是完整构造契约,`Quick` 是其高频参数薄包装 |
| `agent/routeragent` | `Route`/`RouteFunc`、内置 `FirstFunc`/`IndexFunc`/`KeywordFunc`/`RandomFunc`/`LLMFunc` | 分发策略 |
| `agent/workflowagent` | `New`/`NewDAG`/`NewDAGWithEdges`/`NewLoop` | 顺序/图/循环编排,委托 `orchestrate` |
| `prompt` | `PromptTemplate`、`StringPrompt`、`NewPromptTemplate` | 基于 `text/template` 的提示词渲染 |

## 关键设计决策

- **依赖注入而非硬编码**:TaskAgent 的模型、工具、记忆、护栏、技能、检查点、hook、上下文构建器全部通过 `With*` 选项以接口注入。默认值缺省时行为退化(如无迭代存储则不可 Resume),而非报错崩溃。
- **一次 Run 一个装饰器接缝,同步/流式同链**:`agent.Middleware` 以 `Wrap(next RunFunc) RunFunc` 装饰整次 ReAct 执行,TaskAgent 在 `Run` 与 `RunStream` 上各驱动同一条链恰好一次(第一个注册者最外层,`ChainMiddleware` 跳过 nil)。不引入 before/after/wrap_model/wrap_tool 等分阶段巨型接口,也不合并模型、事件与工具执行中间件。
- **草拟与终态分离**:链内只产生「草拟 `RunResponse`」,输出护栏、消息记忆写入与成功终态事件都在链之后对最终响应执行,因此短路或改写后的消息与 ReAct 产物受完全相同的约束,并成为 `AgentEnd.Message` 的唯一来源。`SessionID`/`Duration` 在终态阶段由框架回写,不能伪造;`nil, nil` 转 `ErrNilMiddlewareResponse`。
- **真实终态与报告的 stop reason 分离**:`runContext` 记录 ReAct 循环实际达成的 `stopReason` 与是否真实跑过(`reactRan`),与响应里可被中间件改写的 `StopReason` 分开。护栏对部分结果的宽容、预算耗尽事件都只看真实循环状态,避免伪造的 stop reason 触发假预算事件或放行护栏。
- **直返工具作为 Agent 控制流而非工具层策略**:`taskagent.WithReturnDirectTools(names...)` 按名称声明哪些工具的成功结果可直接作为最终答案。判定收敛在共享 `runReactLoop` 的工具批处理之后(`executeToolBatch` 额外回传护栏后的 `ToolResult` 切片),因此 `Run` / `RunStream` / `Resume` 复用同一语义,不另建同步专用循环。批内全部工具照常执行完毕,再按模型 `ToolCalls` 顺序选第一个「名称已配置且成功」的候选;失败绝不短路。`schema.ToolDef` / `ToolResult` / `RunResponse` / StopReason 均不扩展,`complete` 只是多了一条到达路径。选「名字配置」而非扩 schema,是因为直返决定的是某个 Agent 的结束策略——同一工具可在一个 Agent 直返、在另一个继续推理。
- **上下文先于链构建**:`RunStream` 提前构建上下文以保持构建错误同步浮现;`prepareContext` 在链之前完成,中间件改写 `req.Messages` 不回溯影响模型输入,需要改输入走输入护栏或 `vctx` 源。`Resume` 复用与 Run 相同的循环与终态路径但刻意不进链。
- **通过 context.Context 传递会话身份、Emitter 与 Run 值**:深层工具处理器无需显式参数即可读取 SessionID、向流写事件(内置事件直接用 Emitter,应用自定义事件走 `EmitCustomData`)、在一次运行内互传临时值。这使工具签名保持稳定。
- **流通道语义**:`RunStream` 为拉取式;成功结束返回 `io.EOF`,生产者错误在缓冲事件排空后浮现,关闭后再读返回专用错误。
- **构造期校验**:WorkflowAgent 的 DAG 构造在建图时即校验环、缺依赖、重复 ID;RouterAgent 构造期要求候选 Agent 非 nil。把错误尽量前移到构造期而非运行期。
- **便捷构造只做薄包装,不做第二套实现**:TaskAgent 的入门构造(身份 + 调用器 + 模型 + 系统提示词)被收进 `Quick`,但它只负责组装参数并委托 `New` —— 不复制默认值、不自行推导协议、不包装调用器、不吞掉额外选项、不新增校验或错误返回。这样默认值、协议保真与后续选项演进只有 `New` 一个事实来源,不会出现两条构造路径漂移。代价是 `Quick` 只覆盖高频入口:需要描述、具名/版本化提示词或其他 `Config` 字段时仍走 `New`。预置选项排在调用方选项之前,沿用本包「后应用者生效」的规则,因此额外能力可叠加、预置项可被显式覆盖。nil 调用器与空模型的失败时机与等价 `New` 调用完全一致(仍在首次运行时暴露)。
- **消息模型单事实源 + 原生回放缓存**:`schema.Message` 的私有 canonical 状态(`role` + `parts`)是唯一事实源;可选 `origin` 保存未经修改的 provider native wire。同协议且未修改时直接回放 `origin`;任何 `SetText`/`SetRole`/`ReplaceParts`/`AppendPart` mutation 都立即清空它,随后由 provider codec 从 canonical 状态重新编码。
- **provider codec 边界**:`schema` 不导入或解析 `aimodel` wire 类型。OpenAI/Anthropic 的 decode、encode、role 映射、content block 规则和 Anthropic tool-result 合并分别收敛在 `largemodel/provider/openais` 与 `provider/anthropics`。

## Run 作用域值(Run values)

`schema` 的 `WithRunValues` / `SetRunValue` / `GetRunValue` 让同一次运行内的多个工具通过字符串 key 交换临时状态,契约要点如下。

- **创建时机**:TaskAgent 只在 `Run`、`RunStream`、`Resume` 三个公开入口各绑定一次全新空存储,随 ctx 传至工具批注入点,因此一次运行的所有轮次、所有串行/并行工具共享同一张表。按批次或按迭代重建会在 ReAct 轮次之间丢值;挂到 `Agent` 字段或按 SessionID 复用则会跨运行泄漏 —— 两者都是被禁止的实现方式。
- **生命周期**:由上下文可达性自然约束,运行结束无需清理。`RunStream` 的表活到流生产结束/报错/取消为止;执行链不得把表写入响应、事件、检查点或其他长期对象。
- **缺省语义**:未绑定存储时 `SetRunValue` 无副作用并返回 `false`,`GetRunValue` 与 key 不存在一样返回 `nil, false` —— 选择可探测的 no-op 而非 panic/error,是为了让同一个工具在 TaskAgent 之外的执行器里也能复用;必须要有这项能力的调用方自行检查布尔返回并失败。
- **并发限度**:单次读写与覆盖写对并行工具是安全的;同一 key 的并发写不承诺确定的最终值,也不提供 compare-and-swap、事务、过期时间或工具调用顺序保证。存进去的可变对象仍由调用方自行同步。值不复制,类型断言与命名空间化的 key 由调用方负责。
- **运行边界即隔离边界**:新绑定的存储总是覆盖从父上下文继承的那一个,所以嵌套 Agent 启动自己的运行时不会与父运行串值。

与相邻概念的区别:**SessionID** 只用于事件关联、会话记忆与检查点寻址,既不是 Run 值的键,也不是使用前提 —— 无 SessionID 的运行照样能存取,相同 SessionID 的两次运行也不共享;**memory / workspace** 才是跨运行、跨进程的长期存储,Run 值不写检查点,`Resume` 从空表开始,断点续跑所需的状态必须显式经长期存储重建。

## 工具期自定义事件(custom event)

`schema.EmitCustomData(ctx, name, payload)` 让工具处理器经既有 Emitter 链发出应用自定义事件,契约要点如下。

- **为什么是一个受控入口而非放开 `EventData`**:`EventData` 用未导出方法密封,外部包无法实现,这是 schema 对「事件载荷集合」的控制点,不能为了自定义进度而废弃。折中方案是固定顶层类型 `custom` + 密封载荷 `CustomEventData{Name, Payload}`,把应用差异全部收进可扩展的 `Name`。代价是下游要做二级分派(先 `custom` 再 `Name`),名称与载荷的版本演进由应用自己负责。
- **发射流程**:从 ctx 取发射器,没有就直接返回;有就构造**恰好一次** `custom` 事件,`SessionID` 取自 ctx,时间戳沿用 `NewEvent`。当前 ctx 不携带 Agent 身份,因此不合成 `AgentID`(留空),需要归因的调用方把标识写进 Payload。
- **缺省与失败语义**:无发射器时是静默 no-op —— 与 Run 值同样的取舍,让工具在 TaskAgent 之外的执行器里可复用;发射器返回的错误被吞掉,不向 handler 传播、不改变工具结果。函数无返回值,调用方**无法据此确认投递成功**,需要可靠交付就换机制(AC-9)。
- **不校验什么**:空名称与不可序列化载荷都不拦截,仍会在进程内发出,失败沿用消费者在序列化边界上的既有行为。「非空、稳定、可命名空间化的名称」和「JSON 兼容、不含凭证的载荷」是**调用约定**,不是框架保证。
- **所有权**:Payload 原样保存、不复制。发射后继续并发修改被引用对象会与消费者产生竞态,同步由调用方负责。
- **顺序**:工具批在调用 handler 前于单一注入点绑定 SessionID 与 Emitter,所以 handler 内同步发出的事件进入同一条 `RunStream`,且落在该工具的 `tool_call_start` 与 `tool_call_end` 之间。单个工具内按调用顺序投递;**并行工具之间可以交错**,不新增跨工具的确定性排序承诺,需要关联就在 Payload 里自带关联字段。

## LLM 路由输出契约

`LLMFunc` 要求模型仅回一个索引数字;解析失败 / 越界 / 调用失败时按 fallback 索引兜底(fallback <0 则报错)。这是把不可靠的自然语言输出收敛为确定分支的关键约束。

## 非功能考量

- **性能**:消息累积为追加,避免重排;工具批可并行(受最大并行度选项约束)。
- **可恢复性**:Resume 复用与 Run 相同的循环与 finalize 路径,保证续跑与首跑行为一致。
- **可观测**:全生命周期事件不可被业务逻辑省略(章程运维原则)。

## 依赖与降级

- `largemodel/provider/openais` 与 `provider/anthropics` 分别依赖对应的 `aimodel` native wire 类型;`schema` 保持 provider-neutral。模型调用经 `largemodel.Caller` 按协议分派。
- 下游各能力缺省即降级:无检查点则不写快照、无技能管理器则不注入技能提示,均不影响主答案产出。

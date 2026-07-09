# 全局业务术语表

收录在**多个领域间通用**的术语。领域私有术语放在各自的 `<domain>.md` 中。

| 术语 | 定义 |
|------|------|
| **Agent(智能体)** | 满足统一 `Run(请求)→响应` 契约、带身份三元组(ID/名称/描述)的执行单元。有任务型、路由型、工作流型、自定义型四种形态。详见 [agent-core](domains/agent/agent-core/agent-core.md)。 |
| **ReAct 循环** | 任务型 Agent 的推理-行动循环:提示 → LLM → 若产生工具调用则执行并回喂 → 直至收敛。三种终止:得到最终答案、达到最大迭代、token 预算耗尽。 |
| **RunRequest / RunResponse** | 一次 Agent 调用的输入/输出信封。请求含消息列表、会话 ID 与单次覆盖项;响应含消息、用量、耗时与终止原因(StopReason)。 |
| **StopReason(终止原因)** | 标记一次 Agent 运行为何结束:complete(完成)/ max-iterations(达上限)/ budget-exhausted(预算耗尽)等。 |
| **Message(消息)** | 在底层 `aimodel.Message` 之上叠加 Agent 语义元数据(所属 Agent、时间戳、附加元数据)的对话消息。 |
| **供应商中立契约** | `schema` 包定义的一套不绑定任何大模型厂商的数据类型,是所有子系统的通用语言。 |
| **工具(Tool)** | 可被 Agent 调用的能力单元,带名称、描述与 JSON Schema 参数。来源分本地函数、MCP 远程、agent-as-tool 三类。 |
| **ToolDef / ToolResult** | 工具的可注册描述 / 工具执行的中立结果(支持文本、JSON、图片、文件多态,带错误标记)。 |
| **护栏(Guard)** | 对消息作检查并返回 Pass / Rewrite / Block 三态结果的安全构件,作用于输入、输出、工具结果三个位置。 |
| **记忆三层** | working(单次请求内的临时记忆)→ session(单次对话内)→ store(跨对话持久)。层级只能向上提升(promote),不可逆向。 |
| **上下文装配(Context Builder)** | 把"存在哪些事实"(会话、记忆、状态)组装成"发给 LLM 的消息序列"的可插拔管线,由若干 Source 组成。 |
| **上下文编辑(Context Editing)** | 在请求到达模型前,把较早的工具结果折叠为短占位符的中间件动作,使多轮 ReAct 不必每轮重付完整工具结果的 token。 |
| **会话(Session)** | 一等的对话实体:身份 + 追加型事件流 + 结构化状态 KV + 可插拔存储后端。事件只追加,结构化状态可覆盖。 |
| **检查点(Checkpoint)** | 某次迭代的完整可恢复快照,用于崩溃/重启后断点续跑。注意:`checkpoint` 包(ReAct 迭代级)与 `orchestrate` 的 DAG 级检查点是两套不同机制。 |
| **DAG 编排** | 以有向无环图组织多个 Runner(Agent 满足此接口),支持并行、条件、循环、补偿、背压、优先级调度。 |
| **补偿(Compensation / Saga)** | 编排失败时对已提交步骤执行的回滚动作,保证长流程的最终一致。 |
| **背压(Backpressure)** | 根据运行时负载自适应调节 DAG 并发度的机制。 |
| **技能(Skill)** | 兼容 [Agent Skills](https://agentskills.io) 开放标准的能力包,可向 Agent 注入提示与过滤工具。 |
| **MCP** | Model Context Protocol。vage 既做 client(消费外部工具)也做 server(暴露 Agent 能力)。 |
| **hook / 事件** | Agent 生命周期通过 `schema.Event` 发出的结构化可观测事件;hook 管理器负责分发。 |
| **Emitter** | 通过 `context.Context` 传递的流式事件发射器,让深层工具无需显式参数即可向流写事件。 |
| **子代理(Subagent)/ agent-as-tool** | 把一个 Agent 包装成工具供另一个 Agent 调用;分发时通过 `sessionview` 交给子代理一份父会话只读快照。 |

> 新增术语规则:一个术语若出现在 >1 个领域,就应登记于此,并在各领域文档中引用而非重复定义。

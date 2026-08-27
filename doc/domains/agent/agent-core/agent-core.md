# 领域:agent-core(智能体核心)

| 元数据 | 值 |
|--------|----|
| 业务组 | agent |
| 一句话 | Agent 统一抽象与四种形态、provider-neutral 数据契约、提示词模板 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | model、tooling、memory、guard、orchestration(仅 TaskAgent/WorkflowAgent 依赖) |
| 对外 API | 是(Go 库 API) |
| 覆盖包 | `agent`、`schema`、`prompt` |

## 概述

本领域回答两个根本问题:**"什么是一个 Agent"** 与 **"Agent 之间说什么语言"**。

- `schema` 是**数据契约层**:定义 Agent 的输入/输出/消息/事件/工具描述/流式通道。`Message` 以 provider-neutral 的 canonical 状态(`role` + `parts`)为唯一事实源,可选 `origin` 缓存未修改的厂商原生 wire 供同协议回放;读取统一走访问器(Role/Text/ToolCalls/…),因此上层业务代码无需按厂商分叉。它是全框架最底层、仅依赖标准库的包。
- `agent` 定义 Agent 统一接口与四种编排形态。
- `prompt` 提供系统提示词的模板化与版本化抽象。

**边界(不做):** 不实现大模型调用本身(委托 `model` 领域);不实现工具执行、记忆、护栏、检查点的具体逻辑(只持有它们的接口)。`schema` 不含任何行为策略,只是纯数据;`prompt` 只负责渲染出字符串,不做提示词治理或评测。

## 核心实体(概念层)

- **Agent**:满足 `Run(请求)→响应` 契约、带身份三元组(ID / 名称 / 描述)的执行单元。`StreamAgent` 是其增量流式扩展。
- **四种 Agent 形态**:
  - **任务型(TaskAgent)** —— 唯一"会思考会用工具"的叶子执行单元,实现 ReAct 循环。是本组乃至全框架的**集成中枢**。
  - **路由型(RouterAgent)** —— 分发器:从若干带描述的候选 Agent 中选一个并原样转发请求,自身不调用工具。
  - **工作流型(WorkflowAgent)** —— 编排器:以顺序 / DAG / 循环三种模式组合多个子 Agent,执行委托给 `orchestration` 领域。
  - **自定义型(CustomAgent)** —— 逃生舱:把用户提供的函数直接包装成 Agent。
- **RunRequest / RunResponse**:一次调用的输入/输出信封。
- **Message**:叠加了 Agent 语义元数据的对话消息。
- **ToolDef / ToolResult**:工具的可注册描述 / 中立的执行结果。`ToolResult.Text()` 是取其文本的推荐入口 —— 它只返回第一个文本 part(框架实际发给模型的那段),与拼接全部文本的 `Message.Text()` 刻意不同;需要完整或多模态内容时直接读 `Content`。
- **Run 值(Run values)**:一次运行内、进程内的临时键值表,供多个工具在同一次执行中传递中间状态。它不是会话记忆:生命周期是「这一次运行」,而非「这个会话」。
- **Event / RunStream**:全谱系可观测事件 / 拉取式流通道。
- **自定义事件(custom event)**:由调用方(通常是工具处理器)在执行途中发出的应用级事件。它挂在固定的 `custom` 事件类型下,靠一个应用自选的名称区分含义;框架不解释它,只负责投递。
- **PromptTemplate**:可渲染、具名、带版本的系统提示词。

> 实体的字段、类型与转换关系属于结构细节,以代码为准,不在此重述。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| AC-1 | **ReAct 循环恰好三种终止**:得到最终答案(complete)、达到最大迭代(默认 10)、token 预算耗尽。每种对应一个 StopReason。 |
| AC-2 | **预算双点检查**:每轮 LLM 调用前、每次工具批执行前各检查一次;预算 ≤0 表示无限。 |
| AC-3 | **消息单调累积**:循环中消息只追加,顺序稳定;工具批结果按调用顺序返回,与并发无关。 |
| AC-4 | **护栏三态**:输入/输出/工具结果护栏统一为 Pass / Rewrite / Block;输出护栏对非完成态的"部分结果"只告警不失败。 |
| AC-5 | **检查点尽力而为**:检查点保存失败只记 warn,绝不打断 ReAct 热路径。 |
| AC-6 | **路由不变式**:路由函数必须返回非 nil 的子 Agent,子响应必须非 nil;响应的会话 ID 始终被重写为请求的会话 ID。 |
| AC-7 | **协议保真**:`schema` 消息保存产出它的厂商 wire 原文,不做有损归一;跨协议回放须显式失败(`ErrProtocolMismatch`),不得隐式转换(章程红线)。 |
| AC-8 | **Run 值以「一次运行」为作用域**:一次运行内的所有工具(含跨轮次、含并行批)共享同一张临时值表;不同运行之间互不可见,即便同一 Agent、同一会话 ID。该表不持久化、不进检查点,续跑从空表开始。 |
| AC-9 | **自定义事件是尽力而为的可观测性**:发不出去(无流、流已关闭/取消、发射器拒绝)就丢弃,既不报错也不改变工具结果;因此它不得成为驱动核心业务状态转换的唯一依据。事件载荷的密封性不因此放松 —— 调用方能选的只有名称与载荷,不能新增顶层事件类型。 |

## 状态与转换

任务型 Agent 一次运行的生命周期(事件序):

```mermaid
stateDiagram-v2
    [*] --> AgentStart
    AgentStart --> Iteration
    Iteration --> LLMCall
    LLMCall --> ToolBatch: 有工具调用
    ToolBatch --> Checkpoint
    Checkpoint --> Iteration: 未终止
    LLMCall --> Terminal: 无工具调用/达上限/预算耗尽
    Terminal --> AgentEnd
    AgentEnd --> [*]
```

断点续跑(Resume):从迭代存储载入最新检查点,复用完全相同的循环骨架与终止路径;跳过输入护栏(原运行已校验),但输出与工具结果护栏仍生效。检查点已是终态则拒绝续跑。

## 领域事件

任务型 Agent 在整条 ReAct 路径上发出结构化事件:AgentStart/End、IterationStart、TextDelta(流式)、ToolCall/ToolResult、LLM 调用、Token 预算、护栏、技能、编排、上下文构建。消费者为 `platform` 组的 hook 与流式调用方。

除这些**内置事件**外,还有一类**自定义事件**:内置事件描述框架生命周期、语义由框架定义;自定义事件只表达调用方自己定义的运行期信息(典型场景是一次长耗时工具调用内部的分阶段进度),框架既不定义也不校验其含义。二者的关键差别对消费者是可见的 —— 内置事件看顶层类型即可分派,自定义事件必须**先看 `custom` 类型、再看载荷里的名称**才能解释,因为所有自定义事件共用同一个顶层类型。名称由应用自行命名与演进,框架不维护名称注册表,也不会把名称提升成新的事件类型。

自定义事件的用途边界:它服务于运行中的即时可观测性,不承担业务状态存储、跨进程投递与可靠消息职责(AC-9)。要留存的状态归 memory / workspace。

## 与其他领域的交互

- **model**:通过 `largemodel.Caller` 链发起 LLM 调用,并接入上下文编辑中间件。
- **tooling**:通过工具注册表注册与执行工具;技能向其注入提示与过滤工具。
- **memory**:多轮会话记忆的读写与 session 提升;上下文装配管线来自 `context`。跨运行、跨进程要留存的状态归 memory / workspace,Run 值只负责单次运行内的临时传递。
- **guard**:输入/输出/工具结果三处护栏。
- **orchestration**:工作流型 Agent 的 DAG/循环执行,任务型的断点续跑存储。

技术实现(选项、ReAct 具体流程、路由内置函数)见 [agent-core-design](agent-core-design.md)。

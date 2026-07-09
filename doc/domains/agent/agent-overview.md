# 业务组:agent(智能体与编排核心)

本组是框架的心脏:定义"什么是一个 Agent",以及 Agent 之间用什么协议对话、如何被编排成多步流程。

## 领域清单

| 领域 | 职责 | 覆盖包 |
|------|------|--------|
| [agent-core](agent-core/agent-core.md) | Agent 统一抽象与四种形态、供应商中立数据契约、提示词模板 | `agent`、`schema`、`prompt` |
| [orchestration](orchestration/orchestration.md) | DAG 编排引擎、断点续跑快照 | `orchestrate`、`checkpoint` |

## 共享上下文

- `schema` 是全框架的根契约层,本组与其余所有组共用它作为通用语言。
- 四种 Agent 形态里,**任务型(TaskAgent)是集成中枢**,直接依赖其余各组的能力;其余三型只做组合。
- 工作流型 Agent(WorkflowAgent)把执行委托给 `orchestration` 领域的 DAG 引擎。

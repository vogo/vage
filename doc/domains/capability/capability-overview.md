# 业务组:capability(模型与工具能力)

本组是 Agent 的"能力接入层":把外部大模型、外部工具、技能包接入框架,并在接入路径上叠加可靠性与治理。

## 领域清单

| 领域 | 职责 | 覆盖包 |
|------|------|--------|
| [model](model/model.md) | LLM 中间件装饰链、上下文编辑、预算/溢出治理 | `largemodel` |
| [tooling](tooling/tooling.md) | 工具系统、MCP client/server 协议、Agent Skills | `tool`、`mcp`、`skill` |

## 共享上下文

- 模型接入的唯一入口是 `aimodel.ChatCompleter`(章程红线:供应商中立)。`largemodel` 在其上叠加中间件。
- 工具有三个来源:本地函数、MCP 远程、agent-as-tool。所有工具经统一注册表暴露给 Agent。
- 技能(`skill`)兼容 Agent Skills 开放标准,向 Agent 注入提示并过滤可用工具。

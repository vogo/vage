# vage 文档

`vage` 是构建 LLM 智能代理系统的 Go 框架。

## 内容

本目录用于存放 vage 的设计与子系统协议文档,覆盖:

- 可组合代理:TaskAgent(ReAct)、RouterAgent、WorkflowAgent、CustomAgent
- DAG 编排:并行、循环、条件、补偿(Saga)、检查点、背压、优先级调度
- 三层记忆:working(请求)→ session(对话)→ store(持久),含上下文压缩与 token 预算
- 安全护栏:prompt injection、content filter、PII、topic、length 与自定义 guard
- LLM 中间件链:日志、熔断、限流、重试、超时、缓存、指标
- 工具系统:本地函数、MCP 远程工具、agent-as-tool、内建 bash 工具(进程隔离)
- Agent Skills
- MCP 协议:client(消费外部工具)与 server(暴露代理能力)
- 评测器:ExactMatch、Contains、LLMJudge、ToolCall、Latency、Cost
- HTTP 服务:sync / streaming / async
- 持久化协议:session、workspace、session-tree
- hook 与事件总线

框架能力总览见仓库根的 [../README.md](../README.md)。

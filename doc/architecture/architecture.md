# 架构总览

## 定位

vage 是**库优先**的框架:每个子系统是一个可独立引入、以接口解耦的 Go 包。它们通过供应商中立的 `schema` 契约层互相通信。使用方按需组装,或直接用 `service` 包把组装好的 Agent 作为 HTTP 运行时部署。

## 分层

```mermaid
graph TD
    subgraph L4[运行时平台]
        service[service HTTP服务]
        hook[hook 事件总线]
        eval[eval 评测]
    end
    subgraph L3[智能体形态]
        task[TaskAgent ReAct]
        router[RouterAgent 路由]
        workflow[WorkflowAgent 编排]
        custom[CustomAgent 自定义]
    end
    subgraph L2[能力与保障]
        model[largemodel 模型中间件]
        tool[tool 工具系统]
        mcp[mcp 协议]
        skill[skill 技能]
        guard[guard 护栏]
        security[security 脱敏]
    end
    subgraph L1[状态与编排]
        memory[memory 三层记忆]
        vctx[context 上下文装配]
        session[session 会话]
        workspace[workspace 工作区]
        orchestrate[orchestrate DAG]
        checkpoint[checkpoint 断点]
        vector[vector 召回]
    end
    subgraph L0[契约层]
        schema[schema 供应商中立契约]
        prompt[prompt 提示词]
    end

    L4 --> L3
    L3 --> L2
    L3 --> L1
    L2 --> L0
    L1 --> L0
```

## 依赖拓扑核心规则

1. **`schema` 是根契约包**:只依赖外部 `aimodel` 与标准库,零内部依赖。所有其他包依赖它,反向依赖被章程禁止。
2. **TaskAgent 是集成中枢**:四种 Agent 中,只有任务型直接依赖模型、工具、记忆、护栏、技能、检查点、hook、context。其余三型只依赖 `agent` + `schema`(工作流型另依赖 `orchestrate`)。它只**编排**这些能力,不实现它们 —— 全部以接口/管理器形式注入。
3. **能力以接口注入**:各子系统对 TaskAgent 暴露的都是接口(ToolRegistry、memory.Manager、Guard、IterationStore、ChatCompleter 链……),因此每一项都可被替换或 mock。

## 一次 TaskAgent 运行的数据流

```mermaid
sequenceDiagram
    participant Caller
    participant Task as TaskAgent
    participant Ctx as context.Builder
    participant Guard as 输入护栏
    participant LLM as largemodel链
    participant Tool as tool.Registry
    participant CP as checkpoint

    Caller->>Task: Run(RunRequest)
    Task->>Ctx: 装配提示(系统提示+会话记忆+extras+请求消息)
    Task->>Guard: 校验输入
    loop ReAct 迭代(≤maxIter, 受token预算)
        Task->>LLM: ChatCompletion(经中间件链+上下文编辑)
        alt 有工具调用
            Task->>Tool: 并行执行工具批
            Tool-->>Task: ToolResult(经工具结果护栏)
            Task->>CP: 写非终态快照(尽力而为)
        else 产出最终答案
            Task->>Task: StopReasonComplete
        end
    end
    Task->>Guard: 校验输出
    Task->>CP: 写终态快照(Final + StopReason)
    Task-->>Caller: RunResponse
```

## 横切关注点

| 关注点 | 承载机制 | 说明 |
|--------|----------|------|
| 可观测 | `schema.Event` + `hook.Manager` | 全生命周期结构化事件,通过 ctx 中的 Emitter 发射 |
| 流式 | `schema.RunStream` | 拉取式通道;非流式 Agent 可被适配为流式 |
| 断点续跑 | `checkpoint`(迭代级)/ `orchestrate`(DAG 级) | 两套独立机制,勿混淆 |
| Token 预算 | `RunOptions` + largemodel budget 中间件 | 每轮 LLM 调用前、每次工具批前双点检查 |
| 上下文膨胀 | `largemodel` 上下文编辑 + `memory` 压缩 | 折叠旧工具结果、按重要度/预算压缩历史 |
| 安全 | `guard` + `security` | 三态护栏 + 跨边界凭证脱敏 |
| 资源隔离 | `tool.ResourceTracker` + `sessionview` | 工具资源标签、子代理只读快照与预算 |

## 架构决策记录(ADR)

架构级、有长期影响或多种权衡的决策记录于 `architecture/adr/`(编号 `NNNN-title.md`)。ADR 需人工评审通过后方可写入,新建默认 `proposed` 状态。当前尚无 ADR;后续可将以下已体现在代码中的关键决策补记为 ADR:

- 以 `aimodel.ChatCompleter` 作为唯一模型接入点(供应商中立)。
- `schema` 作为零内部依赖的根契约包。
- `checkpoint` 与 `orchestrate` checkpoint 双轨分离。
- 上下文编辑采用"收敛策略单一判定点"折叠旧工具结果。

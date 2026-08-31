# ADR 编号与撰写约定

架构决策记录(Architecture Decision Record)记录**架构级、有长期影响或存在多种权衡**的决策。

## 编号与命名

- 文件名:`NNNN-kebab-title.md`,`NNNN` 为零填充递增序号(`0001`、`0002`……)。
- 一次决策一个文件。

## 生命周期规则

1. **需人工评审后方可写入**:不得直接落盘 ADR。先向相关方提交草案,获显式批准后再写文件。
2. **默认 `proposed`**:新建 ADR 状态为 `proposed`,经确认后才升为 `accepted`。`proposed` 应在一个迭代内收敛。
3. **永不删除**:被取代的 ADR 移入 `deprecated/`,并在头部注明指向替代 ADR 的链接。状态改为 `superseded`.

### 新增横切接缝(seam)时

ReAct 热路径上的拦截/装饰平面已记录在 [agent-core-design.md](../../domains/agent/agent-core/agent-core-design.md) 的决策表中。任何**新增**拦截或装饰平面 —— 包括 Run 级、每迭代级、尚未列入该表的热路径扩展点 —— 须遵守:

1. **先 ADR,后代码**:提案须先提交 ADR 草案,按上文「需人工评审后方可写入」获显式批准;README 或包注释中的说明**不能**替代 ADR 准入。新平面的运行时代码在 ADR 状态升为 `accepted` 之前不得合并。
2. **ADR 必答项**:现有平面为何不足、扩展已有平面为何不够、触发时机、可观测性/事件、与护栏及 interrupt/resume 的交互、向后兼容影响。
3. **本项不做自动 enforcement**:违规 seam 不由此文档或 CI 自动检测;维护者在评审时引用本节与决策表。

## 必备章节

每个 ADR 必须含:

- **Status** —— proposed / accepted / deprecated / superseded
- **Date** —— `YYYY-MM-DD`
- **Context** —— 背景与驱动力
- **Decision** —— 决定了什么
- **Rationale** —— 为何这样决定(权衡)
- **Consequences** —— 正负后果与影响面

## 候选待补 ADR

以下决策已体现在代码中,建议后续补记(见 [../architecture.md](../architecture.md)):

- 以 `largemodel.Caller` 作为唯一模型接入点,其下按协议直连各厂商 native 客户端(当前公开:Chat / Messages;`openai-responses` 预留未接)。
- `schema` 作为仅依赖标准库的根契约包(`Message` 为 provider-neutral canonical + 可选 `origin`)。
- `checkpoint`(迭代级)与 `orchestrate` checkpoint(DAG 级)双轨分离。
- 上下文编辑采用"收敛策略单一判定点 + V1 兼容层隔离"。
- DAG 执行器"锁契约收尾单点"(错误与取消收敛到同一收尾路径)。
- An independent `interrupt` state machine, rather than extending `checkpoint.IterationStore`, carries the cross-process human-in-the-loop suspend (`agent/taskagent` interrupt/resume). See [0001-interrupt-independent-state-machine.md](0001-interrupt-independent-state-machine.md).

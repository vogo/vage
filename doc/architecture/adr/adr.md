# ADR 编号与撰写约定

架构决策记录(Architecture Decision Record)记录**架构级、有长期影响或存在多种权衡**的决策。

## 编号与命名

- 文件名:`NNNN-kebab-title.md`,`NNNN` 为零填充递增序号(`0001`、`0002`……)。
- 一次决策一个文件。

## 生命周期规则

1. **需人工评审后方可写入**:不得直接落盘 ADR。先向相关方提交草案,获显式批准后再写文件。
2. **默认 `proposed`**:新建 ADR 状态为 `proposed`,经确认后才升为 `accepted`。`proposed` 应在一个迭代内收敛。
3. **永不删除**:被取代的 ADR 移入 `deprecated/`,并在头部注明指向替代 ADR 的链接。状态改为 `superseded`。

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

- 以 `aimodel.ChatCompleter` 作为唯一模型接入点(供应商中立)。
- `schema` 作为零内部依赖的根契约包。
- `checkpoint`(迭代级)与 `orchestrate` checkpoint(DAG 级)双轨分离。
- 上下文编辑采用"收敛策略单一判定点 + V1 兼容层隔离"。
- DAG 执行器"锁契约收尾单点"(错误与取消收敛到同一收尾路径)。

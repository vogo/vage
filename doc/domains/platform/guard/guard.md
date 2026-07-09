# 领域:guard(安全护栏与脱敏)

| 元数据 | 值 |
|--------|----|
| 业务组 | platform |
| 一句话 | 对进出模型与工具的内容施加三态护栏,并在第三方边界脱敏凭证 |
| 负责人 | vogo 维护者 |
| 状态 | active |
| 依赖领域 | agent-core(`schema`) |
| 对外 API | 是(Go 库 API) |
| 覆盖包 | `guard`、`security` |

## 概述

本领域是 Agent 的"安全边界"。

- `guard`:对消息作检查并返回 Pass / Rewrite / Block 的护栏构件,作用于三个位置 —— 输入、输出、工具结果。内置多类护栏并可串成链。
- `security`(credscrub):扫描字符串与 JSON 中的凭证(API token、access key、私钥),脱敏或上报。定位为 MCP client/server 边界的中间件,因为第三方工具 I/O 是攻击面。

**边界(不做):** 护栏不决定业务逻辑对错,只判定"内容是否越过安全红线";脱敏只处理凭证泄露,不做完整 DLP。

## 核心实体(概念层)

- **Guard(护栏)**:对一条 Message 作检查、返回 Result 的抽象。
- **Result 三态**:Pass(放行)/ Rewrite(就地改写内容)/ Block(阻断,返回 BlockedError)。
- **护栏家族**:
  - **PromptInjectionGuard** —— 提示注入检测。
  - **ToolResultInjectionGuard** —— 工具结果中的注入检测(工具输出是外部不可信内容)。
  - **ContentFilterGuard** —— 内容过滤。
  - **PIIGuard** —— 个人身份信息检测/脱敏。
  - **TopicGuard** —— 话题边界。
  - **LengthGuard** —— 长度限制。
  - **CustomGuard** —— 使用方自定义规则。
  - **护栏链(Chain)** —— 多护栏按序组合。
- **凭证脱敏(credscrub)**:识别并脱敏/上报凭证的中间件。

## 业务规则与不变式

| ID | 规则 |
|----|------|
| GRD-1 | **三态且仅三态**:护栏结果只能是 Pass / Rewrite / Block(章程安全基线)。 |
| GRD-2 | **Block 即阻断**:被 Block 的输入不得进入模型调用;工具结果被 Block 时替换为错误结果。 |
| GRD-3 | **输出护栏对部分结果宽容**:非完成态的部分输出被违反时只告警不失败(见 [agent-core](../../agent/agent-core/agent-core.md) AC-4)。 |
| GRD-4 | **工具结果视为不可信**:工具输出默认经注入护栏检查后再回喂模型。 |
| GRD-5 | **凭证不得跨边界泄露**:MCP 边界的第三方 I/O 必须经 credscrub 脱敏后再落日志/流(章程安全基线)。 |
| GRD-6 | **护栏链按序短路**:链中任一护栏 Block 即终止后续检查。 |

## 状态与转换

护栏无长期状态,每次对一条消息独立判定。护栏在 Agent 运行中的三个触发点(输入前、输出后、工具结果回喂前)见 [agent-core](../../agent/agent-core/agent-core.md) 的生命周期。

## 与其他领域的交互

- **agent-core**:护栏在 ReAct 循环的输入/输出/工具结果三处被调用。
- **tooling**:MCP client/server 边界接入 credscrub 脱敏。

技术实现(各护栏配置、模式规则、脱敏扫描)见 [guard-design](guard-design.md)。

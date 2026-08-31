# vage 章程(Constitution)

本文件是项目的**最高约束**:一组几乎不变的原则,定义任何设计、代码都不得逾越的红线。它高于领域文档、高于代码、高于个人偏好。每条都必须可对"是否被违反?"作 Yes/No 判定。年度评审一次。

## 使命与价值序

**使命**:提供保真于厂商协议、可观测、可运维的 Go LLM 代理框架。

**价值优先级(冲突时按序裁决):**

1. **契约稳定 > 功能扩张** —— `schema` 契约层与各子系统公共接口的向后兼容优先于新增能力。
2. **协议保真 > 便利** —— `schema.Message` 以 provider-neutral 的 canonical 状态(`role` + `parts`)为唯一事实源;厂商 wire 差异只在 `largemodel` 的 provider codec 边界编解码,同协议未修改时可经可选 `origin` 无损回放,不得用有损归一掩盖跨厂商差异。
3. **正确性与安全 > 性能** —— 护栏、预算、幂等、断点续跑的正确性优先于吞吐。
4. **可组合 > 大一统** —— 宁可提供小而可替换的构件,也不做无法拆分的单体。新增 ReAct 热路径横切接缝须先经 ADR 评审(见 [architecture/adr/adr.md](architecture/adr/adr.md)).

冲突裁决:序号小者胜;无法裁决时升级到维护者评审。

## 技术栈基线

- **大模型接入**:唯一入口是 `largemodel` 的 `Caller` 协议调用层,其后端是 `github.com/vogo/aimodel` 各 provider 的 native 客户端。当前公开 Caller 仅接 OpenAI Chat 与 Anthropic Messages;`openai-responses` 常量已预留,provider 侧有路由能力,但尚未接入公开 Caller(`Protocol.Valid` 拒绝)。厂商协议细节只允许出现在 `largemodel` 的 provider 实现中,不得散落到 `schema` 或其他核心包。
- **重试与路由不自研**:调用内重试、端点存活判定与同协议多端点的选择策略一律取 `largemodel/router`,vage 只负责把它接到 `Caller` 后端接口上并承载并发,不得再提供第二套重试或熔断机制。跨协议的路由与故障转移不做。
- **引入新核心依赖**:需经维护者评审(说明必要性、许可证兼容性、维护活跃度)。
- **禁止**:在核心库中硬编码厂商私有端点、模型名或鉴权方式;凭证与 base URL 一律由调用方注入。

## 分层与依赖红线

- **`schema` 是最底层契约包**,只依赖标准库,不得依赖 `aimodel` 或任何其他 vage 内部包。所有子系统依赖 `schema`,反向依赖被禁止。
- **WHAT / HOW 分离**:业务行为写在 `<domain>.md`,技术实现写在 `<domain>-design.md`,二者不混。
- **文档不含实现细节**:凡读代码可还原的内容不写入文档(见 `overview.md` 使用规则)。

## 安全基线(不可协商)

- **凭证绝不泄露**:跨越 MCP client/server 等第三方 I/O 边界的字符串与 JSON 必须经 `security`(credscrub)脱敏后再落日志/流。任何位置不得硬编码密钥。
- **默认拒绝的护栏语义**:护栏动作只有三态 —— Pass / Rewrite(就地改写)/ Block(阻断)。被 Block 的输入不得进入模型调用。
- **工具执行隔离**:执行外部命令的工具(如 bash)必须进程隔离,不得共享框架进程的权限上下文。
- **最小权限**:工具、子代理只能访问其被显式授予的资源(见 `sessionview` 的资源预算、`tool` 的 ResourceTracker)。

## AI 工程原则

- **AI 生成的代码不自动合并**:必须通过 lint、test、license-check 三道门禁(`make build`)方可进入 `main`。
- **操作可逆优先**:破坏性、对外可见的操作需显式确认或幂等保护;补偿(Saga)用于回滚已提交的编排步骤。
- **上下文隔离**:子代理(agent-as-tool)通过 `sessionview` 拿到的是父会话的**只读冻结快照**,其 scratch 写入不得污染父会话的 notes。
- **热路径不因辅助功能失败而中断**:检查点保存、指标上报等失败只记 warn,绝不打断 ReAct 主循环。

## 编码原则

- **质量门禁**:所有提交必须通过 `make build`(license-check → goimports/gofmt/gofumpt 格式化 → golangci-lint → `go test`)。
- **许可证头**:每个 Go 源文件必须带 Apache-2.0 许可证头(`license-header-checker` 校验)。
- **测试底线**:核心逻辑必须有单元测试;跨包行为进 `integrations/` 集成矩阵;覆盖率上报 Codecov。
- **命名一致**:目录/文件用 kebab-case;日期用 `YYYY-MM-DD`。

## 运维原则

- **可观测优先**:Agent 全生命周期(启动、迭代、LLM 调用、工具调用、预算、护栏、编排)必须发出结构化事件(`schema.Event`),供 hook 与流式消费。
- **HTTP 服务三态**:对外执行接口必须同时支持 sync、streaming、async 三种语义。

## 合规基线

- **许可**:Apache License 2.0。所有分发物与依赖必须许可证兼容。
- **数据留存**:框架自身不持久化最终用户 PII;会话/记忆存储的留存策略由使用方通过可插拔后端决定,框架仅提供护栏与脱敏能力。

## 修订程序

- 任何维护者可提议修订。变更 `constitution.md` 需维护者显式签核,并在合并前公示。
- 若某条款与当前代码现状冲突,应附整改计划,而非假装合规。

# vage 知识库总览

`vage` 是构建 LLM 智能代理系统的 Go 框架。本目录（`doc/`）是项目的**文档单一事实来源**,记录代码无法表达的东西:意图与理由、边界与约束、跨越多处代码的横切决策、稳定的业务不变式。

## 使用规则

1. **文档写 WHY,代码写 HOW。** 凡是"读代码即可还原"的内容不写入文档,只在文档中链接到对应代码位置。文档只承载代码表达不了的高度:为什么存在、为什么这样设计、什么绝不能发生。
2. **单一事实来源。** `doc/` = 当前状态;`archived/` = 已废弃。二者不混放。
3. **就地增量更新。** 更新既有文件,而非创建带版本号的新文件名。
4. **文档不含代码。** 不放源码、SQL、部署配置。图用 Mermaid,契约用 OpenAPI YAML,结构信息用表格。
5. **同一模板结构。** 所有领域遵循 `references/` 中一致的模板。
6. **明确负空间。** 每个文档都要写清非目标("我们不做什么")与反场景("什么绝不能发生"),不只写happy path。所有非功能指标必须量化。

## 目录导航

| 路径 | 内容 |
|------|------|
| `project.md` | 项目愿景、范围、相关方 |
| `constitution.md` | 全局硬约束、不可逾越的红线 |
| `glossary.md` | 全局业务术语表 |
| `architecture/architecture.md` | 架构总览、分层与依赖拓扑 |
| `architecture/adr/` | 架构决策记录(ADR) |
| `domains/<组>/` | 各业务组与其下的限界上下文(领域) |
| `archived/` | 已废弃内容 |

## 领域地图(限界上下文)

框架划分为 4 个业务组、8 个领域,对应仓库中的 Go 包:

| 组 | 领域 | 覆盖的包 | 一句话职责 |
|----|------|----------|-----------|
| `agent` | `agent-core` | `agent`、`schema`、`prompt` | Agent 统一抽象与四种形态、供应商中立数据契约、提示词模板 |
| `agent` | `orchestration` | `orchestrate`、`checkpoint` | DAG 编排引擎与断点续跑快照 |
| `memory` | `memory` | `memory`、`context` | 三层记忆、上下文装配与压缩 |
| `memory` | `session` | `session`、`workspace`、`sessionview` | 会话实体、持久工作区、子代理视图 |
| `capability` | `model` | `largemodel` | LLM 中间件装饰链与上下文编辑 |
| `capability` | `tooling` | `tool`、`mcp`、`skill` | 工具系统、MCP 协议、Agent Skills |
| `platform` | `guard` | `guard`、`security` | 安全护栏与凭证脱敏 |
| `platform` | `service` | `service`、`hook`、`eval`、`vector` | HTTP 服务、事件 hook、评测、向量召回 |

框架能力总览见仓库根的 [../README.md](../README.md)。

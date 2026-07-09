# 设计:guard

对应领域行为见 [guard.md](guard.md)。

## 组件与职责

| 文件/关键类型 | 设计角色 |
|---------------|----------|
| `guard/guard.go`(`Guard`/`Message`/`Result`/`BlockedError`) | 护栏契约与三态结果 |
| `guard/chain.go` | 护栏链,按序短路 |
| `guard/injection.go`(`PromptInjectionGuard`) | 提示注入检测 |
| `guard/tool_result.go`(`ToolResultInjectionGuard`) | 工具结果注入检测 |
| `guard/content_filter.go`(`ContentFilterGuard`/`PatternRule`) | 内容过滤 |
| `guard/pii.go`(`PIIGuard`/`SeveredPatternRule`) | PII 检测/脱敏 |
| `guard/topic.go`(`TopicGuard`) | 话题边界 |
| `guard/length.go`(`LengthGuard`) | 长度限制 |
| `guard/custom.go`(`CustomGuard`) | 自定义规则 |
| `security`(credscrub) | 凭证扫描/脱敏中间件 |

## 关键设计决策

- **统一契约 + 三态结果**:所有护栏实现同一 `Guard` 接口、返回同一 `Result` 三态,使它们可无差别地组合进链、插入任一触发点。
- **护栏与位置解耦**:同一护栏可用于输入、输出或工具结果三个位置;位置由 Agent 装配决定,而非护栏内部硬编码。
- **工具结果专用护栏**:工具输出是外部不可信内容,单列 `ToolResultInjectionGuard` 而非复用输入护栏,以针对其特征。
- **脱敏定位在边界**:credscrub 作为 MCP client/server 边界的中间件,把"第三方 I/O 是攻击面"这一假设落到具体拦截点。
- **模式规则可配置**:内容过滤、PII 等基于可配置的模式规则,使用方按需扩展。

## 非功能考量

- **性能**:护栏在 ReAct 热路径上,须保持轻量;链的短路避免无谓检查。
- **可扩展**:CustomGuard 与可配置模式规则支撑使用方自定义安全策略。
- **误报权衡**:输出护栏对部分结果宽容(只告警不失败),避免因中间态误伤打断运行。

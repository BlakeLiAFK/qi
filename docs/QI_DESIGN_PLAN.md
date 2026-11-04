# qi 产品与工程设计方案

版本：设计草案 v0.1  
日期：2026-11-04  
对象：qi 项目维护者与实现者  
状态：供评审与排期，不代表已实现或已通过验证

## 1 结论与设计边界

qi 应当是一个用 Go 实现、以个人长期使用为目标的 agent 产品：普通对话保持轻量，需要工具时获得可审计的能力，需要编程时委派专门的 coding agent，并在中断后仍知道哪些工作已经完成、哪些等待授权、哪些结果不确定。

推荐路线是“小核心、显式能力、持久状态、可替换适配器”，而不是复刻 Pi 的代码、终端界面、配置格式或全部功能。借鉴应发生在架构思想和用户体验层面。是否与 Pi 某个接口兼容，必须作为单独承诺、单独测试，不应由“inspired by Pi”推导。

首个可用版本交付 REPL 与嵌入式响应式 WebUI，二者共享内核；包括本地会话、多任务并发与隔离、卡片式结果与审批、可靠工具执行、持久任务、审批恢复和一个经过验证的 coding adapter。完整 TUI 不进入首版。公网 WebUI 是明确需求，必须经过认证与 HTTPS 验收后开放；多渠道、远程 MCP、插件市场、向量记忆后置。没有持久执行语义之前，不宣传“自主后台助手”；没有操作系统级隔离之前，不宣传“安全沙箱”。

### 1.1 核心决策

1. 单用户、本地优先，但支持经认证的公网 WebUI。一个 qi daemon 拥有一个数据目录；REPL 和 WebUI 连接同一 application service，不能各自直接调度同一数据库。浏览器关闭不停止 daemon 中的任务。
2. agent 内核不依赖 CLI、MCP、HTTP 或特定模型供应商。调用模型、提出动作、批准动作和执行动作是不同职责。
3. simple、tools、coding 是执行策略，不是三套互不相通的会话。策略转换保留 session、run、task 和授权来源。
4. 使用 SQLite 保存业务状态、事件和执行意图；第一版采用串行数据库写入和每个 session 单一活动 run。
5. 工具结果、网页、文件和插件说明均是外部数据，不具备授予权限的能力。
6. 授权绑定具体动作及目标，不绑定模型一句“用户同意了”。审批跨重启可恢复，但修改参数或权限版本后必须重新评估。
7. 副作用不能普遍保证 exactly-once。对不支持幂等或结果查询的工具，崩溃窗口进入 `unknown`，禁止盲目重试。
8. 插件采用独立进程和 manifest；不采用 Go 动态 `.so` 插件。进程隔离便于崩溃治理，但不是安全隔离。
9. 默认不把一个供应商收到的用户数据自动转给另一个供应商。模型 fallback 必须在显式允许的端点集合内。
10. 先证明真实工作闭环，再增加 UI 和自治程度。路线图以验收门槛而非日期作为发布条件。

## 2 当前基线与证据等级

本方案区分三种内容：**当前事实**来自已检查文件或已有审计；**设计决策**是推荐实现；**待验证事项**需要后续测试或资料核实。未运行的测试不能写为通过，工作区文件不能写为远程已发布能力。

### 2.1 现有原型

截至本次设计审阅，远程 `master` 已推进至 `b0e684b`：在 `cb575060` 的初步 CLI、模型与工具循环、MCP stdio 和 coding workers 基础上，新增了经过单元测试的 SQLite 会话及运行事件存储。该存储尚未接入 CLI 或 WebUI；多任务和审批恢复尚未完成。后续以实际远程提交为准。

本地 README 与源码可确认的结构：

- `cmd/qi` 提供 CLI 入口；原 README 将产品定位为早期原型。
- `internal/agent` 定义 `Model`、`Tool` 和串行 model/tool loop，默认最多 20 个模型步骤，事件是完整消息级而非 token 流。
- `internal/provider` 是 OpenAI-compatible Chat Completions 适配层。
- `internal/plugins` 启动显式配置的 MCP stdio server，并适配工具。
- `internal/workers` 提供 coding 启动、状态、取消；README 说明任务只在进程内，退出会取消 worker。
- 当前插件和 worker 继承父进程环境，插件不是沙箱；固定工作目录也不是沙箱。
- 当前终端审批和全会话 `-yes-tools` 不能直接搬到网络服务或多渠道权限体系。

### 2.2 SQLite 存储的准确定位

`b0e684b` 已提交 SQLite 会话、运行状态和事件存储，并通过存储单元测试与 race 测试。它尚未接入现有 CLI 或 WebUI；仅有存储不等于任务可以持久运行或安全恢复。多进程所有权、执行意图、审批、工具副作用及不确定结果仍须按后续设计完成并验证。当前工作区的进一步修改不代表已发布。

### 2.3 尚未证明的能力

没有证据表明当前版本已提供可恢复后台任务、可靠消息投递、流式多供应商、隔离插件、上下文压缩、长期记忆、定时器、多渠道身份验证或生产级安全审计。此文所有相关内容均是目标设计。

### 2.4 必须优先修复的审计发现

以下来自本次项目审计交接，尚不能声称已修复：

- **P0**：one-shot 返回时取消 coding tasks，用户可能收到“已启动”而工作被终止。持久任务模式必须交由 daemon；临时模式必须等到终态或明确告知取消。
- **P0**：MCP 超时不证明服务端停止，副作用结果可能 unknown。超时后禁止盲目重试，需结果查询或人工核对。
- **P0**：插件继承宿主环境和 secrets；`-yes-tools` 没有目标与权限范围。替换为最小环境及作用域授权，旧危险选项若保留必须明确警告且不能用于公网。
- **P1**：worker 只保留输出前缀可能丢失最后答案。持久完整日志，限额情况下保留头尾摘要及 artifact 引用，并标明截断。
- **P1**：CLI 在错误后丢弃 partial history。错误返回的已有消息和工具结果必须持久化，不能让下一轮重复已经执行的动作。
- **P1**：阻塞终端输入影响 Ctrl-C。REPL 输入生命周期与 run cancellation 分离，覆盖等待输入、审批、生成和子进程四种状态。
- **P1**：现有测试主要是 fake HTTP/shell，不证明真实 MCP interoperability 或真实 coding CLI 行为。真实固定版本 smoke test 是发布门槛。

Pi 研究的可用启发是区分持久事件与上下文投影、区分工具函数返回与任务真正 settled、区分说明型 skills 与可执行扩展，并认真定义并行工具输出的顺序。qi 应根据自身持久任务目标设计这些边界，不照搬其实现。

## 3 产品范围与典型体验

### 3.1 产品承诺

- 普通问题直接回答，不为一句解释启动 worker，也不加载所有插件说明。
- 需要外部数据时，告诉用户将访问什么；需要副作用时，按照策略批准后执行。
- 编程请求由 qi 管理意图、授权、进度和结果，实际代码操作优先由用户选择的 coding agent 完成。
- 用户能查看进行中的工作、审批、消耗和失败原因，并可停止工作、恢复会话、撤销未来授权。
- 任务最终结果包含交付物、验证依据和未解决事项，不以“已启动进程”代替完成。

### 3.2 首版范围

纳入：REPL 多轮、one-shot 与嵌入式响应式 WebUI；多任务标签页、独立上下文和权限、卡片交互；至少一个真实模型适配器；结构化事件；SQLite 会话与任务；本地附件；显式工具目录；MCP stdio；持久审批；一个 coding adapter；取消、恢复、预算和基本诊断。

不纳入：多租户 SaaS、插件市场自动安装、任意第三方脚本自动信任、通用浏览器自动化、完整 IDE、分布式工作流、无监督支付与签署、跨渠道自动合并身份、默认全盘索引、无限自主代理循环。

### 3.3 三种路径

| 路径 | 适用情形 | 可用能力 | 结束条件 |
| --- | --- | --- | --- |
| simple | 解释、改写、已有上下文内问答 | 模型，无外部工具 | 生成完整答案或提出必要问题 |
| tools | 搜索、读文件、读取已连接服务、授权操作 | 本次筛选且政策允许的工具 | 结果经检查；副作用状态明确 |
| coding | 改代码、运行测试、仓库诊断、审查 | 固定 workspace 的 coding adapter | 产物及测试可核验，或明确阻塞 |

simple 不是“廉价模型”的同义词；tools 也不一定需要大模型。模型选择与能力选择分别决定。

### 3.4 路由顺序

1. 用户显式模式、禁止事项和 workspace 限制优先。
2. 确定性规则识别明确命令、审批回复、任务查询和取消，尽可能不调用模型。
3. 对自然语言做轻量分类，输出结构化 `route/reason/confidence/required_capabilities`。规则足够明确时不增加分类模型调用。
4. 低置信度且选择会扩大数据访问或副作用时澄清；仅影响回答方式时采用最小能力方案。
5. 路由器只能建议能力集合；policy engine 才决定是否允许。用户说“解释这个函数”并不授权执行 shell。
6. 升级路径追加 `route.changed` 事件，收缩能力可自动发生，扩大访问必须重新评估。工具返回的文字不能触发授权升级。

验收语料至少涵盖中文、英文、混合术语、误导性插件内容和含糊编程请求。重点衡量不必要工具调用率、漏路由率和越权率，而非只看分类准确率。

## 4 总体架构

```text
REPL / 响应式 WebUI / HTTP / 未来消息渠道
            │ 标准输入事件与已验证身份
            ▼
Application Service ─ Session coordinator ─ Router
            │                    │
            ▼                    ▼
Task supervisor             Agent runtime
            │                    │
            ├──────── Policy / Approval ────────┐
            │                                  │
            ▼                                  ▼
Coding adapters                         Tool executor
                                         │ builtin / MCP
            └──────── State store / Outbox / Event log
                              │
                         SQLite + artifacts

Model adapters 由 Agent runtime 调用；所有网络发送受数据目标策略约束。
```

### 4.1 包边界

建议逐步形成以下边界，避免一开始为了“干净架构”移动所有现有文件：

- `domain`：Message、Session、Run、Task、Action、Approval、Event 及状态转移，不能依赖网络库。
- `runtime`：一次 run 的执行状态机、上下文构建、预算、工具提案处理。
- `app`：面向客户端的 commands/queries，身份、事务和幂等入口。
- `store/sqlite`：迁移、事务、查询、事件追加、lease 和 outbox。
- `models`：供应商适配、能力描述、usage、错误分类。
- `policy`：权限判定、动作规范化、授权绑定、撤销。
- `tools`：工具注册与执行；`plugins/mcp` 仅是其中一个来源。
- `tasks`：持久队列、worker 管理、恢复与取消。
- `coding`：编码 agent 的版本检查、进程启动、输出转换和产物收集。
- `context`、`memory`、`skills`：上下文选择、可追溯知识与操作说明。
- `channels`：终端、HTTP 和未来 IM 适配；不能直接写任务状态或绕过 policy。

依赖方向为适配器依赖应用接口，应用依赖领域接口；不要让 agent 包导入 SQLite 或让模型适配器发送用户通知。

### 4.2 并发模型

一个 session 同时最多一个活动生成 run。新用户输入可排队、取消当前 run，或作为明确 steering 事件进入下一安全边界，不能直接修改正被模型消费的切片。子任务独立执行，但写回父会话必须经过 coordinator。

默认工具串行。仅当工具声明、策略和本次动作分析均确认只读且互不依赖时允许有界并行。外部写操作保持顺序；不同任务并行必须受全局、供应商、插件和 workspace 限额约束。SQLite 事务内禁止等待模型、网络、用户审批或子进程。

## 5 消息与模型抽象

### 5.1 标准消息

消息保留稳定 ID、role、创建时间、来源、父消息 ID、content blocks 与工具调用关联。blocks 至少包含 text、image reference、file reference、tool call、tool result。图片与文件本体放 artifact store，不塞进每条事件。

区分用户原话、系统约束、第三方内容、模型输出和工具证据，保留 provenance。不能把外部工具结果直接提升为 system 消息。供应商原始响应可选择性脱敏留存，不成为公共领域模型。

### 5.2 模型接口建议

```go
// 设计接口草图，并非当前已实现 API。
type Model interface {
    Capabilities() ModelCapabilities
    Generate(ctx context.Context, req GenerateRequest, emit func(ModelEvent) error) (GenerateResult, error)
}
```

Capabilities 描述 streaming、tools、parallel tool calls、vision、JSON schema、context limit、max output 和 usage 来源。未知能力应标为 unknown，不依靠名字猜测。适配器负责供应商协议差异，runtime 负责步骤和权限。

首个适配器继续支持 OpenAI-compatible Chat Completions，但“兼容”是经过测试的功能子集，不代表任意同名端点行为相同。随后按需求增加 OpenAI Responses 与 Anthropic；不要通过抹平差异丢掉工具 ID、finish reason、拒绝、错误或 usage。

### 5.3 流式与失败处理

事件按 `response.started → text.delta/tool.arguments.delta → response.completed` 组织。只有收到完整工具调用并通过 JSON/schema 校验后才能执行。中途断流的工具参数禁止执行，未完成的 assistant 文本标记 partial。

429、可重试 5xx、连接建立失败按供应商建议及带抖动指数退避重试；401/403、schema 错误、上下文过长不盲重试。模型请求不是外部工具副作用，但重试可能重复计费，需要预算和 retry limit。已发给用户的 token 不直接重复播放；必要时显示一次“生成中断，正在重试”。

切换模型前检查上下文能力、工具语法和数据发送目标。不能把供应商私有 continuation ID 当作跨供应商会话。工具调用结果匹配、usage 未知值、reasoning token 等差异保留明确语义。

## 6 工具与 MCP

### 6.1 工具契约

每个工具定义包括稳定名称与版本、描述、input/output schema、能力标签、风险默认值、超时、输出上限、幂等能力、是否可查询执行结果、取消语义和来源插件。

工具元数据只提供建议；不信任服务端自称 `readOnly` 或 `idempotent`。实际 risk 由 host 的配置与动作参数共同决定。参数先 canonicalize，再 schema 校验、路径/目标检查、授权检查，最后执行。未知字段默认拒绝，而不是默默忽略权限相关参数。

结果返回结构化状态 `succeeded/failed/cancelled/unknown`、可展示文本、结构化数据、artifact references 和 retryability。失败必须区分“未启动”“已启动但失败”“结果未知”，避免模型把超时视为安全重试。

### 6.2 MCP 实施顺序

第一阶段只做明确配置的 stdio：初始化、版本与 capability 协商、`tools/list` 分页、`tools/call`、协议错误、超时、取消、server 退出和输出限制。stdout 专用于协议，stderr 作为限额日志，不能混入帧解析。

第二阶段再评估 Streamable HTTP、认证和重连；不要以一次 HTTP 请求成功声称支持完整远程 MCP。Resources、prompts、sampling、elicitation 等 capability 按需实现，不默认全部开启。server 请求 host 代表用户行动必须回到 host policy，不能在协议处理线程内直接批准。

工具名对外采用命名空间，但存储真实 server/tool 标识；名称长度和字符规则由各模型适配器映射，映射冲突在加载时失败。服务端工具列表变化生成新的 registry revision；正在等待审批的旧工具定义若发生语义变化则失效。

### 6.3 内置工具的范围

首版可提供受限文件读写、目录列举、搜索及 artifact 导出；shell 应作为高权限能力显式开启，不能借 shell 绕过文件工具边界。既有 coding agent 能完成的代码工作优先委派，不同时维护第二套庞大的 shell coding loop。

路径检查覆盖绝对路径、`..`、符号链接、大小写差异、设备文件、管道和 Windows reparse points。先检查后打开存在 TOCTOU 风险；真正安全的文件访问需平台支持的相对目录句柄和拒绝链接机制。无法提供时明确降低保证，不声称 canonical path 检查即构成沙箱。

## 7 插件生命周期与安全模型

### 7.1 Manifest

建议 manifest 包含 `schema_version/id/version/entrypoint/args/workdir/env_allowlist/secret_refs/capabilities/supported_os/protocol_version/integrity`。配置使用固定版本和校验摘要，安装位置与运行目录分离。敏感值只存 secret reference，不写入 manifest。

生命周期为 `discovered → validated → installed → disabled/enabled → starting → ready → unhealthy → stopped`；更新建立新版本，原子切换，健康检查失败可回滚。安装、首次运行、扩权、更新和移除是不同操作。停用应阻止新调用，并询问或遵循既定策略处理活动任务。

### 7.2 信任边界

默认不自动发现并运行仓库里的配置，不根据模型建议下载执行任意程序。插件启动本身可以有副作用，不能仅审批 `tools/call` 而忽略启动授权。

子进程环境从最小 allowlist 构建，逐个注入必要 secret，不能继承全部环境。进程资源有 CPU/内存/文件描述符/运行时长/输出上限；平台不支持的限额在诊断中明示。网络访问限制必须由操作系统、容器或代理强制，manifest 中写 `network: false` 本身不阻止出网。

不要使用 `plugin.Open` 作为公共扩展 ABI：编译器和依赖版本耦合、跨平台分发困难、崩溃与权限隔离不足。第一版独立进程，后续若需要 WASM，再明确其 host calls、资源和供应链边界。

### 7.3 插件供应链

用户明确选择来源，固定版本；记录下载来源、摘要、许可证及启用时间。签名只能证明发布者/完整性，不能证明程序安全。默认关闭自更新和启动脚本；更新后 capability 扩大须重新批准。卸载不默认删除用户数据，删除单独确认。

## 8 Skills 设计

Skill 是有版本的操作说明及可选资源，不是可绕过策略的代码权限。建议目录含 `SKILL.md`、元数据、references 和 scripts；加载 descriptions 供选择，只有匹配任务时才加载正文，避免全量上下文膨胀。

元数据记录 ID、版本、来源、适用任务、依赖工具、预估上下文、资源摘要。查找路径优先级明确为系统安装、用户安装、显式项目启用；项目内同名 skill 不得静默覆盖全局 skill。skills 修改触发索引刷新和审计，不自动授予新工具。

指令优先级：用户授权和 host policy 高于 skill。仓库说明、插件说明和检索文档都不可自行授予权限。skill 附带脚本属于可执行代码，执行前按工具政策处理；不能因为正文写了“必须运行”便自动执行。

第一版定义 qi 自己的最小 schema；对其他生态 SKILL.md 可提供导入器，但必须公布支持字段、忽略字段和行为差异。不能未经兼容测试宣称“所有 Pi/Claude skills 即插即用”。

## 9 记忆与上下文管理

### 9.1 四类数据

- Transcript：用户和助手实际说过的话，以及工具调用与结果，是事实日志。
- Working context：本次推理使用的窗口、摘要、任务状态和选定证据，可重新构建。
- Durable memory：用户显式偏好、经验证事实和带来源的推断；允许查看、更正、删除。
- Artifacts：大文件、日志、代码补丁和图片，保存内容摘要、媒体类型、大小和访问路径。

这四类不能混为“把历史都存起来”。默认保存本地会话，但长期事实提取应可关闭，敏感数据和凭据默认不进入 durable memory。删除必须覆盖索引、摘要和后续检索；备份保留策略另行说明。

### 9.2 Context builder

按固定预算构建：不可删除的系统/policy 条件、当前用户请求、任务/审批状态、必要工具 schema、近期消息、选定记忆与证据。给输出和潜在工具结果预留空间；按各供应商 tokenizer 或保守估计计算，记录 estimate 与 actual 的差异。

首版使用 SQLite FTS5 与来源/时间/项目过滤，不急于引入向量数据库。只有检索评估证明收益后增加 embeddings，且明确发送数据的供应商、失效和重建策略。检索命中不是事实验证，应展示来源与更新时间。

### 9.3 压缩触发与不变量

建议上下文达到有效窗口的 70% 时尝试压缩，85% 时必须裁剪或暂停；比例可配置，不能假定所有模型窗口相同。压缩发生在完整消息/工具结果边界，不拆散 tool call/result 对。

摘要必须保留：用户目标、限制、明确授权与撤销、已执行动作及结果、未决审批、未完成任务、关键原始证据位置和不确定性。**授权的可执行记录来自数据库，不来自摘要。** 摘要即使丢失“禁止发送”，policy 仍必须阻止发送。

每次 compaction 保存被覆盖的消息范围、摘要、模型版本、输入摘要和时间；保留原 transcript 以便重建。失败时不删除历史。后续发现摘要错误可以回退。不得压缩掉 unresolved/unknown 工具执行、正在等待用户的原问题或审批绑定字段。

### 9.4 长期记忆写入

模型可提议 memory candidate，但必须附来源、scope、置信度、有效期和敏感级别。用户明确要求记住的偏好可以直接入库；推断事实应标记为 inferred 并接受纠正。矛盾事实保留版本关系，不用“最后一句 wins”覆盖重要历史。个人、项目、workspace 和渠道范围不可默认互相泄漏。

## 10 SQLite 数据模型与迁移

以下是目标逻辑模型，需通过迁移实现，并不是当前 `store.go` 已有表。ID 使用随机 UUID/ULID 等不含敏感信息的稳定值；时间保存 UTC 毫秒整数，展示时再转换时区。JSON 需 schema 校验，重要约束不只留在 JSON 中。

### 10.1 表与约束

| 表 | 核心字段 | 约束与用途 |
| --- | --- | --- |
| schema_migrations | version, checksum, applied_at | 版本唯一，校验已应用脚本不可篡改 |
| sessions | id, title, owner_id, scope, revision, created_at, updated_at | session revision 用于乐观锁 |
| messages | id, session_id, seq, role, blocks_json, source_json, created_at | UNIQUE(session_id, seq)，追加而非整段覆盖 |
| runs | id, session_id, input_message_id, status, route, model_ref, budget_json, revision | 一个 session 至多一个活动 run |
| tasks | id, run_id, parent_id, kind, state, input_json, result_json, revision, deadline_at | 显式状态、父子关系、取消与完成时间 |
| task_attempts | id, task_id, attempt_no, state, lease_owner, lease_until, fence, process_ref | UNIQUE(task_id, attempt_no)，租约与 fencing |
| actions | id, task_id, tool_ref, tool_revision, canonical_args, args_hash, risk, state, idempotency_key | 同一逻辑动作稳定 ID，重试另记 attempt |
| action_attempts | id, action_id, attempt_no, state, external_ref, started_at, ended_at, error_json | 执行前先写 intent；unknown 独立状态 |
| approvals | id, action_id, action_hash, policy_revision, subject_id, state, expires_at, decided_at | 审批决定不可修改，只能追加撤销/替代 |
| grants | id, subject_id, scope_json, valid_until, revision, revoked_at | 限定目标、操作和数据范围的授权 |
| events | seq, id, aggregate_type, aggregate_id, aggregate_revision, type, payload_json, created_at | 全局递增 seq，事务内和业务状态一起写 |
| outbox | id, event_id, channel, destination_ref, payload_json, dedupe_key, state, next_attempt_at | UNIQUE(channel, dedupe_key)，可靠投递 |
| inbox | channel, account_ref, external_message_id, received_at, session_id | 复合主键，去重入站事件 |
| artifacts | id, digest, size, media_type, local_ref, retention_until, sensitivity | 内容寻址、限额及完整性校验 |
| memories | id, scope, text, source_message_id, confidence, valid_from, valid_until, supersedes_id | 支持删除、修订、来源追溯 |
| compactions | id, session_id, from_seq, to_seq, summary, model_ref, source_hash | 不改变原始消息 |
| plugins | id, version, digest, manifest_json, state, approved_capabilities | 工具 registry 另记版本快照 |
| channel_bindings | id, channel, account_ref, conversation_ref, owner_id, auth_state | 不凭显示名映射 owner |

索引至少覆盖 `tasks(state, deadline_at)`、`task_attempts(lease_until)`、`actions(task_id,state)`、`approvals(state,expires_at)`、`outbox(state,next_attempt_at)`、`messages(session_id,seq)`、`events(aggregate_id,seq)`。外键删除策略明确指定，核心审计记录不随会话删除默默 cascade；隐私删除采用专门流程。

### 10.2 事务边界

接受用户消息：校验入站身份与幂等键 → 写 inbox/message → 建立 run/task → 写事件与 outbox → commit。重复消息返回原来的结果引用。

提出动作：写 actions + `approval.requested` 或 `action.ready` 事件 → commit。用户批准：确认 actor、有效期、action hash 与 policy revision → compare-and-swap → 写事件 → commit。

执行动作：事务领取 lease 和写 `action_attempts: dispatching` → commit → 调用外部系统 → 事务记录确定结果和 outbox。外部调用与 SQLite 不可能成为同一原子事务，因此中间崩溃必须按恢复协议处理。

### 10.3 存储运行约束

启用 WAL、foreign_keys、busy timeout；对所有连接一致设置 PRAGMA。首版一个写连接、有限只读连接，禁止网络文件系统上的 WAL 多机共享。数据库与 WAL/SHM、备份、artifact 目录都需受用户权限保护，不能只把主 `.db` chmod 0600。

使用 `synchronous=FULL` 作为持久模式默认值，性能优化必须基于测试。它不等于在所有硬件和文件系统上绝不丢失。SQLite 默认不加密；如需加密，应选择经验证方案及密钥管理，不自行实现密码学。

迁移运行前检查版本与备份；事务内可执行的迁移原子提交，不能原子执行的迁移要有可恢复阶段。旧版本程序遇到较新 schema 应拒绝写入。备份使用 SQLite backup API 或一致快照，不直接复制正在写入的 `.db`。恢复流程必须实际演练。

## 11 任务状态机与审批恢复

### 11.1 状态定义

任务：`queued → running → waiting_approval / waiting_input / waiting_external → succeeded / failed / cancelled`。发生不可确定的副作用结果时进入 `needs_reconciliation`，不能伪装成普通失败。取消请求先进入 `cancelling`；确认执行停止或结果已查明后才进入 `cancelled`。

工具动作：`proposed → awaiting_approval → ready → dispatching → succeeded/failed/unknown/cancelled`。任务状态与动作状态不同，用户取消任务不会撤销已经成功发送的消息或购买。

审批：`pending → approved/denied/expired/revoked`。一次性批准只消费一次，范围批准存 grants。批量审批需要列明每项动作及共同边界，不能用一个“允许”覆盖后来新增动作。

### 11.2 审批绑定

绑定字段包括 owner、action ID、规范化参数哈希、工具及版本、目标账户/路径/收件人、金额/数据范围、工作目录、policy revision、有效期与必要的资源版本。审批 UI 展示人能理解的实际后果，不只显示巨大 JSON。

相同动作的安全重试不必重复要求用户，但前提是已确认前一次未生效或具有可靠幂等键。改收件人、扩目录、提金额、换插件版本、扩大数据范围或撤销授权会使旧批准失效。

### 11.3 重启恢复算法

1. 获取 daemon 独占锁与启动 ID，验证数据库完整性、迁移和 policy revision。
2. 读取非终态 tasks/actions/approvals；过期租约不直接证明外部进程已经停止。
3. 通过可验证的进程身份和启动时间检查本地 worker，不能仅依赖可能被复用的 PID。无法重新接管 stdout 的 worker 按 adapter 约定转查询或待处理。
4. 对 queued/ready 且没有 dispatch 记录的动作可重新调度。
5. 对 dispatching：若提供幂等 key 和可靠 status query，先查询；已成功则补录，明确未执行才重试；否则标记 unknown 并请求核对。
6. 对 pending approval：恢复同一审批请求和绑定值，不自动批准；过期或 policy 变化则重新请求。
7. 对 outbox：重试可幂等投递；平台不提供去重或查询时，超时结果标记 uncertain，不无限补发。
8. 用户看到一条归纳后的恢复结果，列出继续、需授权和需核对事项，不把每张表恢复过程刷屏。

### 11.4 长任务与预算

任务具有 deadline、最大模型步骤、费用上限、工具调用次数、最大并发和输出/磁盘预算。预算接近阈值时提示，达到上限暂停而非悄悄换模型继续花费。时间到期取消只表示不再继续执行，不能承诺撤销已经产生的副作用。

父任务完成前检查所有相关子任务：要么完成、明确解除依赖，要么报告阻塞。孤儿任务策略由用户选择：CLI 临时模式退出即取消；daemon 模式可继续运行且有清晰通知。两种模式不能混淆。

## 12 Coding agent 适配

### 12.1 职责分工

qi 管理请求、workspace、任务、授权、预算、日志和结果交付；coding agent 管理其内部代码推理和工具循环。qi 不通过“跳过所有批准”参数规避下游安全控制，也不能把下游工具调用假装成 qi 已逐项审查。

适配接口包含 `Probe/Start/Observe/Cancel/Collect`。Probe 检查安装路径、版本、支持的 machine-readable 输出与非交互行为；版本不在测试矩阵内，明确标记 experimental，必要时拒绝后台执行。CLI flags 随产品版本变化，必须针对固定版本测试，不凭历史记忆构造命令。

### 12.2 Workspace 与输出

workspace 由用户配置，模型只能选择已授权 workspace ID，不能提交任意路径。启动前记录 repo、branch、HEAD、工作区 dirty 状态和授权边界。默认保留用户未提交修改，禁止自动 reset/clean；多任务写同仓库使用 workspace lease 或用户批准的 worktree。

stdin/stdout/stderr、日志和退出码分别记录。优先解析结构化事件，纯文本作为降级模式，不用脆弱正则猜测“测试通过”。截断日志附 artifact reference 和截断标志。完成证据包括变更文件、diff、执行的测试命令与退出码、未运行的测试、构建产物，以及是否仍有后台进程。

编码 agent 退出码 0 只证明进程成功退出，不证明需求正确。qi 应校验 artifact 存在和结果结构；必要时运行事先授权的独立测试。不自动 commit、push、创建 PR 或合并，除非请求包含该动作。

### 12.3 取消与恢复

Unix 使用进程组并验证身份，先温和取消、等宽限期、再强制结束；Windows 使用对应进程/job 控制策略。取消后检查子进程和日志最终状态。远程 coding agent 若支持任务 ID，则保存 external ref 并恢复轮询；不支持时不得宣传断点续跑。

首版只选一个 coding agent 完成端到端验证，再增加第二个。Codex 与 Claude Code 适配器不能假设权限、会话恢复或输出 schema 一致。

## 13 渠道与本地 API

### 13.1 渠道统一协议

InboundMessage 包含 `channel/account/conversation/message_id/actor_id/reply_to/attachments/received_at`。渠道适配器负责验签、重放防护、外部身份解析和入站去重；application service 决定权限和 session。

CLI 的本地用户身份不能泛化成“收到任意 webhook 就是用户本人”。群聊请求默认只访问该群授权的内容；个人记忆和其他渠道内容不得自动注入。跨渠道发结果需要明确绑定和用户选择，不凭同名用户匹配。

### 13.2 首版本地 HTTP API 草案

- `POST /v1/sessions`，创建会话。
- `POST /v1/sessions/{id}/messages`，带 `Idempotency-Key`，返回 message/run/task 引用。
- `GET /v1/tasks/{id}`，返回状态、进度、结果与阻塞。
- `POST /v1/tasks/{id}/cancel`，幂等取消请求。
- `GET /v1/approvals` 和 `POST /v1/approvals/{id}/decision`，携带 approval revision 与 actor 认证。
- `GET /v1/events?after={seq}`，SSE 增量事件。
- `GET /v1/artifacts/{id}`，权限检查后读取，不接受任意本地路径。
- `GET /v1/health` 与 `GET /v1/capabilities`，不泄露配置或 secret。

默认仅 Unix socket/受保护的本地 pipe，或 loopback 加高熵 token。监听 localhost 不等于没有攻击面；HTTP 需 Host/Origin 检查、CORS 默认拒绝、CSRF 防护、请求大小限制和 rate limit。远程监听是额外部署模式，要求 TLS、认证和运维文档，不能仅加 `0.0.0.0`。

### 13.3 事件契约

```json
{
  "schema_version": 1,
  "id": "evt_...",
  "seq": 123,
  "type": "task.state_changed",
  "occurred_at": "2026-11-04T12:00:00Z",
  "session_id": "ses_...",
  "run_id": "run_...",
  "task_id": "task_...",
  "causation_id": "cmd_...",
  "correlation_id": "run_...",
  "payload": {"from": "running", "to": "waiting_approval", "revision": 4}
}
```

稳定事件包括 `message.accepted`、`run.started`、`response.delta`、`response.completed`、`action.proposed`、`approval.requested/decided`、`action.completed`、`task.state_changed`、`artifact.created`、`budget.exhausted`、`run.completed`。

业务终态事件持久化；高频 token delta 可以是临时流，明确标注不保证回放，断线后用完整消息和快照恢复。SSE 至少一次传递，客户端按 event ID 去重；不要承诺网络端 exactly-once。游标超出保留范围返回 `cursor_expired` 和快照入口，而非静默丢失事件。

错误统一含 `code/message/retryable/details/correlation_id`，敏感参数脱敏。400 参数错误，401/403 认证授权，409 版本冲突，422 能力不支持，429 限额；业务 unknown 不是普通 HTTP 500 的同义词。

### 13.4 REPL 与 WebUI 的共同产品契约

REPL 提供多轮输入以及 `/tasks`、`/switch`、`/new`、`/cancel`、`/approvals`、`/model`、`/quit` 等明确命令；命令命名在实现前冻结。交互审批走同一服务端权限检查，不单独实现“终端特权路径”。Ctrl-C 首次取消当前生成或输入，再次按明确退出策略处理；退出客户端默认不终止 daemon 任务。

WebUI 采用嵌入 Go binary 的静态资源，后端提供版本化 API/SSE。每项顶层用户任务拥有独立标签页、会话、历史、上下文、工作目录、模型配置和授权 scope；task_id 是一切读写和事件订阅的隔离键。父子任务关系不自动授权读取其他任务。全局个人记忆、跨任务引用和共享 workspace 都必须显式选择，不通过全局 history 切片暗中共享。

任务在标签切换、刷新、断网或浏览器关闭后继续由 daemon 执行。重新连接从持久快照和 event cursor 恢复。关闭标签页仅隐藏界面，不取消任务；取消与归档是独立动作。服务端重启依第 11 节恢复，不能把“浏览器不在线”当作任务失败。

并发使用全局最大活动任务数、供应商限流、workspace lease 和每任务预算。超过上限显示 queued，队列避免长任务永久饿死短任务。两个写同一 workspace 的任务默认串行，独立 worktree 需要明确授权；每个任务的工具权限不能被另一个任务的批准扩展。多个标签可同时等待不同审批，回复必须通过 card/task/action ID 定位，不能按“最近一个问题”猜测。

桌面标签栏显示任务名、状态和未读结果，移动端改为可切换任务列表；消息区、输入区、状态和卡片布局响应式。支持键盘操作、可辨识焦点、屏幕阅读器标签、窄屏和大字体。不要把原始 terminal ANSI 输出直接注入 DOM。

### 13.5 跨渠道卡片模型

采用 channel-neutral typed card，首版 WebUI 渲染，未来 Feishu adapter 映射能力子集。借鉴交互模式，不承诺复制飞书组件或全部能力。首批类型为 `task_progress`、`deliverable`、`question`、`choice`、`form`、`approval`、`error`。文本、状态、字段、选项、artifact 和动作是结构化数据，不接受任意 HTML/JavaScript。

卡片保存 `card_id/task_id/type/schema_version/revision/state/payload/created_at/expires_at`；动作保存 `action_id/card_id/expected_revision/type/input_schema/authorization_scope/consumed_at`。数据库增加 `cards`、`card_actions`、`interaction_submissions`，提交唯一键用于业务去重。卡片只是既有任务/审批的视图，不再创建一套可独立批准的状态。

客户端提交只包含 card/action ID、expected revision、字段值及 idempotency key。服务端重新读取目标、验证用户身份、任务 scope、schema、有效期和状态，通过 CAS 原子处理，不能相信客户端提供的金额、权限、收件人或 hidden fields。重复点击返回原结果，过时卡片提示刷新，过期按钮失效。批准卡片复用 approvals/action hash，不把按钮事件当作无条件授权。

交付物卡片包含标题、说明、媒体类型、大小、生成任务和安全下载地址。下载每次授权，禁止目录穿越和可猜 ID 越权；文件名转义，危险内容以下载方式提供，HTML 预览使用隔离 sandbox/CSP，不能在主站 origin 运行 artifact 脚本。分享链接默认关闭，需授权后建立独立带期限的分享能力。

飞书等平台回调由渠道 adapter 验证签名/身份/重放，快速确认收件，再异步执行业务；官方回调时限应按实际接口核查，研究 FAQ 给出的约 3 秒目标不能作为执行长任务的时间预算。分别实现外部 delivery ID 去重和业务 action 幂等。渠道不支持某组件时降级到文本/链接或受支持表单，不能默默丢掉审批信息。WebUI 与渠道卡片共享业务状态，任何一侧批准后另一侧应更新为已处理。

### 13.6 公网与 VPS 调试部署

公网认证 WebUI 是已确认需求；此前“远程以后再说”的默认建议由此更新。开发阶段本地绑定，公开预览必须通过本节安全门槛。计划采用已有 Caddy 作为 HTTPS reverse proxy，qi 监听 loopback 上配置端口；应用后端端口不对公网开放。实际域名、地址、用户名、SSH 信息、私钥、token、密码、Caddy 实例配置、环境文件、数据库和运行日志都存仓库之外，Git 只提供不含真实目标的通用模板。

身份方案第一版选择明确的单用户登录，不开放注册。首次管理员引导通过服务端本地命令/一次性短期 token 完成，不在公网提供无保护的“第一个访问者即管理员”。密码采用成熟库的强哈希方案；登录失败 rate limit、延迟与告警，不能把明文密码作为持久 cookie。会话使用高熵随机 token、服务端记录、轮换、过期、注销与撤销；cookie 设置 Secure、HttpOnly、SameSite，并对所有修改请求做 CSRF 与 Origin 校验。

API、SSE、artifact、卡片回调/提交和调试入口全部认证授权；不要只保护首页。SSE 建连与重连都验证 session，注销或权限撤销后关闭连接。Host 校验、可信代理 allowlist、正确处理 forwarded headers、CORS 默认拒绝、严格 CSP、请求与上传限额、错误脱敏均为默认配置。Caddy TLS 不能代替应用认证，应用认证也不能代替任务/文件级授权。

调试部署流程：构建可追溯版本 → 仓库外配置与 secrets → 单独低权限用户和受保护数据目录 → loopback 启动 → 健康检查 → Caddy 校验与安全 reload → 从公网验证 HTTPS、登录及未授权拒绝 → 检查日志/端口 → 记录回滚方式。不会自动覆盖现有 Caddy 配置或其他站点。远程连接、证书、端口或权限被阻塞时保留本地可运行结果并明确阻塞，不能声称已部署。

部署验收：匿名访问 API/events/artifacts 均失败；错误 Origin 的写请求失败；过期/撤销会话立即失效；同用户不能用 A 任务的 action ID 修改 B 任务；TLS 有效、HTTP 按配置重定向、后端端口外部不可达；仓库历史与构建日志 secret 扫描通过；重启后任务/卡片/审批恢复；Caddy reload 不影响已有站点；有备份、回滚与调试实例关闭步骤。

## 14 安全与威胁模型

### 14.1 需要保护的对象

用户凭据、会话与记忆、文件、外部账户、审批权、模型预算、插件供应链、身份绑定和任务审计。信任边界包括模型端点、MCP server、coding agent、渠道平台、数据库和本地文件系统。

### 14.2 主要威胁与控制

| 威胁 | 失败方式 | 必要控制与残余风险 |
| --- | --- | --- |
| Prompt injection | 文件/网页要求发密钥或改审批 | 来源分层、policy 独立、最少工具；不能保证模型永不受影响 |
| 恶意插件 | 启动即读环境和发网络 | 最小环境、显式启用、OS 隔离；无沙箱模式视为完全信任代码 |
| 工具描述投毒 | 自称只读实则删除 | host 风险配置、版本快照、验证与审计 |
| 审批替换 | 批准 A 后执行 B | canonical hash、目标绑定、revision CAS、执行前复核 |
| 崩溃重复执行 | 转账/发信超时后重试 | 幂等键、结果查询、unknown reconciliation |
| 路径逃逸 | symlink/TOCTOU 越出 workspace | OS 文件句柄约束；路径字符串检查不足 |
| SSRF 与本地端点滥用 | 请求内网、metadata 或任意 localhost | endpoint allowlist、重定向/DNS 重绑定检查、受控代理 |
| Secret 泄露 | 日志、模型上下文、child env | secret references、脱敏、最小注入、数据目标策略 |
| 渠道伪造 | 伪造 user ID/重放 webhook | 验签、时间窗、nonce/inbox 去重、可信身份绑定 |
| 资源耗尽 | 无限循环、大输出、fork bomb | 预算、超时、大小限制、OS 资源限制、并发上限 |
| 供应链更新 | 相同名称变成恶意版本 | 固定摘要、更新审批、来源/许可证记录 |
| 过度记忆 | 跨项目泄露或保留过时敏感数据 | scope、来源、有效期、删除传播与用户控制 |

### 14.3 凭据与隐私

优先 OS keychain/credential manager，headless 可用受保护 secret provider。配置和 SQLite 存 reference 而非明文 key；不把 secret 放 CLI 参数或模型消息。日志脱敏有误差，因此减少原始内容记录比依赖正则更重要。

遥测默认关闭或仅本地，明确区分操作指标与消息内容。用户可导出和删除数据；删除完成前说明备份、远端供应商和渠道平台的保留边界。不要承诺本地删除会撤回已发送给外部服务的数据。

## 15 兼容性与工程质量

### 15.1 兼容性承诺

qi 使用自己的版本与配置 schema。`0.x` 允许有文档的破坏性修改，但迁移和拒绝旧写入不能省略。稳定 API 用 `/v1` 和事件 schema_version；新增可选字段向前兼容，修改已有字段语义需要新版本。

不承诺 Go API、Pi 扩展 ABI、Pi 会话文件、第三方 skill 全兼容。每个兼容声明对应 fixture、版本范围、已知差异和降级行为。迁移当前 `gopi` module 名到正式路径需要先确认仓库位置，不能仅为产品更名盲目改 import。

### 15.2 操作与诊断

提供 `qi doctor` 检查配置、权限、数据库、模型能力、MCP 启动和 coding CLI 版本；默认不发送私密会话。`qi status/tasks/approvals` 查询真实持久状态。结构化日志带 task/run/correlation ID，不默认记录完整 prompt 或参数。

指标至少包括请求延迟、模型首 token/总耗时、usage 与估计费用、工具失败率、审批等待、任务恢复次数、unknown 动作数、outbox 延迟、SQLite 锁等待和磁盘用量。observability 的错误不能导致业务执行被当成失败而重跑副作用。

### 15.3 测试策略

单元测试覆盖状态机、授权、路由和适配器转换；契约测试使用模型/MCP/coding fixtures；集成测试用真实 SQLite 和可控子进程；真实供应商 smoke test 单独显式开启，不把生产 secret 放 CI。

执行 `go test ./...`、`go test -race ./...`、`go vet ./...`，并加入 fuzz 测试处理 JSON、MCP 帧、工具参数、路径与事件重放。CI 覆盖 Linux/macOS/Windows 关键差异；若某平台尚未通过，不列为正式支持。

## 16 实施路线与退出门槛

### 阶段 0 核实基线并冻结契约

工作：确认远端与本地差异；跑现有测试；记录真实失败；确定目标 Go 版本、首个模型和 coding agent；新增 ADR；将本文分解为 issue；隔离未经验证 SQLite 草稿。

退出门槛：提交、文件和测试证据可复现；README 不夸大能力；无“实现中”被写成“已发布”。

### 阶段 1 会话和事件持久化

工作：迁移框架、追加消息、run 状态、artifact、事件序列、session 单写者、CLI list/resume、数据库备份恢复；复用 SQLite 草稿中有效代码而非直接照搬全量 Save。

退出门槛：重启后消息和工具关联完整；并发写不丢失；故障注入不产生半条业务状态；旧 schema 导入有测试；大历史不需要每轮重写全部记录。

### 阶段 2 安全动作与持久任务

工作：动作/审批/attempt schema、policy、任务 supervisor、lease/fencing、outbox、取消、unknown reconciliation、预算；替换全局 `yes-tools` 的粗粒度授权。

退出门槛：崩溃恢复测试覆盖每个关键窗口；任何副作用都可追溯授权；不确定结果不会自动重发；撤销立即影响后续 dispatch。

### 阶段 3 MCP 与 Coding 闭环

工作：插件最小环境、健康检查、版本注册；一个 coding adapter 的 Probe/Start/Observe/Cancel/Collect；workspace lease；结构化结果；父子任务。

退出门槛：真实已固定版本的 coding CLI 完成一个测试仓库修改与测试；取消无孤儿进程；插件崩溃不使 daemon 崩溃；受限制条件不会被 worker 入口绕过。

### 阶段 4 上下文与多模型

工作：流式模型事件、第二模型适配、预算可见性、能力矩阵、context builder、可逆压缩、FTS5 记忆、skill 选择。

退出门槛：长对话压缩不丢授权约束；模型切换不会误发数据；工具对关联完整；golden corpus 评估优于未压缩基线的成本/可用性且不损害关键正确性。

### 阶段 5 产品化和渠道

工作：公网 WebUI 强化、一个真实消息渠道、身份绑定、入站去重、可靠投递、打包升级、文档和安全审计。REPL 与基础 WebUI 在阶段 1 即并行交付，任务标签页与卡片在阶段 2 交付，不能拖到此阶段。

退出门槛：端到端身份与审批测试、升级/降级和备份恢复演练通过；受支持平台矩阵发布；可在干净环境按文档安装。

每个阶段先交付纵向可运行闭环，再扩展同类适配器。暂不承诺工期；应在阶段 0 后根据测试债务和可用人力估算。

## 17 详细验收矩阵

| ID | 场景与输入 | 通过标准 |
| --- | --- | --- |
| R01 | 普通知识问答 | 不启动 MCP/worker；无工具 schema 外发；直接响应 |
| R02 | 明确要求只解释代码 | 未授权执行 shell/编辑文件；需要读文件时独立评估 |
| R03 | 分类器建议 coding 但用户禁用 coding | 保持禁用，解释或询问，不扩大权限 |
| M01 | 流式工具参数中途断线 | 工具调用次数为 0，消息标为 partial |
| M02 | 429 后恢复、401、工具 schema 不支持 | 仅可重试类别重试；退避/上限正确；错误可辨识 |
| M03 | 切换到未批准模型端点 | 请求被阻止，不泄露现有 transcript |
| D01 | 用户消息提交事务任一写入点崩溃 | 恢复后完整接受一次或完全未接受，无半任务 |
| D02 | 同 session 两次并发运行 | 一次成功、一条排队/冲突；无 history 覆盖 |
| D03 | 数据库与 WAL 正在写入时备份 | 通过 backup API 得到可恢复、一致快照 |
| A01 | 用户批准后修改收件人/路径/金额 | action hash 不匹配，禁止 dispatch，重新审批 |
| A02 | pending approval 后重启 | 原请求可见，未自动批准，超时后不可使用 |
| A03 | grant 撤销与准备执行竞态 | 以定义的 dispatch 线性化边界判定，撤销后不开始新动作 |
| A04 | 两客户端重复批准 | 仅一次消费，另一返回已决定状态，无双重执行 |
| T01 | 写 intent 后、外部调用前崩溃 | 能区分安全重试或进入核对，不假设未调用 |
| T02 | 外部成功后、结果落库前崩溃 | 有查询时补录成功；无查询时 unknown，不重发 |
| T03 | 租约到期但旧 worker 未停止 | fencing 阻止旧 worker 写回；外部调用不由 fencing 虚构去重 |
| T04 | cancel 正在运行子任务 | 有取消请求及最终状态；已完成副作用不显示已撤销 |
| P01 | 插件输出巨大帧/非法 JSON/乱序 ID | 限额与协议错误生效；daemon 存活；日志受限 |
| P02 | 插件 env 请求全部继承 | 未列入 allowlist 的 secret 不可见 |
| P03 | 插件描述要求读取凭据并发送 | 不升级权限；产生拒绝/澄清而非外发 |
| P04 | 工具列表在审批后变化 | revision 失效，旧批准不执行新工具 |
| C01 | coding 请求任意目录 | 仅接受授权 workspace ID，路径替换被拒绝 |
| C02 | dirty 工作区和并发编码 | 保留原修改；workspace 锁防冲突；不自动 reset |
| C03 | coding CLI 输出成功文字但退出非零 | 任务非成功，保留错误与日志证据 |
| C04 | 超时、Ctrl-C、daemon 异常重启 | 进程身份检查、取消/恢复按平台契约执行，无虚假续跑 |
| X01 | 压缩前历史含禁止发送及未决工具 | 数据库约束仍生效，工具对与审批绑定完整 |
| X02 | 摘要模型生成错误 | 可追溯原文，可回退，不覆盖 transcript |
| X03 | 用户删除一条 durable memory | 所有检索索引不再命中；备份边界明确 |
| H01 | webhook 重放与同名陌生人 | 去重且不映射 owner；无法批准或访问个人会话 |
| H02 | SSE 断线后携带旧游标 | 重放并去重；超保留范围明确返回快照恢复 |
| H03 | 外部消息发送成功但 ACK 丢失 | 幂等/查询恢复；否则 uncertain，禁止无限补发 |
| S01 | symlink/路径 traversal/SSRF 测试 | 拒绝越界；平台无法保证的风险明确记录 |
| S02 | 日志、事件、诊断导出扫描 | 不含已知 secret canary，敏感参数按规则脱敏 |
| B01 | 模型和工具预算耗尽 | 无新外部调用；状态可恢复；不会隐式增加预算 |
| V01 | 从上一版本升级、备份恢复、较新 schema | 数据一致；旧程序拒绝不安全写入；版本可诊断 |

这些验收应成为自动化测试或明确的手工测试记录。尚未自动化的安全项不能仅用代码 review 打勾。

### 17.1 新增界面与并发验收

- UI01：同时运行三个独立任务，模型消息、文件、工具权限和日志互不串用；一个任务取消不影响另外两个。
- UI02：任务达到并发上限时进入可见队列，释放资源后继续；同 workspace 写任务不会并发破坏用户文件。
- UI03：切标签、刷新、关闭浏览器和短暂断网均不停止后台任务；重连恢复正确标签、卡片版本、历史和审批。
- UI04：两个任务各有一个待答问题；在任一标签回答只能推进对应任务。重复/过期卡片提交不会推进错误步骤。
- UI05：320px 窄屏、窗口调整、键盘操作和长中文内容不遮挡输入或确认按钮；移动端可找到所有任务和未读结果。
- UI06：交付物文件名含 HTML、日志含 ANSI/脚本、artifact 为恶意 HTML 时不能执行主站脚本或读取会话。
- UI07：WebUI 和 REPL 同时操作同一任务，以服务端 revision 确定唯一状态，不出现两次审批/执行。
- UI08：REPL Ctrl-C 在输入、审批、模型流和 coding worker 四种状态可预测响应；partial history 保存。
- UI09：跨任务共享记忆/文件未开启时不能检索其他任务上下文；显式共享有来源和范围。
- UI10：公网匿名、CSRF、伪造 Host、过期 session、暴力登录和 artifact 越权测试全部通过后方可开放预览。
- UI11：飞书模拟重复 callback、迟到 callback、旧卡 revision 和非法 actor，确认快速 ACK 与后台执行分离且业务仅生效一次。

## 18 风险与需要确认的决策

### 18.1 实际工程风险

- 任务可靠性比模型循环更难。主要成本在崩溃窗口、重复副作用和外部系统语义，而不是增加一个 goroutine。
- 泛化多供应商很容易产生最小公分母接口；需保留 capability 和 provider-specific 扩展，避免过早统一。
- 支持任意插件与严格安全存在冲突。必须明确 trusted-process 模式和实际 sandbox 模式，不把安全承诺交给 manifest。
- coding CLI 不是永久稳定 API。固定版本、契约测试和 machine-readable 输出是维护成本，不是一次性接入。
- 长期记忆可能比不记忆更危险。来源、范围、失效和纠错比向量检索优先。
- Go 与 SQLite 依赖影响二进制大小、构建时间及平台支持。纯 Go driver 便于避免 CGO，不等于所有目标平台自动可用。
- 多渠道会显著扩大认证和隐私边界，应在单机任务恢复稳定后再做。

### 18.2 已确定方向与可采用的实施默认值

1. 已确定 REPL + WebUI，不做完整 TUI 首版；daemon 承担后台持久任务。用户明确要求公网认证访问。
2. 第一模型端点及数据发送范围是什么？推荐一个真实生产端点加测试 fixture，而非同时接所有厂商。
3. 第一 coding agent 选哪一个及版本？推荐按用户现有安装选择，先只承诺一个。
4. 是否接受默认本地保存 transcript？需要提供 `--ephemeral` 和明确的保留/删除策略。
5. 目标系统优先级是什么？推荐先验证用户实际系统，再发布跨平台承诺。
6. “受信插件”是否可接受第一版不沙箱？若不可接受，OS/container sandbox 必须成为首版阻塞项，而非文档注脚。

用户希望自主推进，无需等待一般实现偏好。未指定项采用最小权限、可替换适配器、用户实际系统优先等默认值继续开发；缺少真实凭据、部署权限或外部授权时，仅暂停依赖该条件的动作，不暂停本地开发。真实安全限制未经验证前不能宣称上线可用。

## 19 参考资料与核实规则

以下均为项目或规范的一手入口，用于后续实现核实。本方案没有把未核实的 Pi 具体实现细节当作论据；当前版本、字段和支持矩阵应在实现时固定到具体 tag/commit 或协议日期。动态页面可能变化，链接存在不等于所有版本都支持文中目标能力。

- Pi 固定研究基线为 v1.5.0。Agent 一手资料：https://github.com/earendil-works/pi/blob/v1.5.0/packages/agent/README.md 。会话、扩展、MCP 与安全资料分别为同 tag 的 `packages/coding-agent/docs/sessions.md`、`extensions.md`、`mcp.md`、`security.md`。研究结论用于架构借鉴，不承诺接口或行为兼容。当前 Pi 支持 MCP，不能沿用旧资料中“没有 MCP”的说法。
- MCP 官方规范：https://modelcontextprotocol.io/specification 。实现时固定协议版本，重点核查 lifecycle、transports、tools 和 security。
- MCP Go SDK：https://github.com/modelcontextprotocol/go-sdk 。评估是否替换手写协议，先检查许可证、版本和实际能力。
- OpenAI API 文档：https://platform.openai.com/docs/api-reference 。核对流式、工具调用、错误和 usage。
- Anthropic 官方文档：https://docs.anthropic.com/ 。核对 Messages、tool use、streaming 和模型限制。
- Codex 官方文档：https://developers.openai.com/codex/ 。核对固定版本 CLI、非交互、权限和结构化输出。
- Claude Code 官方文档：https://code.claude.com/docs/ 。核对 CLI、权限、输出格式与会话恢复。
- SQLite WAL：https://www.sqlite.org/wal.html 。核对并发、文件和部署约束。
- SQLite 事务：https://www.sqlite.org/lang_transaction.html 。核对事务和锁语义。
- SQLite 在线备份：https://www.sqlite.org/backup.html 。指导一致备份与恢复。
- SQLite 外键：https://www.sqlite.org/foreignkeys.html 。核对连接设置和约束。
- Go race detector：https://go.dev/doc/articles/race_detector 。用于并发测试。
- Go fuzzing：https://go.dev/doc/security/fuzz/ 。用于协议与输入边界测试。

实施者必须为重要兼容结论补充“固定版本、已读章节、测试 fixture、验证日期”。任何新增来自 Pi 的具体事实，都需要一手源码或文档支持；对比研究结论和 qi 的自主设计必须保持分离。

- 飞书官方回调接收与处理：https://open.feishu.cn/document/event-subscription-guide/callback-subscription/receive-and-handle-callbacks 。用于核对认证、回调时限和异步处理。
- 飞书 CLI 官方卡片动作参考：https://github.com/larksuite/cli/blob/main/skills/lark-im/references/lark-im-card-action-reply.md 。适配时仍需核对实际平台接口版本。

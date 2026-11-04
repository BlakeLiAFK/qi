# qi 实现与验证状态台账

审阅日期：2026-11-04（UTC）。本文件是当前源码快照的状态说明，不是发布公告、部署验收或安全认证。

## 1. 证据口径

- **已实现**：当前源码存在对应路径，且 README/专项文档描述其边界；不等于真实外部服务已验证。
- **本地测试通过**：明确记录实际运行结果。HTTP fixture、模拟 CLI、假运行时、模拟 DOM 与真实集成分别列出。
- **未验证／阻塞**：缺少真实环境或证据，不能写成通过。
- **计划／原型**：设计目标或接口原型，不计为可用生产能力。

最新已验证的远端源码提交为 **`1dc7527aa4e1ace854c23cfe3278b316f6708a04`**。协调者在本次更新交接中确认：已独立获取该精确提交的远端快照，**195/195 个 blob 全部核对一致**，并在该快照上完成构建与测试，结果见第 3 节。这修正了初版台账中仅有本地无 Git 元数据快照、无法绑定提交的证据缺口。台账编写者依据该验证交接更新记录，没有自行重复下载或重新运行整套检查。

此前远端 **`bfbeae7`** 因缺少 `SelectBounded` 而发生真实编译失败；不能用当时本地工作区的测试通过代替远端可构建性。该缺漏已修复，并在上述精确远端提交上重新验证通过。**源码完整性、构建与本地测试通过不代表已经部署或真实外部集成通过。** 本文件的后续文档提交不包含在上述已验证源码提交中。

[设计方案](QI_DESIGN_PLAN.md) 的第 2 节记录的是早期历史基线，其中“存储未接入”“没有长期记忆”等措辞不能作为当前实现状态。方案中的验收矩阵仍是目标，不应整表视为已通过。当前边界优先参考源码、README 和专项文档。

## 2. 当前已实现的能力与限制

| 能力 | 当前实现与证据 | 不应推导出的能力 |
| --- | --- | --- |
| CLI 与 daemon 共用任务 | `cmd/qi/main.go`、`server.go`、`client.go`；独立 CLI、`serve`、`attach` 已存在。`attach` 通过 HTTP 使用 daemon 任务 | 独立 CLI 仍仅内存会话；退出会取消其 worker。`attach` 断开不取消 daemon 工作，不代表终端自动持续接收更新 |
| 模型与工具循环 | `internal/agent`、`internal/provider/openai.go`、`anthropic.go`；OpenAI-compatible Chat Completions 与原生 Anthropic Messages 文本/客户端工具适配 | 未验证真实供应商；不等于支持所有兼容端点、OpenAI Responses 或 Anthropic thinking/server-side tools |
| 模型流与进度 | `internal/provider/stream.go`、`internal/tasks/live_text.go`、`internal/webui/events.go`；持久事件 SSE 与可选 OpenAI 文本预览 | Anthropic streaming 未实现，启用会拒绝；SSE 不是 exactly-once 投递或副作用重放 |
| 持久任务与并发 | `internal/session/store.go`、`queue_store.go`、`internal/tasks/engine.go`、`queue.go`；SQLite WAL、单 engine 所有权、独立历史、每任务一个 outstanding turn、有界 FIFO 队列 | 重启将未完成／排队工作标记 interrupted，不自动续跑；不提供分布式调度或跨 daemon workspace 协调 |
| 幂等提交与创建 | `internal/session/idempotency.go`、`task_creation.go`，对应 tasks/webui 实现；接受记录与输入/run/队列事务绑定 | 只保证接受阶段去重，不保证外部工具 exactly-once；无 key 的旧入口不能安全自动重试 |
| 审批与提问卡 | `internal/tasks/approvals.go`、`questions.go`、`question_continuation.go`；精确动作审批与显式单次 Continue task | 回答问题不是执行授权；取消、崩溃或超时不撤销已经发生的副作用，unknown 需人工核对 |
| 归档与恢复 | `internal/session/archive.go`、`internal/tasks/archive.go`、`internal/webui/archive.go` | 归档不是删除或释放配额；活动任务不能归档 |
| 文件交付物 | `internal/artifacts`、`internal/tasks/artifacts.go`、`internal/webui/artifacts.go`；显式根目录、任务范围、hash 校验、授权注册与认证下载 | 没有自动导入 worker 输出、上传 UI 或外部发送；注册不复制／保留历史文件字节；同 UID 恶意进程不是可隔离的威胁边界 |
| Skills 与上下文预算 | `internal/skills`、`internal/contextbudget`；显式引用快照、不可信参考标记、按完整旧 turn 淘汰的字节预算 | 无自动发现／执行 skill；不是 token 预算或语义压缩；被淘汰旧用户 turn 中的限制也可能不再对模型可见 |
| 显式保存记忆 | `internal/session/memory_store.go`、`turn_memory.go`、`internal/tasks/turn_memory.go`、`internal/webui/memory.go`；认证 CRUD、来源/版本/范围、逐轮勾选和精确模型目的地确认 | 不自动提取或跨任务召回，无 embedding／FTS；删除源记录不删除已接受 run 快照、普通对话引用或备份 |
| 受信本地 coding/MCP | `internal/workers`、`internal/plugins/scoped.go`、`internal/tasks/coding.go`、`mcp.go`；显式启用、动作审批、环境过滤、临时 home、Unix 进程组清理、有限头尾输出 | 不是 OS 沙箱；同 UID 文件、进程、网络仍有风险；真实 MCP 服务与 coding 工作闭环未通过验收 |
| 单用户 WebUI 认证 | `internal/webui/server.go`、`auth.go`、`cmd/qi/public_preview_test.go`；本地 Basic Auth，独立 public-preview HTTPS session 登录/退出、Host/Origin/CSRF 检查 | 不代表多用户权限、TLS/proxy 已部署或公网安全验收通过；公网预览明确拒绝 workers 与 MCP |
| 存储隐私 | `internal/session/privacy_unix.go`、`privacy_other.go`；Unix 私有目录/文件、symlink/hardlink/祖先检查、失败关闭 | 数据仍是明文；不是加密或同 UID 隔离；Windows 原生 ACL 校验未实现，持久存储不支持 |

## 3. 实际测试证据

### 最新精确远端快照验证

以下结果来自协调者于 2026-11-04 的最新验证交接，目标均为 `1dc7527aa4e1ace854c23cfe3278b316f6708a04` 的独立远端快照：

- 远端内容核对：**195/195 blobs 一致**。
- 干净构建：`go build` **通过**。
- 不使用测试结果缓存的全包检查：`go test ./... -count=1` **通过**。
- JavaScript DOM 契约测试：**52/52 通过**。
- 部署模板检查器 `deploy/check_templates.py`：**通过**。这是模板检查，不是目标主机部署、服务启动、Caddy/TLS 实机验收或公网连通性验证。

### 历史检查，不能代替最新提交的验证

- **18:25 UTC**：协调者报告旧本地快照 `go test -race ./...`、`go vet ./...` 退出 0，DOM 测试 47/47 通过。这些 race/vet 结果**未绑定到最新远端提交**，不在此冒充该提交的新运行结果。
- **18:32 UTC**：台账编写者独立执行 `node --test internal/webui/app_test.cjs internal/webui/memory_test.cjs`，旧快照 47/47 通过、0 失败、0 跳过，退出 0。最新计数已增加为 52，不应继续将 47 作为当前套件数量。
- 初次默认 `go test ./...` 返回 `Go: Unknown option: test` 是 PATH 中工具不符，未运行 Go 测试。它与后来 `bfbeae7` 缺少 `SelectBounded` 的**真实源码编译失败是不同问题**；后者已经通过补齐代码和精确远端快照重验修复。

DOM 测试使用模拟 DOM/浏览器 API，不是实际浏览器截图或真实网络验收。
### 测试证明的范围

代码中存在模型 HTTP fixture、SQLite 事务/恢复、审批并发、任务/问题/记忆幂等、认证与输入边界、模拟 CLI 进程、MCP fixture、输出和取消测试。Go 全包通过不能自动证明任何真实外部 provider、CLI 登录、MCP 服务或生产部署正常。隔离包测试只使用假 runtime，不能证明容器、账号、namespace、cgroup、磁盘配额或网络隔离真实生效。

没有在此台账中记录代码覆盖率百分比、完整设计验收覆盖率或跨平台运行通过率，因为没有对应测量证据。

### Coding CLI 的有限真实检查

[独立验证记录](coding-cli-validation.md) 记载实际 Codex 版本为 `codex-cli 0.166.0-alpha.1`，以下 **help-only** 命令退出 0：

```sh
codex --ask-for-approval never exec --sandbox workspace-write --help -- 'contract-check-only'
```

这只支持该已安装版本接受参数形式，未触发 agent 执行；不能证明认证、模型访问、sandbox、文件修改或最终交付成功。Claude Code 没有找到可执行文件，只有官方文档核对，没有真实运行。README 中笼统的“installed-version compatibility 未验证”应按此处的有限 parser 检查细化理解，不应升级为集成通过。

## 4. 尚未解除的阻塞与未验证项

1. **部署与远端访问**：协调者交接记录 SSH 网络/DNS 阻塞。台账工作未重试连接，也没有部署成功、远端服务启动、TLS 或公网访问证据。具体主机和认证资料不进入此文件。
2. **真实浏览器截图与视觉 QA**：协调者交接记录浏览器 socket 阻塞。52 项 DOM 契约测试不能替代桌面/移动端真实浏览器操作、布局验收或截图。当前不能声称已获得真实截图。
3. **真实模型和 coding 闭环**：无 live provider 验证；无真实 Codex 文件修改与测试闭环；无 Claude Code 执行验证。需在合适权限、凭据和隔离测试环境下单独验收。
4. **公网执行能力**：public-preview 禁用 workers/MCP 是当前明确安全限制，不是可绕过的配置缺省。没有已验证 OS 隔离前不得开放这些入口。
5. **隔离后端**：`internal/isolation` 只有策略/协议/生命周期 supervisor 与 read-only preflight。无生产 runtime adapter、独立 runner 认证传输、模型 gateway 或重启清理闭环。`qi isolation-preflight` 当前始终不能认证隔离就绪，不能作为启用 worker 的许可。
6. **Windows 与平台矩阵**：Windows 持久数据库打开失败关闭；交叉编译即使成功也不等于运行支持。未在此台账中主张完整 Linux/macOS/BSD 实机矩阵通过。

## 5. 仍属于计划的工作与完成门槛

- **安全隔离生产化**：实现独立身份 runner、固定镜像和真实约束检查、受控模型访问、崩溃/孤儿清理；在目标系统通过文件/进程/网络/资源耗尽/跨任务攻击验证后，才能重新评估公网执行。
- **真实纵向验收**：固定 provider 和 CLI 版本，在隔离测试仓库完成一项修改、运行测试、收集交付物、取消和故障恢复；保留实际命令、状态和结果。
- **实际公网与 UI 验收**：解除访问与浏览器阻塞后，验证外部 TLS、proxy、登录/退出、失效 session、审批重放、刷新/断网、窄屏与键盘使用；只记录实际观察结果。
- **设计中的后续产品范围**：自动重试、语义压缩、自动记忆/搜索、定时任务、消息渠道与可靠 outbox、更多供应商协议、完整 TUI 等仍不能由当前代码推导为已交付。详见设计方案，按独立验收门槛推进。

## 6. 审阅结论

当前是功能较完整的**本地优先、单用户早期实现**，具有持久任务、受控提交、审批、显式记忆和受限 WebUI。现有本地测试支持其内部契约，但仍不能称为已部署产品、真实 provider/coding 集成已验收，或可运行不可信代码的生产沙箱。

本次台账更新只修改本文件；未修改应用代码、推送提交或执行部署。提交由维护者另行处理。

# CPA-BPS 接入与验证记录

初次记录：2026-11-15；续接和断流修复：2026-11-16。基线：`88c75c579`（`production`）；代码交付分支：`codex/cpa-bps-channel`。本记录的验证不包含发布或生产部署。

## 与原内置 BPS 的区别

| 项目 | 原内置 Excel/BPS | 新 CPA-BPS 模式 |
| --- | --- | --- |
| 执行链路 | Sub2API 内置 Go BPS 适配 | Sub2API → 原 Python 兼容代理 → CPA-BPS 0.5.31 |
| OAuth 与账号池 | Sub2API 账号 | CPA 独立维护；Sub2API 只保存 `client_api` |
| 工具与历史处理 | 内置转换与清洗 | 原始请求交给原包；Sub2API 只映射模型、隔离会话 |
| compact | 内置 BPS 处理 | 原路径 `/v1/responses/compact`；不支持时返回 CPA 错误 |
| 请求失败 | 原有调度策略 | 已向 CPA 发送后不重试、不换号、不回退 |
| 用量 | 对应 Sub2API OAuth 账号 | 整个 CPA 实例，不代表池内某个 OAuth 账号 |
| WebSocket / Chat / 独立图片 | 按原通道能力 | CPA 首版拒绝；内联图片原样转交，不承诺插件权限 |

为避免 CPA 返回内容前混入本地 compact 心跳，compact 心跳在选定非 CPA 账号后才启动。其他通道仍使用原心跳机制。

新模式使用 OpenAI / API Key 账号的 `extra.openai_cpa_bps`；复用 `base_url` 和 `api_key`，协议固定 Responses，无数据库迁移。创建、全量编辑、extra 合并、批量编辑均校验冲突。编辑时保留已脱敏的现有密钥。

管理员模型查询失败时返回错误，空列表保持为空。专用 CPA 分组的普通模型接口和 Codex 模型目录都使用 CPA 实际返回值，不能由本地映射或默认模型填充。混合分组仍沿用其原有目录语义，因此切流和回退使用独立分组；需要固定特定实例时，使用现有分组模型来源设置。

## 2026-11-16 续接与失败统计修复

评估发现两个缺口：成功返回的 CPA `response.id` 没有登记归属及账号绑定，下一轮携带 `previous_response_id` 被入口拒绝；已经输出 HTTP 200 的 SSE 若缺少终态，内部虽然返回错误，Ops 仍可能保存为 200 的已恢复错误。

修复后，成功的普通 Responses 使用现有绑定机制记录用户、API Key、分组和 CPA 实例；JSON、SSE 和上游 SSE 转非流式 JSON 共用该逻辑。失败、未完成和 compaction 对象不会登记续接权限。原有跨用户、跨分组归属校验继续生效；这只修复 Sub2API 的续接准入与路由，不代表真实 CPA 已支持服务端续接。

公共入口测试进一步发现，补齐绑定后，普通透传的 native recovery 限制仍会把 CPA 续接拒绝为 409。现增加 CPA 专用分支，在完成原账号归属检查后允许响应 ID 续接；存在 native 加密历史归属或已重建历史时继续拒绝，普通透传限制保持原行为。

SSE 断流、半帧和失败终态增加内部失败标记，保留客户端原始 SSE 和实际上游 HTTP 状态。缺少终态的断流在 Ops 中按逻辑 502 计入失败率，不再作为已恢复的 200；已返回的实际用量继续沿用原失败结算规则。流中客户端取消登记为请求级 499，不归为 CPA 故障。

新增回归包含：两种调度模式下实际发送两轮请求，确认第二轮进入相同 CPA 实例、原工具回执与响应 ID 不变、其他用户被拒绝；通过真实 Ops 中间件检查断流记录的逻辑状态、上游 HTTP 200 证据和原 SSE 内容。修复前，公共入口测试稳定复现第二轮 400 和断流 Ops 状态 200；service 测试也复现绑定及失败标记缺失。

本次只修改后端及验证记录，前端与原 Python 代理保持既有实现。多实例模型目录仍只查询首个可调度实例；试点应采用一个独立分组对应一个 CPA 实例，或显式配置模型来源。

本次已通过的命令（在 `backend/` 执行，无 overlay、无真实上游）：

```sh
go test -mod=readonly -race ./internal/service -run '^Test(CPABPS|CopilotSDK|ExcelBPSProgress|OpenAIAgentMessage|HTTPResponseOwner)' -count=1 -timeout=180s
go test -mod=readonly -race -tags=unit ./internal/handler -run '^Test(CPABPS|OpenAIResponsesExcelBPS|OpsErrorLoggerMiddleware)' -count=1 -timeout=180s
go test -mod=readonly -race ./internal/service -run '^TestIssue(715|758|805)' -count=1 -timeout=180s
```

最后一项覆盖既有加密历史恢复、归属保留、交付隔离及路由策略回归。`git diff --check` 与修改 Go 文件的 `gofmt` 检查通过。

2026-11-16 分支交付前另从 Git 暂存区导出 `deploy/cpa-bps/`，核对所有标记为未修改的导入文件 SHA256，并在导出副本执行上述 Python 26 项离线测试，全部通过。目录内 `.gitattributes` 保留原脚本的 CRLF/LF 字节，避免 Git 换行转换导致重新检出后的来源校验失败。

再次尝试 `go test -mod=readonly -tags=unit ./internal/service -run '^TestCPABPS' -count=1 -timeout=180s`，仍被原有 `payment_recharge_bonus_test.go:222–233` 的 `affiliateRebateBaseAmount` 未定义阻断。没有修改或排除该文件；以上 service 回归使用未带 unit 标签、实际包含 CPA 用例的测试集。`golangci-lint` 当前仍不可用；未执行完整 `make test`，未重复前端检查或进行生产部署及真实推理。

## 已执行的验证

均为虚构数据、mock 或本地回环测试，没有生产数据库、真实 OAuth 或付费上游调用。

- CPA Go service 测试：账号类型/冲突、创建与编辑回读、敏感凭据保留、模型映射、会话隔离、路径与完整工具历史保留、成功/失败终态、200 内嵌错误、401/403/429/5xx、重定向、断流、取消及无进展超时；每次转发只发送一次。
- 公共 handler 测试：两种调度路径；普通 Responses 与 compact；孤立回执、交接上下文、完整工具回合不被公共入口改写；有其他候选账号时错误仍不换号；原错误体后不追加通用 SSE 错误。
- 跨语言离线测试：把公共 handler 实际发送的字节输入仓库中的原 Python `sanitize_body`；验证孤立回执/交接上下文由原代理删除，custom call 配对保持，CUA `code` 与 `completion_summary` 两种参数分支保留。未启动真实 CPA 二进制。
- DTO 测试：账号模式可回读，`api_key` 不出现在管理响应中，凭据存在状态仍返回。
- 模型测试：管理员、专用 CPA 分组、Codex 目录、空列表和失败，无默认列表替代；同时运行相关原有模型查询回归。
- Go race：CPA、Copilot 和 BPS progress 相关 service 用例通过。
- 前端：8 个 Vitest 文件、336 个测试通过；包括创建/编辑保存、冲突开关、模型查询、空列表、查询失败、过期异步结果、新功能提示及中英文键完整性。全量 ESLint、`vue-tsc --noEmit`、Vite 生产构建通过。
- Python 原包及网络整合离线测试（共 26 个；25 个全套通过后新增来源校验，并单独复跑 5 个整合测试全部通过）：回环端口、路径/清单校验、固定版本/镜像摘要、独立密钥配置、重复安装不覆盖、停止保留数据、共享网络仅连接 gateway、CUA schema、chunked/SSE、取消和并发连接回收通过。来源清单校验确保未修改导入文件与原包哈希一致。
- `git diff --check` 通过。原包代理 SHA256：`67eef71f47a4f038e00f066ec3e4cabefdfb011a5bca3775144209eeff3d3e8e`。

主要复现命令（Go 命令在 `backend/`，前端命令在 `frontend/`）：

```sh
go test -mod=readonly ./internal/service ./internal/handler/admin ./internal/handler/dto -run 'Test(CPABPS|CopilotSDK|.*CodexModels|.*ModelsList|.*AvailableModels)' -count=1 -timeout=180s
go test -mod=readonly -tags=unit ./internal/handler -run 'Test(CPABPS|.*Models|OpenAIResponses_FunctionCallOutput)' -count=1 -timeout=180s
go test -mod=readonly -race ./internal/service -run '^Test(CPABPS|CopilotSDK|ExcelBPSProgress)' -count=1 -timeout=180s
./node_modules/.bin/vue-tsc --noEmit
./node_modules/.bin/eslint . --ext .vue,.js,.jsx,.cjs,.mjs,.ts,.tsx,.cts,.mts
./node_modules/.bin/vitest run src/components/account/__tests__/CPABPSAccountSettings.spec.ts src/components/account/__tests__/CreateAccountModal.spec.ts src/components/account/__tests__/EditAccountModal.spec.ts src/components/account/__tests__/CreateAccountModal.autoBPS.spec.ts src/components/account/__tests__/EditAccountModal.autoBPS.spec.ts src/components/common/__tests__/NewFeatureBadge.spec.ts src/stores/__tests__/newFeatureNotices.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
./node_modules/.bin/vite build --outDir /private/tmp/cpa-bps-frontend-build
```

在 `deploy/cpa-bps/scripts/` 执行：

```sh
python3 -B -m unittest test_bootstrap test_sanitize_proxy test_proxy_capacity test_sub2api_network -v
```

扩展回归使用 `-tags=unit`，选择 `Test(CPABPS|CopilotSDK|.*ExcelBPS|.*OpenAI.*Compact|.*Passthrough|ManagedConnectionScheduling)`，覆盖 service、basispoints、handler、admin。service 的原有 `payment_recharge_bonus_test.go:222–233` 引用了不存在的 `affiliateRebateBaseAmount`，导致未经处理的 unit 测试包无法编译。为验证本次相关用例，使用临时 Go `-overlay` 将**仅该无关测试文件**替换为 `package service`；没有修改仓库中的该文件。扩展 service/basispoints 回归通过；handler/admin 另行运行通过。该结果不等同于完整 `make test` 通过，也不覆盖被排除的充值返利用例。

当前 pnpm 启动脚本报 `ERR_PNPM_VERIFY_DEPS_BEFORE_RUN`（现有依赖/override 配置同步问题），因此使用项目已经安装的本地二进制完成前端验证；没有迁移锁文件或更新依赖。构建仍报告既有 package.json 重复键及 bundle 大小提示。当前未安装 `golangci-lint`，未执行其检查。

## 新功能提示与 UI 证据

提示 ID：`cpa-bps-channel-initial-announcement`。展示入口：新建账号、编辑账号中的 CPA-BPS 设置，共用 `CPABPSAccountSettings`。点击任意入口的标签后同管理员其他入口同步隐藏，并在当前浏览器 localStorage 中持久化。新增 ID 不影响旧功能提示的关闭记录；相关 NewFeatureBadge/Store 测试覆盖这些行为。

浏览器检查使用本地 Vite 和无头 Chrome，挂载真实 CPA 设置组件、真实中英文文案和提示 store；接口返回虚构空列表。检查开关、说明、模型获取、空列表、提示关闭及刷新持久化，保存桌面和 390px 手机宽度截图。图片不提交源码仓库；创建 PR 时作为附件上传：

- `zh-disabled.png`：关闭模式。
- `zh-enabled-empty-models.png`：中文、已启用、实际空列表。
- `en-enabled.png`：英文已启用。
- `en-mobile.png`：英文手机宽度，无横向溢出。

本次截图目录：`/Users/ambi/.codex/visualizations/2026/11/15/01a1dd49-9c10-7da2-bf53-0180bde9c085/cpa-bps/`。这是独立组件预览的状态截图，不冒充生产管理页面或旧版本对比截图。完整创建/编辑表单的序列化交互由上述 Vitest 验证。

人工复核入口：管理员 → 账号 → 新建 OpenAI/API Key → 启用 CPA-BPS → 填兼容入口和 client_api → 获取模型 → 保存 → 重新打开编辑 → 测试已保存通道。连接测试按钮明确提示会发送真实模型请求；本次没有点击执行真实推理。

## 尚未完成的运行验收

- 本机 macOS ARM 无 Docker，不具备原 Linux amd64 插件运行条件。未执行 Linux 容器 native smoke、空账号启动、HTTP/2 实际协商及资源压力验收。
- 本次没有重新下载原 ZIP 或镜像。部署入口固定其提交/ZIP SHA256/镜像摘要，下载时校验；离线测试验证配置和篡改拒绝，不能替代实际下载与 Linux 启动。
- 未授权测试账号，未完成真实短文本、工具调用/回执、多轮、compact。不得据此宣称 CPA 已支持 compact 或某个具体模型。也未执行支付、邮件和真实付费上游测试。
- 未运行依赖 Docker/PostgreSQL/Redis 的完整集成测试、完整前端测试集或完整 `make test`。
- 验证过程中没有部署、切换生产服务或迁移现有 OAuth 账号；代码按独立分支交付，不向 `ranxi2001/sub2api` 创建 PR。

部署与切换步骤见 [CPA-BPS 部署说明](../deploy/cpa-bps/README.md)。Linux 验收应记录实际镜像摘要、空账号启动结果、模型列表，以及获准账号各项真实推理的 request ID/状态/实际用量；记录前先脱敏。回退使用原内置 BPS 分组并新建会话。

## Agent 披露

- Agent：Codex，参与本次对比、实现、测试、文档与 UI 验证，以及 2026-11-16 的代码评估、续接和断流修复。
- 模型：GPT-6（会话提供的标识；更具体的模型 ID 未公开，未知）。
- 推理强度：默认（未手动设置）。
- 本次没有使用子 Agent。用户提供了前一模型的工作摘要，已在其工作区结果上继续；前一模型完整 ID 和推理配置未知，不作推测。

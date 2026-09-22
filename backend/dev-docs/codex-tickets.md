# Codex 292/332 票据

系统设置 → 网关 → Codex 设置中的「292 打票」控制后台采集和业务注入。默认关闭；「无票时暂停账号」仍独立控制缺票时是否暂停目标模型的账号调度。

## 采集与复用

- 参考 `fenjue.py`，铸票请求携带 Responses Lite 头、`additional_tools` 工具声明、`parallel_tool_calls=false` 和 `reasoning.context=all_turns`，不携带旧票或 Cookie。
- 仅保存 HTTP 200、正常完成、响应模型与请求模型一致、state 形态和时间有效，并且同时返回 `__cflb`、`__oailb` 的响应。292 和 332 都可接受，不依赖可能缺失的订阅类型；312 不接受。长度本身不能证明模型质量。
- 按账号和模型成组保存 state、同次响应的两个 Cookie、铸票代理和返回模型。其他 Cookie 不会混入；管理接口和账号导出隐藏整组运行凭据。
- 业务请求通过铸票代理复用整组凭据。HTTP 使用按账号及代理隔离的可复用连接池；WebSocket 握手也使用该代理，连接池区分票据和 Cookie。
- Lite 转换保留用户指令、工具定义及推理配置，将 namespace 工具移入 `additional_tools`，关闭并行工具调用。显式 hosted 图片工具等不兼容 Lite 的请求，在关闭缺票拦截时使用原链路；开启缺票拦截时返回兼容性错误。
- 复用时与 fenjue 对齐：只要本次生成未按请求模型应答，当前配对即失效，由后台下轮重新采集；不会自动重放已执行的用户请求。具体包括响应模型与票据模型不一致（如请求 Astra 返回 Luna、同流模型冲突），以及上游自行讲完（completed/failed/incomplete 终态）却始终未声明模型的情形——fenjue 在 gen_model 为空时同样停止复用。客户端取消、超时、传输读错误与非 200 响应不携带模型信号，仅记录日志、不作废。迟到的旧请求不会使新票失效。

- 管理端可手动重打：`POST /api/v1/admin/accounts/:id/codex-tickets/refresh`（可选 `{"models":[...]}` 限定模型）立即清除采集冷却并同步探测，绕过采集范围但复用同一校验与 singleflight；账号编辑弹窗提供「立即重打」。

## 代理与升级

代理必须提供稳定出口或粘性会话。相同代理 URL 不保证相同出口 IP；每次 CONNECT 随机轮换出口的代理不适合此流程。HTTP 连接可复用，但连接关闭、并发请求或 WebSocket 新握手仍可能建立新连接。服务不操作本机 mihomo 选择器。

修改打票代理后，新票使用新代理；已有票在有效期内继续使用其铸票代理。无有效票且允许请求时，仍使用账号代理。票据默认最多缓存一小时，并按 state 签发时间提前 30 秒失效。

升级后，旧版仅保存 state 的缓存不会注入，需要等待后台重新采集。长时间保持的 WebSocket 在票据过期、替换、失效或切换模型后需要重新连接，以获取一致的新握手。

本地回归使用模拟上游验证协议、配对隔离、模型不匹配失效、代理选择和导出脱敏；真实模型返回及代理出口稳定性仍需在部署环境观测。

## 2026-09-22 与 fenjue 的一致性审查

结论：票据配对的核心规则一致，完整执行流程并不等同于 `fenjue --rotate`。本次审查以桌面 `fenjue.py` 的实际代码为准，而非仅依据使用说明。

| 检查项 | fenjue.py 实际行为 | 本项目行为与边界 |
| --- | --- | --- |
| 铸票信封 | Lite、additional_tools/noop、禁止并行、all_turns，不带旧票和 Cookie | 一致；模型列表可配置，默认还包括 Sol，脚本固定 Astra |
| 合格条件 | 292/332、首个 model=Astra、两个 Cookie；没有显式要求 mint_status=200 或完成事件 | 本项目更严格：200、完成事件、响应模型无冲突、state 编码与时效、完整 Cookie；拒绝重复/删除 Cookie |
| 配对复用 | reuse_burst 在同一个 HTTPSConnection 上复用同次换来的票和 Cookie，忽略后续 Set-Cookie | 本项目固定该配对及铸票代理，忽略业务返回的新 Cookie；HTTP 连接池并非独占原连接，WS 必须新建握手 |
| 无工具业务 | 脚本仍注入 synthetic noop，并要求不调用工具 | 本项目保留真实业务工具和指令，不凭空加入 noop；无 namespace 时不保证存在 additional_tools；不能据此宣称所有请求信封完全一致 |
| 返回其他模型 | reuse_burst 停止继续生成 | 本项目使当前配对失效，后台下轮采集，不重放当前请求；已发出的并发请求可能仍返回 |
| 无模型/中断 | reuse_burst 因 gen_model 为空停止 | 已对齐：上游正常终止却未声明任何模型（completed/failed/incomplete 且整流无 model）时同样作废并重采；差异仅在网关必需的例外——客户端取消、超时、读错误与非 200 不作废，避免把客户端中断和账号级限流误判成降级 |
| 无票策略 | 不发送生成请求 | 可配置 fail-open；允许无票或 Lite 不兼容时走原链路。需要接近脚本时开启“无票时暂停账号” |
| 节点轮换 | mihomo 选择器、节点历史、90/180 秒冷却、结束回香港01 | 未实现该选择器协议；使用配置代理和账号/模型级冷却。必须由外部提供稳定/粘性出口 |
| 普通模式 | main 调用 run，每轮重新铸票后生成一次 | 后台预铸票，跨请求复用直到过期或失效 |
| 轮换成功判据 | rotate 只要 hits>0 就返回，即使后续生成降级；不保证 n 次连续成功 | 没有连续 n 次预验证；harvested 仅表示铸票成功，实际生成看 generation 日志 |
| 插件链路 | 无插件 | 采集绕过插件，业务可能经过插件；generation 的 plugin_handled=true 时还需确认插件遵守传入代理和请求头 |

票据长度和上游声明的 model 都是路由观测信号，不是模型能力的独立测量。日志中的 proxy_endpoint 也是配置端点，不能证明实际出口 IP 相同。当前票据的进程内缓存与数据库/调度快照存在传播时差；跨实例失效并非全局即时撤销。

## 运行日志与排查

沿用现有日志系统，无需新建日志服务。默认 info 即可看到下列事件；`applied` 是 debug，仅排查注入时临时启用。推荐 `log.format: json`，并按部署实际配置确认 `log.output.file_path`；容器可直接查标准输出。文件滚动与保留时间仍由 `log.rotation` 管理，未在本次审查中改动线上配置。

| 消息包含 | 含义与重点字段 |
| --- | --- |
| openai_codex_ticket harvested | 铸票已被当前进程接受；account_id、model、bundle_id、length、response_model、cookie_present、proxy_endpoint、http、duration_ms、expires_at。不是业务生成成功证明，也不保证数据库持久化成功 |
| openai_codex_ticket probe miss | 采集未通过；reason、http（0 表示无 HTTP 响应）、length、cookie_present、response_model（可观测时）、cooldown_seconds；token 失败可能没有响应字段 |
| openai_codex_ticket probe skipped | 缺少采集代理或 transport；检查 reason。关闭功能、范围外、冷却中等不会逐账号输出此事件 |
| openai_codex_ticket probe cycle | 本轮调度的探测数量、范围和优先级分布；不表示全部采集成功；无探测时不输出 |
| openai_codex_ticket request decision | 缺票或 Lite 不兼容；reason=ticket_unavailable/lite_incompatible，fail_closed=true 为拦截，false 为走原链路。请求构建可能多次执行，此事件不能当作请求计数 |
| openai_codex_ticket applied | debug：在 HTTP 请求构建时选择了票据。不能单凭该事件断定已经发送 |
| openai_codex_ticket generation | HTTP 每次带票转发完成、EOF、关闭或失败时输出一次；WS 在终止事件输出。reason=ok 才是完成且返回模型一致；HTTP 还记录 http、duration_ms、plugin_handled。WS 中途断连无终止事件时结合现有 WS 错误日志排查 |
| openai_codex_ticket invalidated | warn：本次生成未按票据模型应答（模型不一致/冲突，或上游正常终止却无模型），当前配对已在本进程失效并等待重采；按 bundle_id 查对应 generation 和后续 harvested |
| openai_codex_ticket persist failed / invalidate persist failed | warn：票据保存/失效持久化失败，本地状态已改变但其他实例或重启后的状态可能不同；检查数据库与调度快照 |

generation 的异常 reason 包括 `response_model_mismatch`、`http_status`、`transport_or_read_error`、`response_failed`、`response_incomplete`、`missing_response_model`。`error_class=timeout/canceled/error` 区分错误类别，不输出可能夹带凭据的底层错误原文。HTTP 成功时 error_class 为空。`model_conflict=true` 表示同一响应中的模型声明互相冲突。

`bundle_id` 是票据、Cookie、代理组合的 SHA-256 摘要前 16 位，用于日志关联，不是票据原文。代理日志仅包含协议、主机和端口；新增日志不输出 token、票据或 Cookie 原值、请求正文、上游错误正文或数据库错误原文。HTTP 错误/中断不会自动重放用户请求。

JSON 日志查询示例（把文件路径、账号 ID 换成部署实际值；默认 logger 消息字段为 msg）：

```sh
rg 'openai_codex_ticket' /app/data/logs/sub2api.log
jq -c 'select((.msg // "" | startswith("openai_codex_ticket")) and .account_id == 41)' /app/data/logs/sub2api.log
jq -c 'select(.msg == "openai_codex_ticket generation" and .reason != "ok") | {account_id,model,response_model,bundle_id,transport,http,reason,error_class}' /app/data/logs/sub2api.log
jq -c 'select(.bundle_id == "替换为待排查的bundle_id")' /app/data/logs/sub2api.log
```

建议按账号/模型统计采集失败与生成异常，优先处理 `response_model_mismatch` 和持久化失败。同一 bundle 多次降级时先核查代理出口是否漂移、是否跨传输/插件，再检查票据剩余有效期。采集成功但没有 generation 可能只是尚无业务请求，或请求被兼容性策略拦截/放行到无票链路。日志受采样、等级、轮转影响，不能直接等同于精确计费指标；bundle_id 仅用于日志检索，不宜作为监控指标标签。

## 本次验证记录

2026-09-22 本地验证通过（Go 命令在 backend 目录执行）：

- `go test ./internal/service ./internal/repository`：service 135.768 秒，repository 2.347 秒。
- `go test -race ./internal/service -run 'TestCodexTicket(Log|Pair|HarvesterStop|ProbeBypasses)|TestRefreshOpenAICodexTickets' -count=1`：通过。
- `go test -tags=unit ./internal/repository -run 'TestSchedulerTicket' -count=1`：通过，覆盖 Redis 票据投影和网关读取。
- `python3 /Users/ambi/Desktop/fenjue/fenjue.py --self-test`：self-test ok，使用本地模拟上游。
- `git diff --check`：通过。

新增日志回归覆盖同一票据连续三次成功后第四次返回 Luna、失效与日志关联、HTTP 非 200/超时/中断/提前关闭/模型缺失、WS 跨事件模型冲突、采集失败保留响应元数据，以及持久化异常脱敏。未使用真实账号访问上游、未部署服务；真实出口稳定性和返回模型需部署后观测。

2026-09-22 追加（手动重打与 fenjue 失效语义对齐）：

- `RefreshOpenAICodexTicketsForAccount` + `POST /admin/accounts/:id/codex-tickets/refresh`：清冷却、同步探测、绕过采集范围；返回合并内存票后的状态。
- 生成结束的作废条件对齐 reuse_burst：HTTP 200 与 WS 终态在「模型不一致/冲突」或「整流无模型」时作废（`response_model_mismatch`、`response_failed`、`response_incomplete`、`missing_response_model`）；取消/超时/读错误/非 200 仅记录。
- 回归：`go test ./internal/service -run 'Ticket|Harvest|CodexTurnState|ResponsesLite' -count=1` 通过；`go test -race ./internal/service -run 'TestCodexTicket(Log|Pair|Probe|Harvest|Generation)|TestRefreshOpenAICodexTickets|TestOpenAIWSForwarder' -count=1` 通过；`go vet ./internal/service ./internal/handler/admin` 干净。

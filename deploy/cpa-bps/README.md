# CPA-BPS 0.5.31 与 Sub2API

这是独立通道的部署入口。请求链路为：客户端 → Sub2API → Python 兼容代理 → CPA-BPS 插件。CPA 保存和刷新 BPS OAuth；Sub2API 只保存网关 client_api 密钥、提供分组和计费。一个 CPA 实例在 Sub2API 中是一个上游账号，不能据此计算 CPA 池内每个 OAuth 账号的用量。

## 环境与固定来源

需要 Linux x86-64、Python 3.10+、本机 Linux amd64 Docker Engine 和 Compose v2。Windows 使用 WSL2。原插件不支持 macOS/ARM 原生运行。本目录不包含插件二进制、管理面板二进制、账号或密钥；安装脚本按固定提交下载 ZIP，校验 SHA256 和包内清单，保留 bundle 中的许可证及通知。镜像全部固定摘要。

- 固定版本：0.5.31。
- 来源：2697106714/cpa-bps，提交 `aaee30218e174e5a6c495b935d20d4014d432f49`。
- ZIP SHA256：`c35fe15632fbf6a09437ce490afcdf1877ea7e9f2a66a9dfee087ec55145e45b`。
- 导入来源：用户提供的 `bps` 分享包。Python 兼容代理保持原文件内容；部署脚本仅增加共享 Docker 网络选项。导入文件的来源哈希见 `SOURCE.json`；原附件未单独附带脚本许可证，保留原作者文件头及来源声明，不将其重新声明为本仓库原创授权。原包部署记录位于 `references/validation.md`，它不是本次整合的验收结果。

## Sub2API 在宿主机运行

在本目录执行，所有命令使用相同的独立运行目录：

```sh
python3 scripts/bps.py doctor
python3 scripts/bps.py install --root /opt/sub2api-cpa-bps
python3 scripts/bps.py status --root /opt/sub2api-cpa-bps
```

Sub2API 账号的 Base URL 填 `http://127.0.0.1:18319/v1`。18317 是 CPA 核心，18318 是管理面板，18319 才是带原包清洗逻辑的兼容入口。端口冲突时用 `install --port-base 19317`，对应兼容入口为 19319。

## Sub2API 在 Docker 中运行

先建立独立网络，再把现有 Sub2API 服务加入该网络：

```sh
docker network create sub2api-cpa-bps
# 从 Sub2API 仓库根目录运行，保留原先部署使用的 Compose 文件和环境文件参数。
docker compose -f deploy/docker-compose.yml -f deploy/cpa-bps/sub2api-network.yml config
# 检查生成配置后，用同一组参数执行 up -d sub2api。
```

部署 CPA：

```sh
python3 deploy/cpa-bps/scripts/bps.py install --root /opt/sub2api-cpa-bps --network sub2api-cpa-bps
```

Sub2API Base URL 填 `http://cpa-bps-gateway:8318/v1`。只有 gateway 加入共享网络，core/manager 留在独立 Compose 网络中。管理端口仍绑定宿主机回环。一个共享网络只接入一个使用该别名的 CPA 实例；多个实例使用不同网络。

Sub2API 的出站 URL 策略必须允许所填地址和 HTTP。检查现有 `security.url_allowlist` 配置；不要为接入而关闭整站安全策略。启用严格 HTTPS 白名单的部署应给兼容代理配置内部 HTTPS 入口，并仅加入该主机。

## 授权与接入

1. 访问安装结果中的面板地址（默认 `http://127.0.0.1:18318/management.html`）。远程服务器可通过 SSH 本地端口转发访问。
2. 在服务器本机查看运行目录的 `secrets/keys.json`，使用 `manager_admin` 登录；Manager 的 CPA 地址填 `http://core:8317`，管理密钥填 `cpa_management`。不要把密钥贴入日志或聊天。
3. 在原面板完成自己的 BPS 授权。Sub2API 现有 OAuth 账号不会自动迁移，不让两个服务同时刷新同一套凭据。
4. 在 Sub2API 新建 OpenAI/API Key 账号，打开 **CPA-BPS**，填写兼容代理 Base URL 和 `client_api`。使用独立分组，避免与内置 BPS 混池。
5. 用“获取 CPA 模型”查询真实模型。空列表就是未发现可用模型；先检查原面板授权，不手填模型冒充权限。
6. 保存后重新打开编辑页，选择“测试已保存通道”。测试需要显式点击执行，将发送一次真实模型请求。更新地址或密钥后先保存再测试。
7. 客户端保持连接 Sub2API，使用该分组的 Sub2API API Key。切换通道后新建会话，再验证工具调用、工具回执、多轮和 compact。

## 行为与边界

首版仅支持 HTTP/SSE Responses 和 compact 请求转发、模型查询。Chat Completions、独立图片接口和 WebSocket 不可用；内联图片内容原样交给 CPA，不承诺插件具有视觉权限。compact 直接转发 `/v1/responses/compact`，插件若拒绝则返回其错误，不转为内置 BPS 压缩请求。

原包默认开启 `omit_encrypted_history`，关闭 `fast_mode`，请求重试次数为 0。Sub2API 发往 CPA 后也不重试或换通道。客户端取消将关闭上游连接。HTTP 200 中的 failed/incomplete 及缺少终态的断流都算失败；存在实际用量时沿用失败请求用量结算，不伪造零用量成功。

兼容代理会删除缺失 call_id 或在当前请求中找不到原调用的工具回执，也会删除特定 create_thread 交接上下文。这是沿用原包的行为；Sub2API 不做第二次清洗。代理只在 HTTP Responses 上清洗，compact 由 CPA 核心处理。不得把本通道描述为无损历史转发或内置 BPS 的等价替换。

## 停止、回退与验证

```sh
python3 scripts/bps.py stop --root /opt/sub2api-cpa-bps
python3 scripts/bps.py start --root /opt/sub2api-cpa-bps
python3 -B -m unittest discover -s scripts -p 'test_*.py'
```

stop 保留运行目录和授权数据。重复 install 不覆盖已有配置；`--network` 仅用于首次安装，已有部署应先停止并按自己的配置变更流程修改网络。回退时将客户端切回原内置 BPS 分组并新建会话；不删除原账号、不搬动已有凭据。

资源压力测试 `test_proxy_resources.py` 应在原部署 Linux 容器中执行，设置 nofile=4096、pids=512。本机离线测试不代表 Linux 插件已启动，更不能代表真实账号推理通过。测试记录见仓库 `docs/cpa-bps-validation.md`。

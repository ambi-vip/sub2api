# Ambi Sub2API 桌面安装与配置

站点固定为 `https://sub.ambi.vip/`，Responses API 地址为 `https://sub.ambi.vip/v1`。安装目标是新版 ChatGPT 桌面应用，配置作用于其中的 Codex 本地模式。ChatGPT Classic、Chat/Work 的其他登录方式不由这些脚本修改。Windows 针对原生模式，不配置 WSL。

## 使用

- Windows：下载并双击 `ambi-setup-windows.cmd`。
- macOS：下载 `ambi-setup-macos.zip`，解压后双击 `Ambi-Setup.command`。ZIP 保存了脚本执行权限。
- 在本机弹窗中输入本站 API Key，选择该密钥可用且支持 Responses 的对话模型。

脚本仅读取 `/v1/models?client_version=0.158.0` 的 Codex 模型目录，不会发起付费生成请求；使用与仓库现有配置窗口一致的客户端版本参数，并将完整目录 URL 写入 `model_catalog_url`，保留自定义模型的元数据。缺少应用时自动安装；Windows 先搜索 Microsoft Store，仅匹配已知官方产品 ID 才安装，缺少 winget 时在系统授权后运行用户指定的 `asheroto.com/winget` 第三方脚本。macOS 已有 Homebrew 时使用 cask，否则通过 OpenAI 更新源下载并验证应用签名。脚本不安装 Homebrew，不修改全局执行策略。

配置尊重 `CODEX_HOME`，默认为当前用户的 `~/.codex/config.toml`。存在原配置时，先在同一目录生成 `config.toml.ambi-backup.*`，再重建配置。因此原有模型、MCP、功能开关等设置需要从备份手动合并。不会修改 `auth.json`。API Key 以明文写在本机配置中，macOS 文件权限为 0600，Windows ACL 仅允许当前用户访问。下载文件和网页均不含用户 API Key。

若桌面应用已打开，需要用户完全退出后重开；脚本不会强制结束正在进行的工作。系统可能仍会要求授权安装或确认脚本运行，网页不能绕过这些提示。

## 恢复

退出应用，将所需的 `config.toml.ambi-backup.*` 备份复制回 `config.toml`，再启动应用。没有旧配置时，可移除新生成的 `config.toml` 后重新登录。备份可能包含旧凭据，应仅保留在本机。

## 打包与发布

修改脚本或 `page.js` 后，从仓库根目录运行：

```sh
python3 tools/desktop-setup/build.py
```

该命令不安装应用、不写用户配置、不访问网络。输出：

- `docs/chatgpt-desktop-setup.html`：内嵌脚本下载内容，可离线直接打开的单文件页面。
- `frontend/public/help/index.html`、`setup.js`、`downloads/`：随 Vite 前端构建发布的版本。JavaScript 从同源文件加载，兼容仓库默认 CSP。

部署包含这些文件的新前端后，页面地址为 `https://sub.ambi.vip/help/`；Windows 文件位于 `/help/downloads/ambi-setup-windows.cmd`，Mac 压缩包位于 `/help/downloads/ambi-setup-macos.zip`。这些地址在部署后才可用，本任务不自动部署生产站点。

## 文档依据

- [ChatGPT/Codex 自定义模型提供商配置](https://learn.chatgpt.com/docs/config-file/config-advanced.md)
- [连接网关与支持范围](https://learn.chatgpt.com/docs/enterprise/connect-to-a-gateway.md)
- [Windows 应用与官方 Store 产品 ID](https://learn.chatgpt.com/docs/app/windows.md)
- [Homebrew ChatGPT cask 与官方 Mac 更新源](https://github.com/Homebrew/homebrew-cask/blob/master/Casks/c/chatgpt.rb)

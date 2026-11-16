# 本包验证记录

2026-11-12，在 Windows + WSL2 Ubuntu / Linux amd64 Docker、Compose 2.40.3 环境验证。

- 从固定公开提交下载ZIP，SHA256匹配；包内文件清单逐项校验通过。
- 在全新隔离目录和独立Compose项目部署三个容器，使用空账号库和新生成密钥。
- core/gateway模型接口、认证后的凭证列表和Manager健康接口均200；账号数0、模型数0符合未授权状态。
- 管理API证实cpa-bps插件registered和effective_enabled；Manager返回页面与固定包BPS面板匹配。
- 同目录重复install通过，不覆盖配置/账号；stop正常关闭服务，测试项目随后删除。
- 从实际分享ZIP解压并执行Windows技能安装成功，16个文件校验一致；重复安装拒绝覆盖，含空格的目标目录运行doctor通过。安装器使用.NET SHA256以兼容缺少Get-FileHash命令的环境。
- 部署目标Python镜像中31项离线测试通过，包括目录归属、重复安装、ZIP路径逃逸/篡改、回环绑定、凭据隐藏、HTTP流式、取消及PID资源预算；压力测试结束workers=0。
- 首次在Windows本机运行Linux资源测试时出现socket关闭语义差异；首次容器测试未设置部署所需nofile=4096，触发FD上限。最终按部署配置在Linux镜像复测31项全部通过。

本次未登录任何账号、未发送真实推理、未修改现有BPS/VM服务或客户端。新接收者机器尚未实测，OAuth、上游权限、代理可达性及其实际业务工具调用仍需本人验证。

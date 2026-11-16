# 固定来源与测试边界

第三方包直接由接收者下载，发布包不复制二进制、第三方管理面板或任何运行数据。

- CPA-BPS：2697106714/cpa-bps，提交 aaee30218e174e5a6c495b935d20d4014d432f49，包 0.5.31。
- 原始ZIP SHA256：c35fe15632fbf6a09437ce490afcdf1877ea7e9f2a66a9dfee087ec55145e45b；下载地址与镜像摘要在 scripts/bps.py。
- CPA、Manager采用已验证部署所用的固定镜像摘要；代理使用Docker官方Python 3.13 slim bookworm的固定索引摘要。
- HTTP兼容代理来自已部署脚本的无凭据副本，保留资源等待上限与进程预算处理。该脚本不是BPS二进制插件本身。
- 原包README声明Linux AMD64/glibc；本技能在Windows上通过WSL调用Linux Docker，不能原生加载Windows或ARM插件。
- 下载包内许可证和通知保留在安装目录bundle中；本技能没有把整套第三方软件重新声明为MIT或承诺有完整业务源码。

安装完成只表示本机三个服务及BPS面板可访问；新用户必须完成自己的授权和请求验证。没有账号的部署演练不能证明其账号具有上游权限。

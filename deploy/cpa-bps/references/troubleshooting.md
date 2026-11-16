# 诊断

先status查本机服务，再查账号、模型、上游错误及终态。不要把所有错误归因于新账号。

| 现象 | 处理 |
| --- | --- |
| 缺依赖、非amd64、端口冲突 | 看doctor，不杀已有服务，不切全局Docker context。 |
| 模型为空 | 检查授权、权限及发现；手填模型名不能制造权限。 |
| 401 | 区分本机API key和OAuth被拒；不重导另一池使用中的凭据。 |
| 429 | 看code、额度与并发；不自动兑换重置卡。 |
| 503 / worker creation failed | 查线程预算、队列与内存。包内使用pids_limit=512、normalizer等待15秒、处理期限60秒；不是无限并发承诺。 |
| basispoints_protocol_error | 工具schema、封装或操作纠正失败；tool transport correction changed an operation; no tool was executed表示该次工具未执行。不能伪造成功、替换成另一操作或关闭校验。 |
| HTTP200但response.failed | 仍为失败，管理成功数可能不准确；保留终态和脱敏trace，不清统计。 |
| unknown provider for model | 检查插件注册、账号发现和模型ID，不无限重试。 |
| SIGBUS / OOM / 断流 | 区分插件崩溃、内存及取消。http2client=0只是规避，不代表缺陷根治。 |

兼容代理处理HTTP JSON中的部分孤立工具回执和特定CUA schema；WebSocket升级后原样转发，不改其中JSON。删去孤立回执会改变历史，不适合要求无损保留历史的场景。

一次短文本或糖果题不能证明全部工具、长期稳定性或推理能力。本技能固定部署方法，不承诺第三方通道持续可用。

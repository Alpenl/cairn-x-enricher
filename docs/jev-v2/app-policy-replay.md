# B07：受限 App 规则重放执行器

配对 Worker 合同在 `cairn-share/docs/jev-v2/app-policy-replay.md`。本批提供受现有内部令牌保护的专用 Go 执行器，Worker 验证 App 权限后只代理这组路径；原管理 HTTP 不能直接当作 App 鉴权面。

`CAIRN_POLICY_REPLAY_URL` 是 Worker 的可选执行器基址，默认关闭；Go 使用现有 `CAIRN_ENRICHER_TOKEN` 配置专用路径身份。新决定另需 `CAIRN_ALLOW_DECISION_WRITE=1`，普通读取/已有回执不需打开写开关。没有改动部署、远端配置或 secret。

Go 只接受准备 nonce 或应用 nonce/hash，不接受 App policy/阈值/spec/model/automatic。初次读取并解析已注册规格，再向权威 Worker 精确协商规格、词表、策略及模型。纯函数使用实际历史规则作为基线，重放到当前明确支持的已部署规则；不猜历史，不调用模型，不唤醒处理器。已有不可变预览/已应用回执先于可变状态读取，恢复不换 nonce、不重算、不改写后来的人工作业。

后端严格检查版本、归属、显式零代次、原执行计划、policy hash 和零模型回执；无跳转、无 POST 自动重试，响应有界。App 响应移除内部执行计划及 review hash。准备/应用 SQL 沿用原完整来源/运行/目标/人工 CAS，并增加所预览的投影与偏好版本护栏。

验证为 `make verify`、两仓现有完整门禁以及 `CAIRN_INTEGRATION_CASE=appreplay CAIRN_SHARE_ROOT=<paired checkout> bash tests/local-integration/run.sh`。第 50 个真实场景通过 App Worker、受令牌保护的真实 Go HTTP、实际 D1/R2 核对提交后丢响应、新来源/人工修改、禁写和预览清理后的原回执；初始模型仅用本地合同替身，重放没有新模型调用。原 49 场景位置及分片保持，完整候选成绩以准确配对提交 CI 为准。

首轮真实 Worker 拒绝只有 policy 的不完整能力声明，现由完整精确声明替代，未绕过 `supported`。原失败及修复日志保留。Android 交互和设备恢复仍须后续增量，不关闭整个 B07、不自行签收最终复审。没有新增迁移、上线、签名或发行；开源发布仍需用户明确同意。

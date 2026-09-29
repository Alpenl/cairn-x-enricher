# B02-T05 / B01-T02：权威目标与消费者规格握手

日期：2026-09-29。范围：本地 Go CLI、Worker/D1 和合成供应商夹具；没有真实付费调用、远端迁移或部署。

## 发现与修复

- Worker 原来允许激活未注册的 v2 `spec_id/spec_hash`，`supported` 和 claim 只核对规格 ID 等能力，不核对已注册的 hash。这会让任务先领租约，再在 run 提交时失败。现在激活前检查已注册规格及 hash；读取目标和领取时也识别既存坏目标，拒绝领取。
- Go 原来相信 Worker 的 `supported`，未核对本地编译规格的 hash。`classify --id` 还在目标检查前重排任务。现在成功注册规格后保留本地 hash；各分类入口在领取前核对协议、规格、hash、词表、政策和模型。`classify --id` 先核对再重排。
- 握手到领取之间若目标切换，Go 在 claim 中提交 `expected_generation`，Worker 在领取前拒绝旧 generation。Worker 原有原子 UPDATE 仍检查目标指针；回包也由 Go 核对绑定的 generation 和规格。

## 验证

- Worker `npm test`：39 文件、393 项通过；`npm run typecheck` 与 `npm run deploy:dry-run` 通过。新增回归覆盖未注册/错误 hash 不能激活、历史坏目标令握手 unsupported 且不领取、握手后切目标不消耗 attempt。
- Go `make verify`：vet、lint、race、前端 488 项检查和构建通过。单测核对未注册或不同 hash 的本地规格在 claim 前暂停，匹配时 claim 携带 generation。
- 真实本地 Worker/D1：`CAIRN_INTEGRATION_CASE=cli` 验证编译后的 `classify --id` 在不兼容目标下不改 classification job、不请求模型；恢复正确目标后正常分类和显式重试。`onceempty` 验证保存来源在不兼容目标下不消耗 attempt，恢复后可继续。`competition` 验证先注册规格后的版本竞争；`lifecycle` 和 `manualrestart` 复验已有跨阶段及人工来源恢复场景。

## 仍需组合验收

本证据没有模拟运行中的 `serve` 遇到目标变化，也没有覆盖新旧 Worker/Go 版本组合、生产数据迁移和长时间负载。B02-T05 与 B01-T02/T03 的总复选框暂不据此勾选，跨端组合仍由 #16 验收。

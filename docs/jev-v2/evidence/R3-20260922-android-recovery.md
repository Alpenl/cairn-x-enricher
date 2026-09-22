# R3-07 / 2026-09-22 Android 持久动作链与真实进程恢复

总控 [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，主责 [S #31](https://github.com/Alpenl/cairn-share/issues/31)，统一验收 [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。本报告证明下面列出的 R3-07 / R2-04、R2-05 工程场景；不是 B07、B10 或所有原 R/B/SC 的总体验收。

## 固定版本

受测 Share 代码 `3aae256d80e966a685e836783502b4caf1ade1bc`；Enricher `60e049764941dfb860e5b7a57c741a793e76a000`（本批未修改 Go 业务代码）。随后证据提交单列于 #16；实现 PR 仍为 Draft [S #32](https://github.com/Alpenl/cairn-share/pull/32) / [E #17](https://github.com/Alpenl/cairn-x-enricher/pull/17)。本批零付费模型请求，无部署、生产迁移或合并。

## 修复与保证

原队列同收藏多条离线动作都绑定旧 revision；每次重试仍用旧值，第一条成功后后续永久冲突。Worker 首次响应顶层 revision，重放只有 override.revision，Android 默认猜回 expectedRevision。ViewModel 在线动作最初仅在内存，flush 又无法把结构化冲突接回界面。

- **持久化先于请求**：所有动作走 CurationActionStore，同收藏后继记录 predecessorKey，未确认前 expectedRevision 为空，不提前猜版本。确认时同一次 DataStore edit 删除前序、写入后继 expectedRevision 与 predecessorRevision；保留其他收藏/账号以及期间新入队的动作。每一条确认落盘后才发送下一条。重建 ViewModel 或整个进程均从存储恢复。
- **真实确认**：首次与合法重放都有 bookmark id、field/term/action、operation_key、revision、replayed，保留旧 nested override 兼容消费者。Android 验证身份和数值；仅对明确旧版 shape 兼容读取，不再把缺失字段默认成旧版本。并发相同操作最多一条 override/event 和一次 revision；异 key/payload 仍冲突。自己已成功的旧操作重放返回原确认，不被后来别人的 revision 覆盖。
- **结构化冲突**：Repository 返回结果和剩余动作，并把 conflictRevision 持久化；重启不自动采用远端版本。用户明确重新应用时先读取最新服务端状态，再将原 reject/set_empty/reset/多字段动作重新连接成依赖链。第二次真实冲突和中途网络失败保留剩余意图。丢弃限定当前账号和单收藏；成功不会一次清空所有草稿。
- **实际 App 入口**：发现 Android 默认 Worker 地址缺少 Go 看板形式的 v2 路由，补齐精确 allowlist 的 GET `/api/v2-taxonomy`、GET `/api/bookmarks/:id/v2-selection`、POST `/api/bookmarks/:id/v2-override`。只接受 App Token，POST 要求非负整数 expected_revision；旧内部 API 鉴权不变。App 无法经这些路由访问 specs、runs、evidence requests 或 taxonomy proposals。没有给 App 内部凭据，也没有把全部 `/api/v2/*` 暴露出去。
- **旧队列**：无依赖元数据的历史行保持 queueVersion=0，不捏造曾经确认的前序；仍使用其原有 revision，真实冲突交回用户。新入队动作和用户明确重应用的动作使用 version=1。回退会失去新的依赖语义，升级后的队列不应交给旧客户端自动发送。

## 实际执行与观察

新 `tests/android-worker/run.sh` 使用本地 wrangler、全部迁移、真实 D1/R2 和 App Token。Python 代理仅丢弃/延迟传输效果及发出独立客户端请求，业务结果由真实 Worker 决定；没有 fake Applied/409。测试沿 Android 实际 JSON/HTTP client → Repository → DataStore → ViewModel，使用 API 26 x86_64 Google APIs 隔离 AVD `cairn-r307-api26`。通过 `sg kvm` 使用已有组权限，不改变系统权限或用户现有 AVD；设备确实启动并执行。主机未安装有效 API 35 镜像，本批不声称 API 35 或物理真机通过。

| 阶段 | 验证结果 |
|---|---|
| 第一进程，离线五条动作 | accept topic、reject topic、单值 carrier、set_empty affordances、reset topic 全部入 DataStore；后四条明确依赖前序且没有猜出的 expectedRevision |
| 第一条已提交，代理丢响应并保持离线 | D1 revision=1；原五条动作和 key 都留在队列，不显示已同步 |
| `am force-stop` 后第二进程，正常启动恢复 | 原 key 精确重放确认 1；后继通过原子落盘只推进到 1 |
| 第二条前独立客户端真实写入 | D1 revision=2；Android CAS 返回实际 409，剩余四条与草稿保留，界面出现可操作冲突 |
| 用户重新应用，另一客户端再次抢先写入 | 重新读取 2，但 CAS 再次失败于 3；第二次冲突仍持久保存 |
| 再次明确应用，第二条成功，第三条前断网 | revision=4；剩余三条及第三条 predecessorRevision=4 持久保留 |
| 恢复网络并 flush | 五条原意图最终完成，总 revision=7（五次原动作加两次独立客户端写入），topics reset 后为空，carrier=external_article，affordances 为空 |
| 其他账号 | 独立保留的 foreign-account-action 从未发送，也没有被整个队列清除 |
| 第三进程：两个收藏、丢弃一个、切换账号再恢复 | 只删除指定收藏草稿/动作；另一收藏两条保留；不同 token 期间不发送，切回后确认两条，服务端 revision 分别为 0 和 2 |

完整调用历史 [transport-history.json](logs/20260922-android-recovery/transport-history.json) 只含测试夹具动作及 operation keys，不含私有收藏或凭据。这里的跨客户端是实际 HTTP 写入同一 Worker 的竞争，不冒充浏览器 Web UI 操作。

| 门禁 | 本批结果 |
|---|---|
| Worker npm test | 179/179，通过；包含真实 D1 并发同 key、丢响应重放、独立客户端 CAS、App 权限与参数矩阵 |
| Worker typecheck / deploy:dry-run | exit 0，dry-run 无部署 |
| Android strict testDebugUnitTest | 63/63，通过 |
| Android strict lintDebug / assembleDebug / compileDebugAndroidTestKotlin | exit 0 |
| 普通 connectedDebugAndroidTest | XML 14 项：11 实际通过，3 个需 Worker 的场景明确跳过；不是 14 项业务全通过 |
| 独立真实 Worker 设备 harness | 三阶段各 `OK (1 test)`，强制分进程，exit 0；补足上述 3 项，非仅编译 |
| Enricher make verify / make test-ablation | exit 0，包括全包 race/lint、73 前端检查和构建 |
| 真实 Go/Worker/D1/R2 回归 | 8/8，通过，模型边界为本地合同夹具 |

日志：[Worker](logs/20260922-android-recovery/worker-verify.log)、[Android 常规与设备门禁](logs/20260922-android-recovery/android-verify.log)、[跨进程真实设备与 Worker](logs/20260922-android-recovery/device-worker.log)、[Go 完整检查与 8 场景](logs/20260922-android-recovery/enricher-verify.log)。普通 connected 输出的动态计数包含 skip 回调重复，采用 XML 的 14/3 计数。开发期间完整 Worker typecheck 发现新增错误码未加入 ErrorCode union，已补齐并完整复跑，未放宽类型检查。

## 仍需完成

本批不关闭 S #31 或总体验收。B07 仍需全部原条目与兼容矩阵复核，包括完整账号绑定、旧队列升级 UX、自动/人工基线及 reset 的草稿显示、能力/缓存/字段状态、其余 UI/实体/导出和设备覆盖。目前 accountKeyFor 仍使用 token 尾 8 位，两个不同 token 尾部相同的情况未被安全地区分；已识别为下一项必须修复的账号隔离缺口，本批不同 token 用例不能证明该更宽范围。API 35、物理真机和完整 Web UI 竞态不由本次 API 26 HTTP 联调证明。

B08 自动参考校准/消融/固定候选检索/留出集、B09 全局预算与收益、全部 126 原任务/R/SC 审计、运行与回滚手册、独立验收仍在原目标内。人工标注已按授权取消前置要求；不能以工程门禁替代自动质量验收。

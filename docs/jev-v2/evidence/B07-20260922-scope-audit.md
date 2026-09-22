# B07 原任务范围审计 · 2026-09-22

依据 S #31 原 B07-T01–T10 与 R2/R3、`07-android-compat.md`，初次审查 Share `3a891f1eceda116b5c76007b8887fde8d36e0dd5` / Enricher `86b82b5e0ccf0096cd67445ce80e02c76f89b505`。本次按 Share `0a38e52` / Enricher `3520dc1` 的 [状态合同证据](B07-20260922-state.md) 更新 T01/T03/T07/T08/T10；前批 [缓存修复证据](B07-20260922-cache.md) 继续有效。状态是逐条证明程度，不用已有门禁或历史摘要代替要求。

| 原要求 | 已有直接证据 | 未完成或尚不能证明 | 下一步验收 |
|---|---|---|---|
| B07-T01 DTO：旧/v2、多状态、来源、候选、实体 | 实际保存 assessment；可选 state 单 SQL 快照；每值来源/revision/候选/独立实体；共享 D1/Android fixture，未知/畸形/不一致不确认 | 历史无元数据保持 unknown；完整新旧服务与状态组合端到端矩阵仍待 | 在实际 App→Worker 验证全部失败/过期/partial/人工来源组合，补全兼容矩阵 |
| B07-T02 能力、summary/detail、representation/revision 缓存 | 本批会话代次阻止 A→B→A 迟到响应；立即清空账号列表/搜索/详情；可选 schema/representation/content/body 身份，译文变化与 AFTER trigger 版本已测；刷新独立自动基线并保留原动作；分开接口能力 | 核心已修；仍需完整筛选/搜索并发、旧 Worker/flag-off 能力和所有原缓存失效子情景矩阵；latest 决策标记不是投影来源证明 | 完整兼容矩阵与真实状态/来源合同，继续测试尚未覆盖的搜索/刷新竞争 |
| B07-T03 有效值、候选、人工来源及状态分别显示 | 六维状态与已保存候选、人工/自动/legacy unknown 来源、待同步字段；主题摘要折叠不丢值；17 主题 FlowRow 可达；载体独立、潜在用途说明 | 新设备场景使用与真实 D1 对照的共享合同，尚非全部状态经实际 Worker 到 App 的完整逐项场景 | 完整真实服务状态矩阵与 UI 语义断言，继续验证未知不能确认 |
| B07-T04 单字段动作/幂等/不隐式接受 AI | 持久原操作/CAS/receipt 与七阶段真实恢复；本批 per-value 来源沿共同 fold，legacy 不变 gold | 已观察 set_empty 后仅 reset(term) 仍被 whole-empty 遮罩抑制，尚未修复；why/status/null 全矩阵仍待 | 同时修 Go/Worker/Android 单标签 reset 分支并补共享回归；why/status 不产生 override |
| B07-T05 离线/冲突/账号隔离 | R3-07 三阶段，新增四阶段：完整 credential/server fingerprint、显式旧队列恢复、缺失旧 CAS 冲突、两次真实跨端写入、进程重启和中途失败 | 队列归属与本批账号读取会话隔离已测，T02 完整矩阵仍待；本批跨客户端是实际 HTTP 写入，不是完整 Web 浏览器 UI 竞争 | 补齐 T02，并在完整 App/Web 用户流程重测跨端冲突和未确认草稿 |
| B07-T06 来源/分类/人工状态与能力权限 | App 精确只读/人工动作 allowlist；App 不能访问 specs/runs/evidence requests/proposals。普通阅读不发模型请求 | 三类生命周期尚未完整进 App，retry/replay 能力也未形成已验证的完整 UI/后端协商 | 权限矩阵与普通阅读零模型调用；不可用能力明确显示 |
| B07-T07 多维搜索/导出/实体/真实依据 | 导出全部有效值与来源/why/partial/状态；待同步不冒充确认；独立实体快照验证并替代旧 classification.entities | 实际 block/URL 身份的安全打开/导出未完成；实体人工动作与过滤全链、完整搜索组合仍需验证 | 真实证据导航/导出；actual entity reject/reset/stale 与过滤、第四主题/隐藏维度全链 |
| B07-T08 导航/恢复/分页/可访问 | ViewModel/DataStore 进程恢复；本批 FlowRow 全词表；实际 API26 360×640dp / 1.5 倍字体，17 项最后标签、候选/状态可达，页面重建通过 | 单个小屏用例不等于完整无障碍/旋转/分页/返回/图片译文矩阵；物理设备未执行 | 继续完整导航和后台恢复矩阵、辅助功能及原阅读功能回归 |
| B07-T09 三代客户端和旧缓存兼容矩阵 | 可选 automatic 请求维持旧严格读者 shape；新读者对旧服务缺失基线为 unknown；历史 v1 覆盖与隐含维度已有后端测试 | 尚缺当前最终 App/Worker 的完整端到端矩阵，不能把 Kotlin DTO unit 当真实读写兼容 | 对旧 App→新 Worker、新 App→旧/flag off、新→新，逐项执行 null/空 use/第四主题/隐藏功能/reject/source stale |
| B07-T10 CI/设备/证据与交付 | 本批 184 Worker、69 Android unit、完整 Go、8 实际跨仓、本地 API26 18 普通 + 7 实际 Worker 阶段；固定源码及 XML/日志已归档 | 本批固定 SHA 的 API26/35 各 18 普通 + 7 实际 Worker 阶段通过；本表其余缺口未完成，物理真机与完整独立复审未证明 | 核对固定 SHA 的 API26/35 与七阶段产物，随后完成全部原要求，不能局部关闭 S #31 |

本审计没有改变原复选框或缩小任务范围；未达到的项继续保留在总控 #10 / 验收 #16。自动参考质量 B08、B09 全局预算/收益以及所有原 R/B/SC 仍属总目标。下一批优先修复已观察的 empty/per-tag reset 分支，再完成缓存竞争、真实证据与端到端兼容矩阵；不要只增加旧模拟 DTO 的通过数量。

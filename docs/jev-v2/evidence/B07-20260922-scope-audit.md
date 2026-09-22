# B07 原任务范围审计 · 2026-09-22

依据 S #31 原 B07-T01–T10 与 R2/R3、`07-android-compat.md`，初次审查 Share `3a891f1eceda116b5c76007b8887fde8d36e0dd5` / Enricher `86b82b5e0ccf0096cd67445ce80e02c76f89b505`。本次按 Share `30110e7` 的 [缓存修复证据](B07-20260922-cache.md) 更新 T02/T05/T08/T10。状态是逐条证明程度，不用已有门禁或历史摘要代替要求。

| 原要求 | 已有直接证据 | 未完成或尚不能证明 | 下一步验收 |
|---|---|---|---|
| B07-T01 DTO：旧/v2、多状态、来源、候选、实体 | v1/v2 dimensions 解析；本批独立 automatic 可选协商；unknown baseline 不猜 | `FieldStatus` 仅定义枚举，没有实际状态/来源/候选 DTO 接入；`MultidimensionalSelection` 仍只有值、revision、automatic、pending reset。实体只解码字符串 state 与 classification.entities | 增加实际 Worker 状态合同，沿 JSON→Repository→ViewModel→UI 验证 not_run/failed/stale/空、人工来源和候选 |
| B07-T02 能力、summary/detail、representation/revision 缓存 | 本批会话代次阻止 A→B→A 迟到响应；立即清空账号列表/搜索/详情；可选 schema/representation/content/body 身份，译文变化与 AFTER trigger 版本已测；刷新独立自动基线并保留原动作；分开接口能力 | 核心已修；仍需完整筛选/搜索并发、旧 Worker/flag-off 能力和所有原缓存失效子情景矩阵；latest 决策标记不是投影来源证明 | 完整兼容矩阵与真实状态/来源合同，继续测试尚未覆盖的搜索/刷新竞争 |
| B07-T03 有效值、候选、人工来源及状态分别显示 | 有效多维值和本地草稿；未知基线 reset 显示等待确认 | `V2CurationSection` 主要是 chips 和“空”，没有候选/人工来源/分类 not_run/failed/stale 展示。enum 存在不能证明 UI 已支持 | 实际服务状态矩阵与设备截图/语义断言；未知不能标为已确认 |
| B07-T04 单字段动作/幂等/不隐式接受 AI | 持久原操作、expected revision、receipt，真实服务 lost-response 与并发 CAS；来源/人工分离已有 Worker 回归 | 旧六字段 BookmarkCuration 的 why/status/null 写入路径仍须与最新 Worker 全矩阵复核，不能用新增队列成功替代 | why/status 不产生 override；legacy unknown 不变 gold；per-tag 和全字段 reset 对照实际自动基线 |
| B07-T05 离线/冲突/账号隔离 | R3-07 三阶段，新增四阶段：完整 credential/server fingerprint、显式旧队列恢复、缺失旧 CAS 冲突、两次真实跨端写入、进程重启和中途失败 | 队列归属与本批账号读取会话隔离已测，T02 完整矩阵仍待；本批跨客户端是实际 HTTP 写入，不是完整 Web 浏览器 UI 竞争 | 补齐 T02，并在完整 App/Web 用户流程重测跨端冲突和未确认草稿 |
| B07-T06 来源/分类/人工状态与能力权限 | App 精确只读/人工动作 allowlist；App 不能访问 specs/runs/evidence requests/proposals。普通阅读不发模型请求 | 三类生命周期尚未完整进 App，retry/replay 能力也未形成已验证的完整 UI/后端协商 | 权限矩阵与普通阅读零模型调用；不可用能力明确显示 |
| B07-T07 多维搜索/导出/实体/真实依据 | 过滤参数和有效字段导出；本批未知 reset 的导出有待确认标记；URL 点击有 http 校验 | `v2ExportMarkdown` 注释宣称来源，但实际未导出每字段 human/AI origin、partial/证据身份；实体取 classification.entities，未证明独立 stale/自动基线/human overrides 一致 | 用真实有效视图及独立实体状态驱动展示/过滤/导出，验证 rejected/reset/stale 和第四主题/隐藏维度 |
| B07-T08 导航/恢复/分页/可访问 | ViewModel/DataStore 强制进程恢复、原动作串行、常规设备流程 | `DimensionRow` 是普通横向 Row，完整词表没有 wrap/横向滚动；大字体/小屏/触控和全词表可达性未证明。本批详情 effect 已随会话/材料/个人元数据变化重载，旧确认框不跨账号恢复 | 完整词表小屏/大字体设备测试、旋转和后台恢复；原图片/译文回归 |
| B07-T09 三代客户端和旧缓存兼容矩阵 | 可选 automatic 请求维持旧严格读者 shape；新读者对旧服务缺失基线为 unknown；历史 v1 覆盖与隐含维度已有后端测试 | 尚缺当前最终 App/Worker 的完整端到端矩阵，不能把 Kotlin DTO unit 当真实读写兼容 | 对旧 App→新 Worker、新 App→旧/flag off、新→新，逐项执行 null/空 use/第四主题/隐藏功能/reject/source stale |
| B07-T10 CI/设备/证据与交付 | 本批本地 65 unit/lint/build、API26 17 普通；7 阶段授权 Worker/D1 设备 harness；Go/Worker 全门禁。device workflow 已接 API26/35 真业务链并上传日志/XML | 前批 S `3a891f1` API26/35 均通过；本批最终 `d217acd` API26/35 各 17 普通 + 7 真实阶段通过，见缓存报告；物理真机未执行，且整张表仍有明确缺口 | 固定 SHA 的 XML 与七阶段 artifacts 已逐项核对；随后完成本表剩余项与独立复审，不以局部通过关闭 S #31 |

本审计没有改变原复选框或缩小任务范围；未达到的项继续保留在总控 #10 / 验收 #16。自动参考质量 B08、B09 全局预算/收益以及所有原 R/B/SC 仍属总目标。下一批优先补齐真实状态/来源合同与缓存身份，并解决全词表可访问性；不要只继续增加旧模拟 DTO 的通过数量。

# B05 / 多维词表与兼容协议：设计规格

任务及进度：[S #30](https://github.com/Alpenl/cairn-share/issues/30)，B05-T01–T14全文见Issue。前置B03存储与B04冻结判断/投影合同。总控[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。本文件不再包含旧PR head/base执行要求。

## 维度

| 字段 | 含义 | v1投影 |
|---|---|---|
| topics | 领域多选 | 显式顺序最多3个 |
| content_functions | 方法/工具/案例/数据/观点多选 | primary_form |
| carrier | 真实单帖/续帖/外链/unknown | 有来源事实才thread/longform |
| affordances | 引用/实践/背景/素材多选 | primary_use |
| 用户intent/status/stance | 明确人工个人维度 | 保留原why/curation范围 |
| facets | 如评估等跨领域方法 | eval旧ID显式mapping |

保留原17/7/5词表的稳定ID和历史含义。真正同义、上下位、相关关系分开；llm/AI/agent不能未经映射改变语义。term定义含正反例/边界，display与语义版本分离。校验ID/alias冲突、层级环、引用及停用。首次不无依据暴增标签，拆不开的旧标签不得自动补细类。

## 读写合同

v2显式协商；旧六字段与旧include=enrichment key/type保持，Go strict解码安全。有效投影人工优先、origin/revision/decision可追踪，大分布详情按需。合法空值不自动全局uncertainty。

v1 PATCH仅影响其可表达的旧维度/可见部分，不抹隐藏第四topic、多功能、reject或实体状态。旧协议没有revision时不能虚构CAS能力；无歧义则范围适配，歧义则保护数据并给可操作冲突。classification:null不扩大成删所有v2人工信息或why/history。

任务状态、字段原因、人工整理分别筛选。分类retry与policy replay权限分开。summary有界、详情按需；缓存带representation与内容/决定/人工revision，完成实体也正确失效而不清source。

## 查询及扩展接口

冻结同维OR/跨维AND和旧单值语义；统计按有效人工覆盖结果。SQL参数化、LIKE转义、无效/停用term处理。搜索覆盖原文/译文/摘要/标题/实体/备注/原因。重排候选先权限/过滤再有界返回原序、文本块/角色/cursor或session，不声称重排弥补漏召回。

实体not_run/failed/empty/stale独立，surface/canonical分开且支持人工修正。补证据与提案接口有范围、revision、预算、去重和审批。未批准不改变词表；批准需要diff/mapping/版本/影响dry-run及回滚。display-only零推断。Markdown导出全部有效字段、why/source、AI/人工/stale及partial数量，不冒充全库备份。

## 验收

四topic保留且v1合法；tool+method+data+thread可表达；空use成功；label改名不重评；停用历史可读；旧写不清隐藏值或复活reject；缓存/过滤/导出一致；候选不越权；未批准提案不生效。旧新App/Go shape/PATCH、共享vectors、本地迁移/回滚、安全/性能规模有测试。

SC03/06–08/13–16/19/21/23–25/29–30；R03–R06/R08–R09/R13/R19/R26–R33/R37–R38。Worker门禁及必要EXPLAIN，禁止以小样本假称大库性能。交evidence/B05.md/固定合同SHA，代码PR按功能拆，不自动关闭整个Issue。回退flag保留v2历史，不反向压平。


## 2026-09-23 实际列表筛选合同

`GET /api/links`（App token）和 `GET /api/enrichment/jobs`（内部 token）共用有效分类筛选：

- 既有 `topic` / `form` / `use` 各为单个稳定 ID；新增 `topics` / `content_functions` / `carriers` / `affordances` 为逗号分隔的 ID。`topic` 与 `topics` 合并为同一维；同维 OR、跨维 AND。载体本身仍为单值，多个查询载体表示任一匹配。
- 显式空参数、空分项、未知 ID、同名重复参数及超界值返回 `400 invalid_query`。已知停用 ID 可查历史收藏，不允许把未知条件静默丢弃后扩大结果。参数有界且绑定到 SQL，不插入用户字符串。
- 筛选读取最新决定、真实 legacy 原始层与按 revision/id 排序的人工事件；不读可变投影缓存，不截断第四主题，也不先截取固定数量候选再过滤。人工 accept/reject/明确清空/单标签或整维 reset 语义与共享向量相同。
- `entity_state` 可选 `not_run`、`failed`、`completed_empty`、`completed_nonempty`、`stale`；多个值用逗号 OR。实体状态由独立运行及 content revision/snapshot/hash 身份决定，人工实体纠正不冒充自动成功或解除 stale。
- 内部列表的 `counts` 为当前内容/分类/人工筛选条件下的各任务状态计数，保留所有状态供切换状态页；不受 `status` 页签、`limit`、`before_id` 影响。分页与计数在同一 D1 batch 事务读取。响应字段和类型不增加，旧无筛选请求仍返回全量状态计数。
- App 缓存键包括全部上述参数，并提升缓存格式版本。迁移 `0025_selection_filter_cache.sql` 在 content revision 变化及新增 evidence snapshot 的同一事务内失效读取缓存，不修改历史人工动作或决定。回退可移除这两个新增触发器而保留全部数据；未执行生产迁移。

此合同不代表 Android 无关键词收藏库已使用完整 v2 筛选：当前该路径仍对旧 v1 载荷本地过滤，须单独修复和设备验收。字段弃权/队列状态组合、新旧/flag-off 全矩阵等原 B05/B07 要求继续有效。

### 客户端显式确认

需要完整有效值筛选的客户端发送 `filter_contract_version=1`，上述两个列表仅在请求时返回数值字段 `filter_contract_version: 1`。未请求时维持旧响应字段；重复、空或其他版本返回 400。该参数参与 App 缓存键。

Go 新维度查询自动要求确认；旧 topic/form/use 可显式启用确认。无字段、null 或未知版本不当作成功，dashboard 以 `409 unsupported_filter_contract` 提示。Web 多维筛选及旧形态/用途控件均要求确认，清空后可继续旧服务普通浏览。词表不可用不静默删除保存条件。完整实际链及边界证据见 [Go/Web 报告](evidence/B05-20260923-client.md)。

后续 Android `76e3e66` 已将激活筛选的收藏库改为独立服务端分页集合，不再对 v1 摘要二次猜测成员；四维与实体状态多选，并在旧形态/用途或新维度筛选时要求数值版本确认。相对日期边界在一次分页查询内固定；确认写入使旧 cursor 失效并重新读取。默认无筛选根快照仍有同步上限，标记为已加载数量。设备与实际 Worker 证据见 [Android 收藏库报告](evidence/B07-20260923-library.md)，不代替完整新旧/flag-off 矩阵。

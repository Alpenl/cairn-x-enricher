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

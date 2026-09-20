# Jev v2：实际 PR 索引与执行交接

创建日期：2026-09-20。**本轮交付是11个真实的草稿PR及完整执行规格；生产功能仍待实现。** 不合并、不部署、不收费调用、不远端迁移。总入口：[Enricher PR #3](https://github.com/Alpenl/cairn-x-enricher/pull/3)。

范围：39项审查发现（R01–R39）、10个实施批次共126项任务、30个强制验收场景（SC01–SC30）。每项默认未完成，必须补代码、测试和证据后才能勾选。创建时仅提交14个文档文件：总入口4份＋各实施批次1份；不能把文档CI通过当作功能完成。

## 1. 先读这四份文件

- [完整审查 REVIEW.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-00-master/docs/jev-v2/REVIEW.md)：此前讨论全部分析的结构化归档、源码依据、优先级与取舍。
- [路线图 README.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-00-master/docs/jev-v2/README.md)：跨仓库责任、依赖、里程碑和发现→批次映射。
- [执行协议 EXECUTION.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-00-master/docs/jev-v2/EXECUTION.md)：数据/版本/幂等/覆盖/兼容约束、30场景及证据格式。
- 本索引：真实PR号、head/base、执行顺序和交接提示。

本文件是B00后追加的索引。已经创建的下游分支不会自动得到B00的新提交，执行时必须按下面的上游同步规则更新，或者通过以上固定分支入口阅读最新总协议。

## 2. 全部已创建 PR

E = `Alpenl/cairn-x-enricher`；S = `Alpenl/cairn-share`。B编号表示工作批次，不等于GitHub PR号。

| 批次 | 实际PR | 任务数 | 工作内容 | 批次规格 |
|---|---|---:|---|---|
| B00 | [E #3](https://github.com/Alpenl/cairn-x-enricher/pull/3) | 总控 | 完整分析、协议、路线图、索引 | [总入口](https://github.com/Alpenl/cairn-x-enricher/tree/plan/jev-v2-00-master/docs/jev-v2) |
| B01 | [S #24](https://github.com/Alpenl/cairn-share/pull/24) | 10 | Worker权威目标、版本竞争、局部失效 | [01-worker-safety.md](https://github.com/Alpenl/cairn-share/blob/plan/jev-v2-01-worker-safety/docs/jev-v2/01-worker-safety.md) |
| B02 | [E #4](https://github.com/Alpenl/cairn-x-enricher/pull/4) | 11 | 显式确认、ReadingResult、运行时安全 | [02-runtime-safety.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-02-runtime-safety/docs/jev-v2/02-runtime-safety.md) |
| B03 | [S #25](https://github.com/Alpenl/cairn-share/pull/25) | 14 | 快照/runs/decisions、字段覆盖与事件 | [03-domain-storage.md](https://github.com/Alpenl/cairn-share/blob/plan/jev-v2-03-domain-storage/docs/jev-v2/03-domain-storage.md) |
| B04 | [E #5](https://github.com/Alpenl/cairn-x-enricher/pull/5) | 15 | typed判断、结构化问题、纯策略与重放 | [04-judgment-policy.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-04-judgment-policy/docs/jev-v2/04-judgment-policy.md) |
| B05 | [S #26](https://github.com/Alpenl/cairn-share/pull/26) | 14 | 多维词表、兼容API、检索/治理接口 | [05-taxonomy-api.md](https://github.com/Alpenl/cairn-share/blob/plan/jev-v2-05-taxonomy-api/docs/jev-v2/05-taxonomy-api.md) |
| B06 | [E #6](https://github.com/Alpenl/cairn-x-enricher/pull/6) | 12 | Web字段整理、依据、状态与筛选导出 | [06-curation-ui.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-06-curation-ui/docs/jev-v2/06-curation-ui.md) |
| B07 | [S #27](https://github.com/Alpenl/cairn-share/pull/27) | 10 | Android接入、离线/CAS及跨版本兼容 | [07-android-compat.md](https://github.com/Alpenl/cairn-share/blob/plan/jev-v2-07-android-compat/docs/jev-v2/07-android-compat.md) |
| B08 | [E #7](https://github.com/Alpenl/cairn-x-enricher/pull/7) | 14 | gold/指标/消融/校准与晋升门禁 | [08-evaluation.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-08-evaluation/docs/jev-v2/08-evaluation.md) |
| B09 | [E #8](https://github.com/Alpenl/cairn-x-enricher/pull/8) | 14 | 实体、补证据、重排、词表提案 | [09-semantic-extensions.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-09-semantic-extensions/docs/jev-v2/09-semantic-extensions.md) |
| B10 | [E #9](https://github.com/Alpenl/cairn-x-enricher/pull/9) | 12 | 集成验收、迁移回滚、文档清理与复审 | [10-release-proof.md](https://github.com/Alpenl/cairn-x-enricher/blob/plan/jev-v2-10-release-proof/docs/jev-v2/10-release-proof.md) |

## 3. 分支与依赖

| 批次 | head branch | PR base branch | 实施时额外依赖 |
|---|---|---|---|
| B00 | plan/jev-v2-00-master | main（E） | 无 |
| B01 | plan/jev-v2-01-worker-safety | main（S） | B00协议 |
| B02 | plan/jev-v2-02-runtime-safety | plan/jev-v2-00-master | B01契约和实现 |
| B03 | plan/jev-v2-03-domain-storage | plan/jev-v2-01-worker-safety | B02握手fixture核对 |
| B04 | plan/jev-v2-04-judgment-policy | plan/jev-v2-02-runtime-safety | B03固定v2内部合同 |
| B05 | plan/jev-v2-05-taxonomy-api | plan/jev-v2-03-domain-storage | B04问题/投影合同 |
| B06 | plan/jev-v2-06-curation-ui | plan/jev-v2-04-judgment-policy | B05 API |
| B07 | plan/jev-v2-07-android-compat | plan/jev-v2-05-taxonomy-api | B06交互合同 |
| B08 | plan/jev-v2-08-evaluation | plan/jev-v2-06-curation-ui | B04 runs；基线准备提前 |
| B09 | plan/jev-v2-09-semantic-extensions | plan/jev-v2-08-evaluation | B03/B05扩展API、B04判断层 |
| B10 | plan/jev-v2-10-release-proof | plan/jev-v2-09-semantic-extensions | B01–B09全部工程交付，S末端B07 |

```text
E: main → B00/#3 → B02/#4 → B04/#5 → B06/#6 → B08/#7 → B09/#8 → B10/#9
S: main → B01/#24 → B03/#25 → B05/#26 → B07/#27
```

单执行者顺序：读取B00 → 基线与B08-T01准备 → B01 → B02 → B03 → B04 → B05 → B06 → B07 → B08主体 → B09 → B10。

**同步规则：** 当前PR的base是依赖分支，不是main。开始该批前fetch两仓，检查干净工作区及无关改动，再把同仓base的最新实现merge进head；不得force-push或覆盖他人提交。跨仓库使用明确的companion commit SHA并记录到测试锁定信息。基线分支有未完成工作不能假定已具备依赖功能。上游修复应先提交上游再逐级同步；不要只在B10补所有漏洞却声称上游早已完成。

如果所有者以后批准按序合并，下一PR应在确认上游已进入main后再重新指定base并检查diff。当前阶段不执行合并、改base、删除分支或重写历史。

## 4. 给 DeepSeek 的直接执行提示

```text
你负责执行 Cairn Jev v2 重构，不是重新写一份建议。

总计划：https://github.com/Alpenl/cairn-x-enricher/pull/3
完整索引：读取 plan/jev-v2-00-master 分支 docs/jev-v2/PR-INDEX.md。

先读仓库AGENTS.md、TypeSafe项目技能、REVIEW.md、README.md、EXECUTION.md和本批文档。
在相邻目录准备cairn-x-enricher与cairn-share，记录实际基线/工作区/两仓SHA。
依索引B01到B10执行126项任务，B08的基线与数据授权准备提前到B01之前。

直接在已建Draft PR的head branch补代码、测试和证据，不另建重复PR，不只修改计划勾选。
各批开始先同步同仓base最新实现，并锁定跨仓companion SHA；保留无关改动，不force-push。
按“核查当前代码→失败回归→实现→局部测试→完整测试→证据”工作。
R01–R39、每个Bxx-Txx、SC01–SC30都要追踪，不能静默删范围或用TODO/mock成功代替实现。
如果事实或设计与现状冲突，附源码和测试依据提出修订，不能盲从也不能暗改合同。

每批新增docs/jev-v2/evidence/Bxx.md，记录commit、测试命令/退出码、故障前后证据、调用计数和限制。
纯阈值/展示重放必须在禁网条件下证明模型调用数0。
人工标签与why/status保护、版本竞争、幂等、旧客户端不清v2、迁移/回滚必须有真实断言。
普通CI不调用付费模型。真实gold必须有人工来源，不能把模型输出或历史隐式确认当真值。
没有gold、live授权或设备时，完成可独立工作，明确BLOCKED_EXTERNAL和quality_verified=false，不伪造验证。
DeepSeek是实现者，不是要求替换生产Grok/Jev供应商。

不得擅自merge、转Ready、tag、远端迁移、部署Worker/NAS/App、收费调用或批量回填。
不能因为环境存在key就认为付费已授权，不能把私人收藏/密钥放公开PR/日志/截图。
扩展实体、补证据、重排和词表提案必须实现并离线验证；质量未过门槛保持默认关闭。

最终在B10提交FINAL-REVIEW.md及完整任务/场景证据矩阵，报告两仓最终SHA、每PR状态、未解决风险和外部阻塞。
全部PR保留Draft等待所有者与后续独立审查。完成的是代码和可审查证据，不是自动上线。
```

## 5. 完成后交回审查的材料

B10的 `docs/jev-v2/FINAL-REVIEW.md` 是交回入口，链接每批evidence与实际测试运行。它必须区分工程实现、真实模型质量和部署授权三个状态，并列两仓最终SHA、PR head/base、未验证项。不要只留言“所有任务已完成”。

本索引记录计划交付，不填写未来执行成绩。TypeSafe线上契约在本轮重访未成功，实施时必须重新核对官方API/限额/模型信息；已有来源和约束见REVIEW与B04-T01，不能从计划示例猜出线上真实响应。

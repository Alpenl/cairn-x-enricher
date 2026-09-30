# 从计划型 PR 迁移到真实 Issues

日期2026-09-20，工作流`issues-v1`。本记录描述跟踪结构迁移，不是代码实现/部署报告。用户批准彻底改用Issue驱动；保留E #3为设计文档PR，不合并main，不删历史分支，不改生产代码。

## 保留和替换

原始完整审查逐字节复制到[archive/REVIEW-20260920.md](archive/REVIEW-20260920.md)，blob `28597ba8e4335747fd3b207c08a4b3798b05bc57`。当前REVIEW保留R01–R39并指向现行任务。全部B01–B10/126任务完整写入真实Issue正文；SC01–SC30仍在EXECUTION。十批静态规格集中到本设计分支，无需再在旧分支找当前要求。

旧PR分支及原始文档提交保留，下表是创建迁移前已核对的历史快照。关闭旧PR只表示由Issue接管，既不是功能完成，也不是取消任务。

| 批次 | 原PR | 历史head SHA | 新Issue |
|---|---|---|---|
| B00 | E #3，保留 | 7bec2d4d5f8a9df9e01963a7fbe542ecfdb1dfb4 | [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10) |
| B01 | S #24 | 9ae990d3e25a7fb2f85a8a86c02cf5afbba2ff61 | [S #28](https://github.com/Alpenl/cairn-share/issues/28) |
| B02 | E #4 | 71dc763e586f321ef0747b3358b7afbf338ff38e | [E #11](https://github.com/Alpenl/cairn-x-enricher/issues/11) |
| B03 | S #25 | b70e5b5f63427f22f1b71ce6f4f9fa32d926f1a8 | [S #29](https://github.com/Alpenl/cairn-share/issues/29) |
| B04 | E #5 | d3abf4cc6cb7026a6128436c72cadb7fa0e231a4 | [E #12](https://github.com/Alpenl/cairn-x-enricher/issues/12) |
| B05 | S #26 | 8d8ae6f5c50549ddc86fc4e9574ab63d60422750 | [S #30](https://github.com/Alpenl/cairn-share/issues/30) |
| B06 | E #6 | 0dd10f34aeadd672e21299e637b1b3ae41ebf42d | [E #13](https://github.com/Alpenl/cairn-x-enricher/issues/13) |
| B07 | S #27 | 96291019c4fc60e957b638ae4164620e90c0aca1 | [S #31](https://github.com/Alpenl/cairn-share/issues/31) |
| B08 | E #7 | 439e0fc1c0075fce3c37c5af1d8dfff22fd3cf4e | [E #14](https://github.com/Alpenl/cairn-x-enricher/issues/14) |
| B09 | E #8 | fae75a6c1a1240b111fed2f11bebca8af0a085a2 | [E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15) |
| B10 | E #9 | 90d48a632938a871129b5c63621253a6e195de0f | [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16) |

E=Alpenl/cairn-x-enricher，S=Alpenl/cairn-share。使用上表repo+SHA+`docs/jev-v2/NN-*.md`可读取原始批次全文；旧PR正文和提交均是历史凭据。设计PR #3的上述SHA是迁移前版本，之后的设计提交不等于生产实现。

## 被彻底替换的规则

旧：先造全套占位PR、固定head/base链、在旧head补实现、每批等前批整份结束、任务进度在文档/PR重复维护。

新：1个总控+10个实施Issues；完整任务和状态只在Issue；真实代码变更才建PR；一个Issue可多个PR；默认main，只有真实未合并代码依赖才短堆叠；依赖精确到合同/实现SHA与验收条件；基线准备、独立P0、客户端/评估工具可并行；部分PR普通引用，不自动关闭批次。

PR-INDEX旧入口改成明确跳转，不保留可被误执行的旧链提示；README/EXECUTION/REVIEW及批次规格统一采用新流程。AGENTS添加本轮入口，原安全/技术要求保留。提供CODE-PR/ISSUE/EVIDENCE模板。静态规格不保留进度checkbox，避免双重事实来源。

## 元数据能力边界

实际创建的是GitHub Issues，不是Markdown假任务卡。当前接口没有原生sub-issue/dependency写入动作，已发现的本地环境也无GitHub CLI。因此总控清单、各Issue的回链/前置和ISSUE-MAP.json记录逻辑关系；**没有声称已设置原生Relationships**。不为这个限制更换项目工具或建立重复Issues。后续可在可用能力下补原生关系，任务身份保持。

## 核验与停止条件

在关闭旧PR前核对：真实Issue创建返回成功且有完整任务/验收；本目录文档提交已到设计PR；新旧映射正确、126任务与30场景无重编号；旧PR没有新增实现、评论中的未处理工作或状态变化。若检测他人新提交，保留该PR并重新协调，不直接覆盖。

关闭顺序从旧链末端向前，原分支和head SHA不变；给旧PR加入迁移通知/新Issue链接，标题明确被替代。只关闭这10个计划PR，不碰无关依赖PR、不关闭实施Issues。迁移后重新读取状态，核对文档PR仍open/draft、旧PR closed/unmerged、新Issues open及历史分支保留。

本轮不执行功能测试/付费模型/远端迁移/部署；文档和任务迁移检查不等于126实现任务通过。最终迁移核验结果在设计PR与总控Issue登记，不提前填未来执行成绩。

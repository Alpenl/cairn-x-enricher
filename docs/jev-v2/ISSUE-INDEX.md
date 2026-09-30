# Jev v2：Issue 总索引与 DeepSeek 入口

工作流`issues-v1`。总控：[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。设计PR：[E #3](https://github.com/Alpenl/cairn-x-enricher/pull/3)。本索引记录稳定任务映射，不维护执行进度，实时状态读取Issues。

全部39审查项、126任务、30场景保留。旧计划PR关闭只表示跟踪迁移，绝不表示任务完成或取消。历史分支不删除，也不再是编码起点。

## 实际 Issues 与旧 PR 映射

| 批次 | 实际Issue | 优先级 | 任务ID范围 | 旧计划PR（历史） |
|---|---|---|---|---|
| B00 | [E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10) | 总控 | R01–R39 / SC01–SC30 | E #3保留为设计PR |
| B01 | [S #28](https://github.com/Alpenl/cairn-share/issues/28) | P0 | B01-T01–T10 | [S #24](https://github.com/Alpenl/cairn-share/pull/24) |
| B02 | [E #11](https://github.com/Alpenl/cairn-x-enricher/issues/11) | P0 | B02-T01–T11 | [E #4](https://github.com/Alpenl/cairn-x-enricher/pull/4) |
| B03 | [S #29](https://github.com/Alpenl/cairn-share/issues/29) | P1 | B03-T01–T14 | [S #25](https://github.com/Alpenl/cairn-share/pull/25) |
| B04 | [E #12](https://github.com/Alpenl/cairn-x-enricher/issues/12) | P1 | B04-T01–T15 | [E #5](https://github.com/Alpenl/cairn-x-enricher/pull/5) |
| B05 | [S #30](https://github.com/Alpenl/cairn-share/issues/30) | P1 | B05-T01–T14 | [S #26](https://github.com/Alpenl/cairn-share/pull/26) |
| B06 | [E #13](https://github.com/Alpenl/cairn-x-enricher/issues/13) | P1 | B06-T01–T12 | [E #6](https://github.com/Alpenl/cairn-x-enricher/pull/6) |
| B07 | [S #31](https://github.com/Alpenl/cairn-share/issues/31) | P1 | B07-T01–T10 | [S #27](https://github.com/Alpenl/cairn-share/pull/27) |
| B08 | [E #14](https://github.com/Alpenl/cairn-x-enricher/issues/14) | P1 | B08-T01–T14 | [E #7](https://github.com/Alpenl/cairn-x-enricher/pull/7) |
| B09 | [E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15) | P2 | B09-T01–T14 | [E #8](https://github.com/Alpenl/cairn-x-enricher/pull/8) |
| B10 | [E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16) | P1验收 | B10-T01–T12 | [E #9](https://github.com/Alpenl/cairn-x-enricher/pull/9) |

E=Alpenl/cairn-x-enricher；S=Alpenl/cairn-share。Issue正文直接提供完整任务、范围、依赖、验收及证据要求，不必猜文档中的“完成”含义。

## 依赖与可并行边界

| 批次 | 接入/验收依赖 | 不应错误等待 |
|---|---|---|
| B01 | 基线保存和B08授权准备 | 不等待B08全部实现 |
| B02 | B01目标/幂等接口 | UI确认和ReadingResult可先做 |
| B03 | B01合同；与B02握手核对 | 不等B04/B05来定义通用存储 |
| B04 | B02运行边界+B03冻结合同 | 不等B05新词表，用fixture先开发 |
| B05 | B03存储+B04判断/投影合同 | 不等Web/Android |
| B06 | B04/B05接口 | 不等B09提取算法 |
| B07 | B05接口，与B06共享操作语义 | 不等Web所有页面结束 |
| B08 | 基线立即；工具接B03/B04，检索接B05 | 不等UI；无gold可先完成工具 |
| B09 | B03/B04/B05+B08离线工具 | 无gold不阻止工程，但不能晋升 |
| B10 | B01–B09交付及跨端合同 | 可提前搭集成测试，不提前填成绩 |

这些是已落实在Issue正文、总控和[ISSUE-MAP.json](ISSUE-MAP.json)中的逻辑依赖。**当前可用GitHub连接器没有原生sub-issue/dependency写入动作，本轮没有设置GitHub Relationships元数据；不要把普通引用声称为原生父子/阻塞栏。** 逻辑执行不依赖该UI元数据；以后有支持能力可据本映射补齐，但不得重建重复Issues。

## 直接交给 DeepSeek

```text
执行 Cairn Jev v2，不要重新写建议或恢复旧计划PR链。
总控：https://github.com/Alpenl/cairn-x-enricher/issues/10
读取总控、各实施Issue、仓库AGENTS.md、TypeSafe技能，及设计PR #3的
REVIEW.md、README.md、EXECUTION.md、ISSUE-INDEX.md和当前批次规格。
设计文档未合并时，读取plan/jev-v2-00-master的明确SHA；它不是代码依赖分支。
先固定两仓实际基线、工作区与现有测试，进行B08-T01准备。
领取无阻塞的Issue或可独立部分，从最新main创建真实实现分支。
仅有未合并真实代码依赖时使用短堆叠，并记录上游PR/SHA和companion SHA。
不向旧计划PR #4–#9、Share #24–#27追加实现，不再创建占位PR。
按核查→失败回归→实现→局部/完整测试→证据工作；一Issue可多个代码PR。
保留R01–R39、126项任务、SC01–SC30追踪；所有进度只更新Issues。
代码PR用普通引用和任务ID，部分完成不能自动关闭整个Issue。
每批交evidence/Bxx.md，列真实commit、命令/退出码、调用数、限制。
严格验证人工覆盖、版本竞争、幂等、零调用重放、旧新协议和迁移回滚。
没有gold、live授权或设备时列局部blocked_external，不造数据，继续独立工程。
任何设计偏离附源码/测试证据；不盲从旧结论，不暗减范围。
不擅自merge/转Ready/发版/远端迁移/部署/收费回填/删分支/force-push。
不把私人内容或密钥放公开GitHub；DeepSeek不是替换Grok/Jev的要求。
最终在B10 Issue #16交FINAL-REVIEW.md及两仓实际SHA、代码PR/测试矩阵，
由所有者/独立审查者验收并决定关闭Issues、合并或发布。
```

开始建议：基线与B08准备、B01和B02独立P0，再B03/B04/B05；随后Web/Android/评估可按合同并行；B09扩展后B10集成。分支数量由真实代码依赖决定，不由此顺序预先决定。

# B01 / Worker 权威目标与局部失效：设计规格

任务及实时进度：[cairn-share #28](https://github.com/Alpenl/cairn-share/issues/28)，完整B01-T01–T10在Issue正文。本文是静态设计，不是第二份进度清单。总控[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)，执行规则[EXECUTION](EXECUTION.md)。

## 问题和范围

审查基线S `5cf3b0d45c8fbae07d0fb5be1772e7d747c69b4b`。核查`worker/src/classification.ts`、`index.ts`、source/claim/complete/fail/retry及触发器；以Worker回归证明同词表旧新policy轮流重跑。聊天中的SQLite声明不能代替D1证据。新增迁移，不修改0009，不自动历史回填。

## 必须冻结的合同

```text
server desired target = immutable spec + requested model + policy + monotonic generation
claim = job(input revision, target generation, spec ID, lease token, expiry)
complete guard = same input + target + spec + valid lease + expected state
```

消费者只声明支持能力，不能由claim任意policy改变服务器目标。legacy消费者不能抢v2任务；灰度显式绑定限定job。回滚用新generation指向旧spec，不倒退generation。目标读取和写入防TOCTOU，不向App token开放管理。

同operation key及payload的complete幂等返回既有结果，不同payload冲突；links/job状态原子更新，响应丢失可查询，不直接重发模型。旧结果可留失效审计，不覆盖新投影或消耗新目标attempt。retry当前目标与主动换目标分开，活跃lease不随意抢占。

note-only保留快照/原文/译文/图片；legacy分类含note时可按旧语义失效，B03/B04后只影响个人维度。URL变化等待新source，但人工curation/why/status保留。typed冲突含target/input/lease/capability等原因，legacy shape或显式协商保护strict解码。

## 验收和依赖

A/B交替至少20轮不能重复领取完成目标；兼容consumer并发仅一个有效lease；推断途中改target/source旧完成失效；过期token不能回写；重复complete不双写；drop与无source不误入自动任务；legacy/v2开关安全；note不重抓而URL变更不删人工。

SC01/02/05/11/12/23/25/30；R01/R19/R22/R25/R38。运行Worker精确失败回归、npm test/typecheck/deploy:dry-run，空库与0009本地迁移、应用回滚；记录退出码、列值、计数、两仓SHA。SQLite可辅助，不能替代Worker测试。

B01先冻结合同供B02和B03使用，不等待整个后继完成。代码PR按回归/目标队列、局部失效、幂等错误拆分，部分PR不自动关闭Issue。交evidence/B01.md。回退保留新数据且不得恢复旧consumer抢新任务。未授权不merge/部署/收费。

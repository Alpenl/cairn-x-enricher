# B10 / 跨仓库验收与最终复审：设计规格

任务进度：[E #16](https://github.com/Alpenl/cairn-x-enricher/issues/16)，B10-T01–T12全文在Issue。总控[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。最终验收依赖全部B01–B09真实交付，集成框架可以提前做。本批不是发布授权。

## 证据链

```text
R01–R39 -> Bxx-Txx -> Issue -> implementation PR + commit/file
         -> SC01–SC30 -> executable test + command/exit + safe evidence
```

126项任务不因迁移或拆PR消失。实时状态只在Issue；证据矩阵带日期和两仓SHA，不再单独维护浮动checkbox。不能把旧计划分支SHA当实现，不用一项代码PR合并自动关闭整批。

## 集成验收

隔离Worker/D1/R2、Go和模型mock跑新收藏、旧原文、人工source、reading/classify失败、retry/replay/refresh、实体/补证据；断言来源先存、lease/revision、调用数、人工值、缓存。故障注入旧新consumer20轮、同spec并发、target/source变更、lease过期、complete响应丢失、重复提交、超时/401/422/429/过载/shutdown。

旧六字段/旧enrichment/v2 App、Go strict、NAS Web、Android对旧新Worker与flags验证；真实浏览器/可用设备执行字段编辑/CAS/dirty/分页/导出，编译不是设备运行。空库/0009历史、legacy人工/停用标签/实体/活跃lease迁移演练，核对row counts/hash/人工值/mapping及应用/target回滚，不清库或改旧迁移规避。

隐私删除/保留期覆盖全部快照/runs/events/entity/cache；token权限、公开日志fixture截图脱敏、SSRF/DNS/redirect/超大内容/HTML注入/预算到界。禁网replay模型0；flags列实现/测试/质量/默认状态，小fixture不证明大库性能。

## 文档与代码清理

ReadingResult独立、Evaluate/Decide/Resolve明确，taxonomy不混旧整包职责；legacy实验可跑不入生产。同步README、architecture、jev-classification、bookmark-management、deployment/cloudflare-backend、CLI/config、CHANGELOG；原始研究保留并标历史。

runbook只准备不执行：兼容Worker/新增迁移，再客户端，最后获批spec/flags；含备份恢复、版本兼容、generation回滚、监控/停止阈值、显式ID/cursor、dry-run预算。收费、远端迁移、生产切换各自需授权。

## 必须保持的不变量

权威target不循环；任一input/spec/generation/lease过期不覆盖；完成幂等；人工why/status不确认标签，reject/empty/reset不同；第四topic不丢；合法none成功；阈值/display模型0；依据真实；旧shape和隐藏v2保护；迁移/flag回退不丢人工来源；mock/gold分开；删除和外链安全。P0/数据安全不能用总体准确率抵消。

make verify/test-ablation及受影响architecture，Worker tests/typecheck/deploy:dry-run，Android CI等价门禁，加合同/浏览器/迁移/故障用例；真命令/退出码/环境/SHA，失败修复后重跑不删断言。

FINAL-REVIEW.md包含两仓baseline/final SHA、全部Issue与真实代码PR、R/任务/SC矩阵、合同/迁移/mapping、P0失败→通过、调用计数、工程/质量/外部阻塞、flags/rollback、偏离/风险和生产操作记录。提交evidence/B10.md并登记B10和总控，等待独立审查；缺数据/设备不能假成功，未授权部署不等于代码没完成。必要修复回到相应真实PR再更新依赖，不在末端偷偷补上游后倒写进度。

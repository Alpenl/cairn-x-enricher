# B07 / Android 多维整理与兼容：设计规格

任务进度：[S #31](https://github.com/Alpenl/cairn-share/issues/31)，B07-T01–T10全文在Issue。依赖B05后端合同，与B06共享交互但不等待整套Web结束。先搜索实际android/app/src文件，不凭名字猜路径，不升级无关依赖。

## 客户端合同

Kotlin DTO兼容旧单值form/use与v2多维、status/origin/revision/candidates/entity；未知状态不当已确认。能力协商v2，summary/详情按需。cache分representation/revision，账号/服务器/schema/分类/整理更新正确失效，旧缓存不硬转v2。

阅读分有效值、AI候选与人工来源，第三主题只是折叠；carrier独立，潜在用途不同于用户动机，空/未运行/失败/stale区分。字段/tag accept/reject/empty/reset带operation及expected revision。why/status无隐式accept，legacy未知不升级gold。

失败保草稿、幂等重试；跨Web修改后旧revision不覆盖，冲突由用户重应用。离线队列存动作而不是完整旧对象，账号/服务器切换不误发。普通阅读不收费；内部目标/model/迁移权限不能借App token绕过。仅能力允许的retry/replay可操作。

多维过滤、搜索、Markdown导出完整字段/why/source/partial，实体状态准确，依据只真实block/URL且安全打开。导航/分页/快速搜索/旋转/进程恢复不丢操作或重复提交，大字体/小屏/可访问和既有图片译文不退化。

## 兼容矩阵

旧六字段→新Worker原shape；旧enrichment→v2已有数据投影合法且不抹隐藏值/reject；新App→旧Worker或flag off安全降级/只读；新→新字段/revision/三状态正常；Web/Android并发CAS/幂等；旧缓存离线稿遇schema/source变化不全量反写。v1 classification:null、空use、第四topic与隐藏多功能均需测试。无法表达旧写入按B05冲突或范围适配，不猜用户删除意图。

## 门禁与交付

SC03/07/13–16/19/23–25/28，R03/R06/R23/R26–R30/R37/R39。核对当前CI后使用仓库指定JDK/SDK运行unit/lint/build与androidTest编译，Worker tests/typecheck/deploy:dry-run。真实设备/模拟器测试独立报告；编译通过不是设备执行，缺设备明确blocked_external。

合成数据截图日志，交evidence/B07.md、实际两仓SHA、所有实现PR。DTO/cache、动作并发、显示过滤/兼容可独立PR；部分不关闭总Issue。flag回退不删除服务端v2历史，不显示未写入的保存成功。未授权不签名发布/merge/部署/收费。

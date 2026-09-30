# B07 搜索写入竞争与凭据持久化顺序 · 2026-09-22–23

受测 Share 源码 `176c1aa63197f0d52cee22d72a32a630763651cb`。本批修改 Android；Worker/Go 生产代码沿用 [上批重置验收](B07-20260922-reset.md)。推进 B07-T02/T05/T07，完整原范围仍未完成，不关闭 S #31 或总控 #10 / 验收 #16。

## 修复

1. **确认写入后的搜索一致性。** 搜索请求绑定账号与搜索代次。v1 整理、编辑、学习状态、删除、批量学习、上传成功和 v2 动作确认后，取消旧搜索，从当前关键词/筛选的第一页重新查询，清掉旧 cursor。沿用 250ms debounce 合并连续确认，过期请求不发布。重新查询让服务端决定修改后是否仍满足条件，避免仅替换值却保留错误的查询成员；失败或离线排队不冒充确认。
2. **凭据写入与通知分开。** DataStore 同事务保存 Token 与单调的 `api_token_revision`，旧安装缺少 revision 时按 0 读取。ViewModel 保留最新本地意图、串行写入，跳过尚未执行的旧意图；忽略低于已确认版本的旧通知，同时继续应用其他设置。设置写入确认后重新处理最新已观察的持久值，避免漏掉确认返回之前已到达的更高版本外部设置。没有把 Token 本身写入新增诊断日志。
3. **保存失败可见且可重试。** IOException 不把当前会话悄悄切回旧值。设置页持久显示保存失败，直到下一次保存；普通上传队列提示不再遮掉该状态。重试成功后落盘，重建 ViewModel 从持久设置恢复新 Token。失败前保存在磁盘的旧 Token 未被伪称为已更新。

## 直接证据

| 检查 | 实际结果 |
|---|---|
| 搜索旧行为 | 在固定 S `a3794e4` 加入真实 Android/HTTP 用例，主列表仍为 `confirmed edit`，迟到搜索却变成 `before edit`：[日志](logs/20260922-search/b07-search-baseline.log)、[XML](logs/20260922-search/b07-search-baseline.xml)、[仅测试补丁](logs/20260922-search/b07-search-baseline.patch) |
| 旧账号通知 | 使用真实文件 DataStore，仅延迟其 B 通知。A→B→A 已完成后放行旧 B，旧逻辑产生 generation 5，期望 3：[日志](logs/20260922-search/b07-credential-baseline.log)、[XML](logs/20260922-search/b07-credential-baseline.xml) |
| 额外时序缺口 | 初版版本过滤仍丢失“新外部值先到、旧写入确认后到”的更新，UI 停在 A：[失败日志](logs/20260922-search/b07-credential-newer-before-ack-final.log)。最终增加确认信号后重新评估最新持久值，该断言通过 |
| 最终 Android 门禁 | [严格 unit/lint/build + connected + 实际 Worker 链](logs/20260922-search/b07-search-final-verified.log)通过；[70 unit](logs/20260922-search/unit-results.json)，[API26 XML](logs/20260922-search/local-api26.xml) 33 项 = 23 通过、10 skip、0 failure/error |
| 新普通设备场景 | 搜索期间编辑；删除只存在于搜索而不在主列表的条目；分页期间修改导致不再匹配；延迟旧 Token、快速 B→A→B→A、旧无版本设置、确认返回前的新外部设置；存储失败、其他设置通知、重试和重建恢复。全部使用真实 ViewModel/DataStore/HTTP，服务响应由 MockWebServer 控制 |
| 实际 Worker 设备链 | [十份阶段日志及传输记录](logs/20260922-search/local-worker/)。新增第十阶段实际执行 App→Worker/D1：原 llm 搜索匹配，离线清空只成草稿、搜索仍匹配；联网提交 personal revision 3 后搜索重新查询并移除该条目。十个阶段均独立 Android 进程且 `OK (1 test)` |
| 远端固定提交 | [代码 CI 35750534464](https://github.com/Alpenl/cairn-share/actions/runs/35750534464) 的 Worker/Android 均 SUCCESS；[设备 35750352361](https://github.com/Alpenl/cairn-share/actions/runs/35750352361) API26/35 均 SUCCESS，精确 HEAD 与顶部一致。已核对 [API26](logs/20260922-search/api26/) / [API35](logs/20260922-search/api35/)：各普通 XML 33 项 = 23 通过、10 skip、0 failure/error；各有十份独立阶段 `OK (1 test)` 日志与传输记录。CI 同时复验既有 186 Worker 项，未把下一批隔离失败用例混入该提交 |

普通 connected 的十条 skip 不算真实业务阶段执行；实际 Worker 阶段由单独 harness 执行。模拟器不等于物理真机。底层 Worker/Go 没有为通过本批场景作特殊处理，没有模型调用。

测试开发中，临时 `data.first(predicate)` 订阅曾错过已经完成的写入。诊断明确记录 B、A 写入都已返回：[记录](logs/20260922-search/b07-credential-order-diagnose.log)。改用已有测试采用的持久快照轮询同步前置条件，仍限制等待时间，未放宽最终账号/版本断言。另一个失败揭示临时消息覆盖保存错误：[记录](logs/20260922-search/b07-search-final-gates.log)，最终以持久设置页状态修复。DataStore 仍为既有固定 1.1.1，本批没有升级依赖。

## 未完成范围

- [B07 原十条审计](B07-20260922-scope-audit.md)继续有效。此次确认写入失效不代替完整 App/Web 并发、全部新旧/flag-off、导航/分页错误/无障碍/图片译文及所有详情竞争矩阵。
- `selectionFilterSQL` 仍未接入实际列表路由，第四主题、隐藏维度与实体状态的完整实际筛选仍需实现和验收。新真实服务场景只证明已有单 topic 筛选在确认后重新查询，不能当作完整多维筛选通过。在固定 S `176c1aa` 的另一隔离工作树仅增加 [实际 HTTP/D1 复现用例](logs/20260922-search/b05-filter-reproduction.ts.txt)，[四项均失败](logs/20260922-search/b05-filter-known-failures.log)：第四主题漏匹配、多维条件被忽略、实体 failed 条件被忽略、非法 v2 条件未拒绝。这些待修回归没有混入本批运行时代码。
- 普通上传队列仍需完整账号归属/跨进程审计；本批修复其成功回调对搜索的失效，不声称改变旧上传队列的存储归属。
- 真实 block/URL 来源打开与导出、实体人工 UI、来源/阅读/分类能力生命周期、B08 policy/消融/检索/dev/holdout、B09 全局预算/收益以及全部原 126 B 子任务与 R/SC 继续保留。

本批 **0 次付费调用**，历史 B08 43 次不变；无需人工标注，dev/holdout 未使用。没有部署、合并或执行生产迁移，PR 继续 Draft。

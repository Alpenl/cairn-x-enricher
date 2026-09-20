# B06 / Web：少打扰的分类、字段级整理、依据与独立状态

状态：待实现。总计划#3；base B04 #5；跨仓依赖 https://github.com/Alpenl/cairn-share/pull/26 （B05）。覆盖R06/R08/R09/R26–R30/R32/R37。保留现有原生JS/Go同源架构，不为了重构引入整套前端框架。

## 范围

`internal/dashboard/reader.js`、`reader.html`、`home.js`、`common.js`、`backstage.js`、`dashboard.go`、样式与前端/浏览器测试；`internal/cairn`对应v2 DTO和代理。先同步B04代码和B05固定contract SHA，旧页面/旧Worker降级路径不得破坏。

## 交互目标

默认显示已经可用的结果：主题、内容功能、载体、潜在用途，人工与AI来源可辨；边缘候选按需接受/忽略。概率和原始判断默认收起。一个模糊候选不挡阅读，不迫使用户审核整条。inbox/kept/compiled/drop、why和用户意图都是独立人工行为。

## 任务

- [ ] B06-T01 更新Go同源代理与DTO，用显式v2协商获取summary/detail/classification view。旧strict JSON路径保持原shape，缓存key包含representation和revision。模拟旧Worker/暂不支持v2时给安全只读或v1降级，而非清空数据。
- [ ] B06-T02 阅读页拆展示与编辑：有效标签、AI候选、人工覆盖、来源适用版本分别呈现；默认不堆概率。标签超过3折叠可展开，不能禁止勾第四个有效标签。inactive/history标签可见且说明。
- [ ] B06-T03 实现字段/tag级accept/reject/set-empty/reset，payload带operation ID、expected revision和适用来源。只改某一字段就只提交该字段；B02的why/status独立保存不退化。明确清空与恢复自动不同按钮/解释，拒绝后的候选不自动复活。
- [ ] B06-T04 CAS冲突与dirty表单：轮询或其他客户端更新不覆盖用户草稿；冲突显示实际差异并允许重新应用明确操作，不无条件last-write-wins。失败保留草稿，重复点击/重试幂等，reset也不丢未保存why。
- [ ] B06-T05 依据展示从snapshot block/span引用渲染，注明原帖/续帖/引用/外链/第三方/legacy unknown。找不到block时显示不可用，不生成似真的说明。概率只能标“模型判断”，不显示未经校准的准确率。引用不展示为用户立场。
- [ ] B06-T06 区分任务状态与字段原因：正文已存阅读待生成、分类pending/processing/failed/exhausted、字段abstained/not_applicable/stale、人工inbox等。合法空值不显示红色失败。后台提供分类错误汇总和按reason筛选，不只复用旧enrichment status。
- [ ] B06-T07 分开“只重试分类”“用当前策略重算”“刷新原文”，每种动作标清是否调用模型；付费/扩大范围操作明确确认、有界ID集合。按钮不把一次分类失败变成重新X Search，活跃任务防重复，返回409按reason解释。
- [ ] B06-T08 首页多维筛选与URL状态同步、前进后退恢复、AND/OR与B05合同一致。实体/来源/人工状态可组合，搜索高亮采用安全text节点，防XSS。新旧请求竞争只显示最新结果，分页稳定，空结果与服务错误不同。
- [ ] B06-T09 导出单条与已加载集合，包含全部有效v2字段、人工why、source、AI/人工来源、stale信息和partial计数；不把折叠的第四个tag丢掉。未加载结果提示仍保留；导出不改status、不发模型请求。
- [ ] B06-T10 实体视图区分not_run/failed/empty/stale；先基于B05通用接口完成展示与人工修正，不需要B09实际提取后才写UI。重排/补证据/词表提案入口按capability和feature flag隐藏或显示明确未启用，不能假按钮返回成功。
- [ ] B06-T11 可访问与移动端：键盘焦点、aria live状态、长标签换行、屏幕阅读器错误关联、320/375/768/1440视口、reduced-motion；不在小屏把关键操作挤出。遵循现有CSS视觉，不大规模顺带改版。
- [ ] B06-T12 新增零依赖前端逻辑测试与真实浏览器交互用例，运行make verify、浏览器测试并保存合成数据截图/trace；日志和artifact不能含私人收藏。提交evidence/B06.md，向B07传递同一操作语义和fixture。

## 浏览器验收脚本要求

至少自动完成下列流程，并断言网络请求体/调用计数，不只截图：

1. 打开未确认记录，只保存why；classification/accept事件均未发送。
2. 两个明确topic加一个候选；候选reject后重放policy，拒绝仍保留，正文可读。
3. content_functions多选且carrier独立；底层四topic在折叠、详情、导出均完整。
4. set-empty后刷新仍为空；reset后恢复自动建议；两个操作的payload不同。
5. 保持dirty表单时后台轮询更新；草稿不丢；第二客户端修改后CAS冲突可解释。
6. 分类失败只retry classification，X Search mock调用=0；policy replay模型调用=0。
7. 依据跳转只到有效block；引用/评论角色正确；恶意原文不成为HTML或脚本。
8. pending/failed/not_applicable/not_run/stale显示不同；合法none不红色报警。
9. 筛选往返/分页/快速连续搜索没有旧结果覆盖；partial export有数量说明。
10. 键盘和手机视口可操作全部关键控制；旧API fallback不写坏v2数据。

对应SC03/06–08/13–16/19/21/25/28/30。静态JS检查通过不等于真实浏览器通过；无浏览器环境要记录阻塞，不能以截图描述替代操作。

## 提交与回滚

建议：①DTO/代理与fixture；②字段编辑与并发；③依据/状态/重做；④筛选/导出/可访问；⑤浏览器与证据。UI v2受capability flag控制，回退v1展示不删除v2存储或人工事件。禁止自动确认所有AI、用全局uncertainty筛掉完整记录、把个人stance当普通机器标签。

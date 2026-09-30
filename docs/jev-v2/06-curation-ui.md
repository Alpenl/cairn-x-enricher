# B06 / Web 字段级整理与依据：设计规格

任务进度：[E #13](https://github.com/Alpenl/cairn-x-enricher/issues/13)，B06-T01–T12全文在Issue。接口依赖B04/B05，共享fixture可提前做UI，不等B09算法。保留原生JS与当前视觉体系，不另换框架。

## 展示与编辑

Go同源代理/DTO显式协商v2 summary/detail/classification，旧strict路径shape不变，cache带表示/revision；旧Worker安全v1或明确只读，不清数据。展示有效结果、AI候选、人工覆盖、适用来源版本，概率默认收起，第四topic仅折叠不删除，停用历史可见。

逐字段/tag accept/reject/set-empty/reset，operation ID、expected revision与来源贯穿。why/status独立保存，未触摸建议不确认；empty与reset不同。轮询不覆盖dirty草稿；CAS显示差异、让用户重应用，失败保草稿，重复请求幂等；reset不丢未存why。

依据只回真实block/span，标原帖/续帖/引用/外链/第三方/unknown；缺依据明确不可用，不生成似真的解释。概率不冒充准确率，引用不冒充用户立场。原文安全text渲染、防XSS。

## 状态、动作与检索

来源/阅读、分类任务、字段原因、人工inbox等分别表达；合法none不是红色故障。后台有分类队列与原因筛选。只分类retry、仅policy replay、刷新原文分开，标清调用/收费，有明确确认、ID上限及防重；409按reason解释，不把分类失败升级X Search。

多维筛选与URL/历史导航同步，同维OR/跨维AND契约一致，快速搜索只显示最新。分页稳定、空结果不同于错误。导出全部有效v2、why/source、origin/stale/partial数量，不丢折叠标签且零模型调用、不改人工状态。实体not_run/failed/empty/stale正确；扩展入口按capability/flags，不做假按钮。

键盘焦点、aria-live、错误关联、长标签换行、320/375/768/1440视口及reduced-motion都有验证，小屏关键操作保留。

## 浏览器验收

未确认记录只why无分类请求；reject后重放不复活；四topic/多功能/独立载体在展开与导出完整；empty与reset刷新行为不同；dirty轮询/CAS不丢草稿；分类retry X Search=0、replay模型=0；真实证据跳转及注入安全；各状态不同；筛选往返/分页/请求竞争/partial准确；键盘手机与旧API降级可用。

R06/R08/R09/R26–R30/R32/R37；SC03/06–08/13–16/19/21/25/28/30。零依赖前端测试、make verify及真实浏览器断言网络体/计数，合成截图trace；静态测试不是浏览器执行。提交evidence/B06.md与两仓SHA，交互fixture给B07。多个代码PR普通引用Issue，复审后才关闭。回滚UI不删v2人工数据。

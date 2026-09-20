# B09 / 实体、补证据、重排与词表提案：设计规格

任务进度：[E #15](https://github.com/Alpenl/cairn-x-enricher/issues/15)，B09-T01–T14全文在Issue。依赖B03/B05后端、B04判断与B08离线工具。无gold可继续实现，但不默认启用。四项能力应按可审查改动分多个代码PR，不因原批次号塞成一个巨大PR。

## 实体

候选先从存档text/links解析surface/project URL/精确span；确需开放提取使用独立获授权生成能力，不让Jev造名字或重新抓X。Jev只做实质相关/偶然提及/有限canonical匹配，有none/unknown，同名无证据不合并，候选外值拒绝。

实体not_run/failed不覆盖同revision成功值，completed_empty明确；source变stale，人工优先、提交幂等、缓存索引同步。surface与canonical来源分开，UI状态通过B05合同表达。

## 按需补材料

仅有明确缺口（未取得外链、关键图文缺失、可观察截断）触发，不把合法none/边界歧义送强模型。记录why_needed/source/scope/budget，优先人工入口；无能力/授权明确blocked。

至少一个真实受控外链适配器，不只空接口。允许名单、实际连接地址、DNS重绑定、私网/loopback/link-local/metadata、IP/端口/scheme、每跳redirect、MIME/大小/timeout检查，禁止跨域凭据和正文指令驱动任意URL。测试用模拟DNS/本地server，不访问真实metadata。

图文字等适配器仅能力已存在且获授权；转录/译文标方法、可靠性与来源，不覆盖原帖，不声称Jev看图。取得内容才追加block/update revision/受控重评，重复内容幂等，失败保已有可读内容，有限attempt与预算。

## 重排与治理

先用B05权限过滤后的有界候选，再同query/rubric做相关性判断，稳定ties；不能比较不同Noul命题当全局重要性。失败/off/超预算回原序，不改变集合/权限/筛选。跨页冻结候选session或明确当前范围，cache含query/filter/候选content revision/spec/model，私人query不公开。

词表缺口由有依据词表外/人工纠正/混淆对提案，含定义/反例/计数/影响mapping；unknown原因不硬当词表外。人工批准/拒绝，批准有diff/稳定ID/version/影响dry-run/rollback；display-only重显，定义变化受控重评，未批准不改词表、不自动造空标签/全库队列。

## 预算与验收

每扩展独立flag默认off，单条/整体token和调用预算、timeout/取消/恢复/去重；用户材料和模型不能加额度。普通分类不因扩展未开失败。用B08分别off/on评估实体precision/span/canonical误合并、补材料收益/成本/重复、候选召回与rerank、p95和预算耗尽，无gold只报工程。

SC10/17–21/24–26/29–30；R12/R14–R17/R26/R30–R33/R38。恶意URL/注入/超预算/幂等/分页/实体状态/未审批词表均有断言，make verify与合同集成；交evidence/B09.md、flag/provider/unsupported范围/调用数和实现PR。关flag不清历史或人工提案，重排回原序，补材料仍可人工。未授权不收费/merge/部署。

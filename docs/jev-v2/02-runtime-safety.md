# B02 / 显式确认、阅读契约与运行安全：设计规格

任务与实时进度：[E #11](https://github.com/Alpenl/cairn-x-enricher/issues/11)，完整B02-T01–T11以Issue正文为准。前置[S #28](https://github.com/Alpenl/cairn-share/issues/28)的握手合同；UI确认及ReadingResult可以先独立实现。总控[E #10](https://github.com/Alpenl/cairn-x-enricher/issues/10)。

## 行为边界

`reader.js`的未reviewed条件不能把保存why/status变为确认全部AI。原因、状态、标签分别维护dirty；只有主动确认或主动改标签提交分类。取消、恢复自动、网络重试不偷带分类；失败保留草稿和人工优先。先构建请求体失败回归，再改交互。

`internal/enrich/source.go`建立真正独立ReadingResult/schema/decoder/validator，不从旧整包schema删字段拼装。模型只生成标题、译文、摘要和确需生成的语言；原文、链接、图片由source snapshot注入Completion。不重复输出source，也不放松JSON、标题/长度、遗漏/尾随内容检查。超长不能静默截断，旧Generate/Workflow只保留实验。

## 运行合同

serve/once/classify/人工source遵循B01的capability/target/job绑定。错误区分configuration/auth/contract、可重试网络/限流/过载、stale输入/目标/租约及已完成；409按reason判断，不一概忽略。单一持久化退避，Retry-After/jitter/总预算均有上限。配置错暂停组件而非耗尽所有job；liveness不依赖外部服务。

完成响应丢失使用同operation ID重试/查结果；不要盲目重复付费评估。superseded不计语义失败且不终止其他任务。每job deadline、shutdown等待有界，停止领取新任务。classify只校验实际用到的Worker/Jev配置，不被未使用Grok self-test阻断；root/help零调用。

## 验收

只why/status的PATCH没有classification/accept；主动确认提交且失败保草稿；已存长文图片进入Completion完整；畸形/遗漏/尾随JSON报错；缺Grok配置仍可只分类；401/422组件暂停与429/超时退避分开；提交响应丢失不重发Jev；来源/目标变更旧结果不覆盖；shutdown有界。

回归source-first：reading失败保source、分类失败X Search调用0、note不重抓、阅读完成不写分类、人工整理0模型调用。SC02–05/10–12/25/27/28；R23–R25及R01/R19/R22/R37/R38。

make verify、test-ablation和受影响实验，真实浏览器断言网络体及dirty轮询，交evidence/B02.md与两仓SHA。可拆确认/阅读/握手运行等代码PR，不在旧计划分支继续。全部Issue验收后交审，不能以toast或文档CI冒充修复。回滚保护source和人工值，不恢复混合consumer漏洞。

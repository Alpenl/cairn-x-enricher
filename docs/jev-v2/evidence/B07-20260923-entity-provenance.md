# 2026-09-23 Android 实体身份与来源依据

源码 S `6e22cda886a10ab9ff755f60a3483c6d79eac094`；E 运行时代码沿用 `dd6bcfa1ed22aa89c2b0ffcfe4cba120bc4cf34f`，本批 E 只补证据。推进 B07 的真实状态/来源展示、B09-T03/T04 和 G02/G06，统一进入 [#16](https://github.com/Alpenl/cairn-x-enricher/issues/16)。这是 Android 读取闭环的局部工程交付，不表示整个 B07/B09 或 235 项验收完成。

## 原行为与修改

上一批已在实际消费者、Worker 存储和 Web 接入[有限身份与逐处来源](B09-20260923-canonical.md)，Android 仍只能读取表面名与总体运行状态。相同的实际 Worker/D1 读取测试在旧 S `5c5d583` 失败：成功保存实体判断后，App 的 `v2-selection?include_state=1` 没有 observations/version。见[旧实现对照](logs/20260923-android-entity/android-entity-baseline.log)，并非编译失败或鉴权失败。

现在 selection 的**同一条 SQLite SELECT** 同时取得实体判断、当前来源版本和人工整理结果，不增加第二次异步读取或可变投影。`state.entities.observations_version=1` 提供最多 40 处已经保存的显示记录：原文名、片段/rune 区间或已有链接、relevant/incidental/none/unknown、有限身份 ID/名称/类型、身份 URL 依据和目录版本。私有缓存请求、全目录候选和原始概率不进入此显示合同；原完整记录仍保存在 0030 的 observations。旧调用不传 include_state 时响应形状保持原合同，无新迁移。

`effective` 使用同一次读取中的来源有效性、成功状态、相关性和人工优先结果计算。人工排除后、来源过期后，记录可以继续查看但标记“非当前有效结果”；恢复自动后重新生效。同名的两处不同身份分别显示 ID，第三处无身份依据保持“身份未确定”，不会因名称列表去重而混为同一身份。

Android 在原 SelectionState 上做可选解码。未知合同版本、非法偏移、身份/依据 URL 错误、重复位置或畸形数组使依据不可用，保留合法整理结果；旧后端不会被升级成已有依据或人工确认。客户端另外核对来源版本、状态、相关性和有效名称，不能仅凭服务端一个 effective=true 把已过期或人工排除记录导出。

详情新增可滚动的“查看实体判断依据”对话框，显示每处相关性、身份、来源及当前有效性。所有内容为文本，不执行来源链接，不发送推断请求。完成但无相关实体时总状态写“已完成，暂无相关实体建议”；具体的 none、unknown、incidental 在记录内分开显示。复制整理只附加**当前有效**实体的身份/来源片段，不导出已排除或过期身份。这一批没有增加 Android 人工实体操作按钮、模型重跑入口或完整 raw/model/usage 历史导出。

## 验证

| 证据 | 结果与边界 |
| --- | --- |
| [Worker 全量](logs/20260923-android-entity/android-entity-worker-full.log)、[dry-run](logs/20260923-android-entity/android-entity-worker-dryrun.log) | 277 项 / 24 文件及 typecheck、打包通过。新实际 D1 测试逐项比对与 Android 共用的实体显示夹具，并验证人工排除/恢复、来源 stale、旧查询形状及私有字段不进入显示合同。全量日志从工具首次 yield 后续输出保留，退出码 0；包含原 workerd reset 警告。 |
| [Android 最终门禁](logs/20260923-android-entity/android-entity-verify-final.log) | 76 项 unit、lint、assembleDebug、AndroidTest 编译通过。 |
| [本地 Android 页面](logs/20260923-android-entity/android-entity-device-pass.log) | API26 的 7 项整理区测试通过，包含新两个身份/未知来源、stale 历史与 Activity 重建。使用 MockWebServer，不能当作真实 Worker 全链。最后仅修改空结果文案，固定源码的完整设备矩阵另列。 |
| [实际 Worker/D1 + Android](logs/20260923-android-entity/android-entity-device-worker.log) | API26 的 14 阶段全部通过，第 14 阶段使用真实 Android 客户端读取持久判断，实际 CAS 人工排除/恢复后验证当前身份及导出。初始判断由明确的合成 SQL 夹具写入，不把它当作消费者推断或质量证明。 |
| [跨仓真实服务](logs/20260923-android-entity/android-entity-real-services.log) | 19 场景通过，保留实际 Go CLI、实体目录/缓存、分类、预算、来源、删除等回归；模型边界为本地 HTTP 夹具。E 运行时没有修改。 |

源码 [CI 35804777565](https://github.com/Alpenl/cairn-share/actions/runs/35804777565) 在上述固定 SHA 成功，含 Worker 与 Android 普通门禁；[元数据](logs/20260923-android-entity/source-ci.json)。本地固定源码完整 API26 [日志](logs/20260923-android-entity/android-entity-device-all.log) / [JUnit XML](logs/20260923-android-entity/android-entity-local-all.xml)：37 普通测试成功，14 个需独立实际 Worker 的方法在普通任务中 skip，随后均在独立阶段执行成功。Gradle 进度文本重复计入 skip 显示 65，实际 XML 为 51 个 testcase，不能当成 65 个通过。本地 API26 字体倍率 1.5 的两个新增页面测试也通过，[日志](logs/20260923-android-entity/android-entity-large-font.log)；运行后恢复原设置。固定源码 [设备矩阵 35804785817](https://github.com/Alpenl/cairn-share/actions/runs/35804785817) 在 API26/API35 均成功：各 37 普通测试 + 14 实际 Worker 独立阶段；[元数据](logs/20260923-android-entity/device-ci.json)、[计数核对](logs/20260923-android-entity/device-summary.json)、[API26 XML](logs/20260923-android-entity/api26/connected.xml)、[API35 XML](logs/20260923-android-entity/api35/connected.xml)。两端新实际实体阶段：[API26](logs/20260923-android-entity/api26/readEntityProvenanceAndHumanOverridesFromRealWorker.log)、[API35](logs/20260923-android-entity/api35/readEntityProvenanceAndHumanOverridesFromRealWorker.log)，其余逐阶段日志保存在各自目录。

## 失败、修正与剩余范围

最初两项新 UI 测试在打开依据后找不到身份文本；增加等待仍失败。节点诊断显示按钮位于屏幕之外（y=3245），因为整个整理区是一个很长的 LazyColumn item，`performScrollToNode` 只让 item 进入组合树。修正测试为进一步 `performScrollTo().assertIsDisplayed()` 再实际点击；没有改为直接调用语义动作，也没有删除可见性/身份/重建断言。[首轮日志](logs/20260923-android-entity/android-entity-device-focused.log)、[等待仍失败](logs/20260923-android-entity/android-entity-device-second.log)及[可见性诊断](logs/20260923-android-entity/android-entity-device-debug.log)保留。初次本地模拟器无 KVM 会话权限，用现有 kvm 组启动后成功，未修改设备权限。

本批验证的是展示合同和生命周期，不能证明真实实体召回、canonical 误合并率、自动参考质量、阈值或端侧全量可访问性。B08 继续按用户授权使用可追溯 automatic_reference，不要求人工标注，也不冒称 human gold。全历史保留/重建、词表治理、真实质量及独立复审仍须完成。完整 [235 项范围](B10-20260923-scope.md)保留；新增付费 0、累计 211，冻结 dev/holdout 未用于推断、拟合或评分。未生产部署/迁移、启用扩展、切换目标、merge、Ready 或关闭 issue，两 PR 仍 Draft。

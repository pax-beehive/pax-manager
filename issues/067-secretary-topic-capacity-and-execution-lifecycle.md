# ISSUE-067 Secretary 多摘要容量与执行恢复规则不完整

**Status:** partial
**Severity:** high
**Component:** Secretary / 摘要模型 / 执行恢复
**Found:** 2026-11-03
**Resolved:** -

## 问题

旧版一场会话只有一份持续更新的摘要，coverage 不断累积，容易遇到 4096 项来源和 64 KiB
容量限制。执行恢复主要从聊天记录推断；收到输出、真实失败、未知终态与停止重试没有完整分离，
可能反复领取无效结果或无法判断是否应重发。

## Current note

2026-11-04，本次变更已经实现 summary-only Node：独立身份、每来源多摘要、不可变版本、
当前指针和精确来源；沿用原有快照，不建立消息 revision 或逐 chunk 表。
当前 coverage、处理进度和历史来源目录分离，修订可压缩旧细节而不抹掉历史。
topic、普通弱关联、合并和 board 继续延后，见[中文设计](../docs/secretary_topics_design.md)。

执行使用 `secretary_jobs`、`secretary_batches`、`secretary_attempts` 三层。
固定输入恢复时重新授权；删除/撤权后不返回旧混合正文，跨 session 来源失效会安排原整理来源修订。
先保存输出再校验/提交，重启可恢复同一结果；未知原生执行不重发，固定批次最多三次实际失败，
未发生模型调用的 prompt/schema 超限直接阻塞。摘要、来源、处理结果、checkpoint 和 ES outbox 原子提交。

完成证明加 source idle/静默门槛，tools/thought 排除，用户全文与 assistant 每消息 last 为默认视图。
已有摘要页 64 KiB，单大项改为 requires_read 目录头，显式读取确切整版后才算已读；累计读取 4 MiB。
迁移只补 Node 身份/目录，不清空 checkpoint、不启动全量重算或模型迁移。
Console 已支持多成果、历史与原文导航；paxd 新增第五个只读 summaries 工具，部署需配套版本。

已用 memory 和独立真实 PG 契约验证生命周期、原子提交、权限恢复和完成门槛，
局部新增 lifecycle/access/helper 覆盖达到 80% 要求；Console 定向测试及本地合成 API 浏览器检查已运行。
完整格式/lint、真实 PG/ES 搜索与历史验收、758 项 Secretary 检查及真实 binary 五工具/原子发布均通过。
标准 integration 的最终结果在交接中记录；旧 Hermes 容器 fixture 的独立基线限制见问题 068。
本 issue 不因本地通过就标记线上修复完成。
尚未部署核验，也未用真实长对话评估摘要拆分和保留结论的质量。

## Remaining

- 完成统一验证及必要修复，审查迁移与 rollout，再合并部署。
- 用真实对话验证多摘要质量、长历史与异常数据处理，不把协议测试当质量评估。
- [问题 064](064-secretary-stale-upstream-background-state.md) 的上游运行状态问题仍独立存在。
- [问题 066](066-secretary-startup-requeues-completed-sessions.md) 只解决启动重复入队，继续保持 resolved。

当前路径与命令见[运行说明](../docs/secretary_automation.md)，
交接见[实施交接](../docs/handoff_20261104_100000.md)。

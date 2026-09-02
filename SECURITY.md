# Security Policy

## Supported Versions

仅最新 GitHub Release 接收安全修复。

## Reporting

请使用 GitHub 仓库的 Private vulnerability reporting，不要在公开 Issue 中提交凭据、收藏 URL、原帖内容或可利用细节。

## Secrets

- `.env`、`XAI_API_KEY` 和 `CAIRN_ENRICHER_TOKEN` 不得提交到 Git、复制到 Docker build argument 或附加到 Release。
- Worker 内部 token 必须与 App 的 API token 分离，并定期轮换。
- 任何曾在聊天、终端输出或日志中出现的 key 都应视为已暴露并立即轮换。

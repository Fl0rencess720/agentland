# PRD: AI 应用生成最小闭环

## Overview
本 PRD 定义最小可用闭环：用户输入提示词并生成应用，进入工作区后可在左侧看到模型输出并继续对话，在右侧看到预览与代码。

## Goals
- 用户可以从提示词发起生成任务。
- 生成成功后自动进入工作区。
- 工作区左侧可查看模型输出并继续聊天。
- 工作区右侧可查看预览和代码文件内容。

## Out of Scope
- 发布、部署、分享能力。
- 附件上传能力。
- 复杂权限与多角色协作。

## User Flow
1. 用户输入提示词并点击“生成应用”。
2. 系统创建项目并发起生成任务。
3. 任务完成后进入工作区。
4. 左侧加载消息历史并支持继续发送消息。
5. 右侧加载预览地址与代码文件树，点击文件可查看内容。

## API Scope (MVP)
- `POST /api/v1/projects`
- `POST /api/v1/projects/{project_id}/generations`
- `GET /api/v1/jobs/{job_id}`
- `GET /api/v1/projects/{project_id}/chat/messages?conversation_id=c_default&cursor=`
- `POST /api/v1/projects/{project_id}/chat/messages`
- `GET /api/v1/projects/{project_id}/files/tree?path=/workspace&depth=3`
- `GET /api/v1/projects/{project_id}/files/content?path=...`
- `POST /api/v1/projects/{project_id}/preview/start`
- `GET /api/v1/projects/{project_id}/preview`

## Acceptance Criteria
- 点击“生成应用”后 1 秒内返回 `job_id`。
- 任务状态可轮询，最终进入 `SUCCESS` 或 `FAILED`。
- `SUCCESS` 后能进入工作区并看到至少 1 条模型消息。
- 代码区能展示文件树，点击文件可加载内容。
- 预览区可拿到 `preview_url` 并展示页面。
- 所有响应体统一为 `msg`、`code`、`data` 三字段。

## Tasks
- [x] add dashboard
- [x] create project before generation
- [x] submit generation prompt and poll job status
- [x] route to workspace after generation success
- [x] load chat history on workspace init
- [x] send chat message and append assistant reply
- [x] load file tree and open file content
- [x] start preview and poll preview status
- [x] handle loading, empty, and error states
- [x] add mockoon environment for MVP APIs
- [x] done task (skipped)

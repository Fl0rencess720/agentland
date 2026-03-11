# API 文档（面向当前前端原型）

本文基于当前项目页面交互（`Login`、`Dashboard`、`Projects`、`Workspace`、
`CodeEditor`）抽象后端接口。范围覆盖认证、项目管理、AI 生成、聊天、
文件系统、预览、发布、部署与分享。

## 1. 全局约定

### 1.1 Base URL

`/api/v1`

### 1.2 鉴权

- 登录前接口无需鉴权。
- 登录后接口统一携带 Header：

```http
Authorization: Bearer <access_token>
```

### 1.3 通用成功响应

```json
{
  "code": 200,
  "message": "ok",
  "data": {},
  "request_id": "req_01J..."
}
```

### 1.4 通用失败响应

```json
{
  "code": 400,
  "message": "invalid_argument",
  "error": {
    "type": "VALIDATION_ERROR",
    "details": [
      {
        "field": "email",
        "reason": "invalid format"
      }
    ]
  },
  "request_id": "req_01J..."
}
```

## 2. 认证与用户

### 2.1 GitHub 登录发起

- URL: `POST /api/v1/auth/github/start`
- 功能说明: 登录页唯一入口，点击 **Continue with GitHub** 后调用，返回
  GitHub 授权地址。
- 请求体:

```json
{
  "redirect_uri": "https://app.example.com/auth/github/callback"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "authorize_url": "https://github.com/login/oauth/authorize?...",
    "state": "st_abc123"
  },
  "request_id": "req_github_start_001"
}
```

### 2.2 GitHub 登录回调

- URL: `POST /api/v1/auth/github/callback`
- 功能说明: 前端拿到 GitHub `code/state` 后回传服务端，交换用户身份并签发
  平台会话 token。
- 请求体:

```json
{
  "code": "github_auth_code",
  "state": "st_abc123"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "user": {
      "id": "u_123",
      "email": "user@company.com",
      "name": "Alice",
      "avatar_url": "https://avatars.githubusercontent.com/u/123?v=4",
      "github_id": "1234567",
      "github_login": "alice-dev"
    },
    "access_token": "jwt_access",
    "refresh_token": "jwt_refresh",
    "expires_in": 7200
  },
  "request_id": "req_github_callback_001"
}
```

### 2.3 刷新 token

- URL: `POST /api/v1/auth/refresh`
- 功能说明: 前端无感续期会话。
- 请求体:

```json
{
  "refresh_token": "jwt_refresh"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "access_token": "jwt_access_new",
    "refresh_token": "jwt_refresh_new",
    "expires_in": 7200
  },
  "request_id": "req_refresh_001"
}
```

### 2.4 当前用户信息

- URL: `GET /api/v1/auth/me`
- 功能说明: 页面初始化时拉取用户资料、套餐信息。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "id": "u_123",
    "email": "user@company.com",
    "name": "Alice",
    "avatar_url": "",
    "plan": "pro"
  },
  "request_id": "req_me_001"
}
```

### 2.5 退出登录

- URL: `POST /api/v1/auth/logout`
- 功能说明: 对应头像菜单退出。
- 请求体:

```json
{
  "refresh_token": "jwt_refresh"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "logged_out",
  "data": {
    "success": true
  },
  "request_id": "req_logout_001"
}
```

## 3. 项目管理（Projects 页面）

### 3.1 项目列表

- URL: `GET /api/v1/projects?view=all&keyword=dashboard&status=deployed&sort_by=updated_at&sort_order=desc&page=1&page_size=20`
- 功能说明: 支持 **All / Recent / Shared**、搜索、过滤、排序、分页。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "items": [
      {
        "id": "p_001",
        "name": "SaaS Dashboard",
        "status": "DEPLOYED",
        "thumbnail_url": "https://cdn.example.com/p1.png",
        "created_at": "2026-03-10T12:00:00Z",
        "updated_at": "2026-03-11T09:00:00Z",
        "is_shared": true
      }
    ],
    "pagination": {
      "page": 1,
      "page_size": 20,
      "total": 35
    }
  },
  "request_id": "req_projects_list_001"
}
```

### 3.2 创建项目

- URL: `POST /api/v1/projects`
- 功能说明: 对应 **New App / Create New App**。
- 请求体:

```json
{
  "name": "Untitled Project",
  "template": "blank"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "created",
  "data": {
    "id": "p_100",
    "name": "Untitled Project",
    "status": "DRAFT",
    "created_at": "2026-03-11T09:15:00Z"
  },
  "request_id": "req_projects_create_001"
}
```

### 3.3 项目详情

- URL: `GET /api/v1/projects/{project_id}`
- 功能说明: 打开编辑器前拉取项目元信息。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "id": "p_100",
    "name": "Untitled Project",
    "status": "BUILDING",
    "owner_id": "u_123",
    "last_opened_at": "2026-03-11T09:16:00Z"
  },
  "request_id": "req_project_detail_001"
}
```

### 3.4 更新项目

- URL: `PATCH /api/v1/projects/{project_id}`
- 功能说明: 重命名项目、保存项目元数据。
- 请求体:

```json
{
  "name": "Marketing Analytics",
  "metadata": {
    "last_view_mode": "code"
  }
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "updated",
  "data": {
    "id": "p_100",
    "name": "Marketing Analytics",
    "updated_at": "2026-03-11T09:20:00Z"
  },
  "request_id": "req_project_patch_001"
}
```

### 3.5 删除项目

- URL: `DELETE /api/v1/projects/{project_id}`
- 功能说明: 对应项目卡片删除按钮。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "deleted",
  "data": {
    "success": true
  },
  "request_id": "req_project_delete_001"
}
```

### 3.6 项目配额与用量

- URL: `GET /api/v1/projects/usage`
- 功能说明: 对应侧栏 `8 of 12 projects used`。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "used": 8,
    "limit": 12
  },
  "request_id": "req_usage_001"
}
```

## 4. 应用生成（Dashboard: Generate App）

### 4.1 发起生成任务

- URL: `POST /api/v1/projects/{project_id}/generations`
- 功能说明: 使用 prompt 生成初始项目代码。
- 请求体:

```json
{
  "prompt": "Create a SaaS dashboard with dark mode and realtime charts.",
  "model": "gemini-2.5-pro",
  "attachments": [
    {
      "file_id": "f_001",
      "name": "prd.md"
    }
  ],
  "options": {
    "framework": "react",
    "styling": "tailwind"
  }
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "accepted",
  "data": {
    "job_id": "job_gen_001",
    "status": "QUEUED"
  },
  "request_id": "req_gen_start_001"
}
```

### 4.2 查询任务状态

- URL: `GET /api/v1/jobs/{job_id}`
- 功能说明: 轮询生成任务进度与结果。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "job_id": "job_gen_001",
    "type": "APP_GENERATION",
    "status": "RUNNING",
    "progress": 68,
    "logs": [
      "Scaffolding project",
      "Generating components"
    ],
    "result": null
  },
  "request_id": "req_job_status_001"
}
```

## 5. Chat Agent（Workspace 左侧）

### 5.1 获取会话列表

- URL: `GET /api/v1/projects/{project_id}/chat/conversations`
- 功能说明: 支持多会话切换（可扩展）。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "items": [
      {
        "id": "c_default",
        "title": "Default conversation",
        "updated_at": "2026-03-11T09:40:00Z"
      }
    ]
  },
  "request_id": "req_conv_list_001"
}
```

### 5.2 获取消息历史

- URL: `GET /api/v1/projects/{project_id}/chat/messages?conversation_id=c_default&cursor=`
- 功能说明: 渲染聊天记录，支持翻页。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "conversation_id": "c_default",
    "items": [
      {
        "id": "m_1",
        "role": "assistant",
        "content": "I've initialized the React components...",
        "created_at": "2026-03-11T09:00:00Z"
      }
    ],
    "next_cursor": null
  },
  "request_id": "req_chat_list_001"
}
```

### 5.3 发送消息（非流式）

- URL: `POST /api/v1/projects/{project_id}/chat/messages`
- 功能说明: 对应 **Send**，一次性返回 agent 回复和变更摘要。
- 请求体:

```json
{
  "conversation_id": "c_default",
  "content": "Add dark mode toggle to header and show code.",
  "attachments": [
    {
      "file_id": "f_002",
      "name": "design.png"
    }
  ]
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "user_message": {
      "id": "m_10",
      "role": "user",
      "content": "Add dark mode toggle to header and show code."
    },
    "assistant_message": {
      "id": "m_11",
      "role": "assistant",
      "content": "Done. Updated Header.tsx and theme context."
    },
    "changes": [
      {
        "path": "src/components/Header.tsx",
        "action": "update"
      }
    ]
  },
  "request_id": "req_chat_send_001"
}
```

### 5.4 发送消息（流式）

- URL: `POST /api/v1/projects/{project_id}/chat/messages/stream`
- 功能说明: 实时打字机输出，适配长回复与代码生成。
- 请求体:

```json
{
  "conversation_id": "c_default",
  "content": "Refactor project layout and keep responsive.",
  "attachments": []
}
```

- 响应体: `text/event-stream`

```json
{
  "event": "delta",
  "data": {
    "text": "Refactoring layout..."
  }
}
```

```json
{
  "event": "done",
  "data": {
    "message_id": "m_12",
    "changes": [
      {
        "path": "src/App.tsx",
        "action": "update"
      }
    ]
  }
}
```

## 6. 文件系统与代码浏览（CodeEditor 只读）

### 6.1 获取文件树

- URL: `GET /api/v1/projects/{project_id}/files/tree?path=/workspace&depth=3`
- 功能说明: 填充 Explorer 树形目录。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "root": "/workspace",
    "nodes": [
      {
        "path": "/workspace/src",
        "name": "src",
        "type": "folder",
        "children": [
          {
            "path": "/workspace/src/App.tsx",
            "name": "App.tsx",
            "type": "file",
            "size": 812
          }
        ]
      }
    ]
  },
  "request_id": "req_fs_tree_001"
}
```

### 6.2 读取文件

- URL: `GET /api/v1/projects/{project_id}/files/content?path=/workspace/src/App.tsx`
- 功能说明: 打开文件 tab 时读取内容。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "path": "/workspace/src/App.tsx",
    "language": "typescript",
    "content": "import { useState } from 'react';",
    "sha": "8f0d2a..."
  },
  "request_id": "req_fs_read_001"
}
```

### 6.3 下载文件

- URL: `POST /api/v1/projects/{project_id}/files/download`
- 功能说明: 导出构建产物或结果文件。
- 请求体:

```json
{
  "path": "/workspace/dist/app.zip",
  "save_path": "/tmp/app.zip"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "downloaded",
  "data": {
    "path": "/workspace/dist/app.zip",
    "save_path": "/tmp/app.zip"
  },
  "request_id": "req_fs_download_001"
}
```

## 7. 预览、发布、部署与分享（Workspace 顶部）

### 7.1 启动预览

- URL: `POST /api/v1/projects/{project_id}/preview/start`
- 功能说明: 启动运行环境，返回预览地址。
- 请求体:

```json
{
  "device": "desktop",
  "port": 3000
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "preview_started",
  "data": {
    "preview_id": "pv_001",
    "status": "STARTING",
    "preview_url": "https://preview.example.com/pv_001"
  },
  "request_id": "req_preview_start_001"
}
```

### 7.2 查询预览状态

- URL: `GET /api/v1/projects/{project_id}/preview`
- 功能说明: 轮询预览是否可用。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "preview_id": "pv_001",
    "status": "RUNNING",
    "preview_url": "https://preview.example.com/pv_001",
    "last_heartbeat_at": "2026-03-11T09:35:00Z"
  },
  "request_id": "req_preview_status_001"
}
```

### 7.3 停止预览

- URL: `POST /api/v1/projects/{project_id}/preview/stop`
- 功能说明: 释放预览资源。
- 请求体:

```json
{
  "preview_id": "pv_001"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "preview_stopped",
  "data": {
    "success": true
  },
  "request_id": "req_preview_stop_001"
}
```

### 7.4 发布（Publish）

- URL: `POST /api/v1/projects/{project_id}/publish`
- 功能说明: 生成可公开访问的版本。
- 请求体:

```json
{
  "channel": "production",
  "version_note": "first public release"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "published",
  "data": {
    "release_id": "rel_001",
    "public_url": "https://apps.example.com/p/p_100",
    "version": "v1.0.0"
  },
  "request_id": "req_publish_001"
}
```

### 7.5 部署（Deploy）

- URL: `POST /api/v1/projects/{project_id}/deployments`
- 功能说明: 触发构建与部署流水线。
- 请求体:

```json
{
  "environment": "prod",
  "build_command": "npm run build",
  "output_dir": "dist",
  "env": {
    "NODE_ENV": "production"
  }
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "deployment_started",
  "data": {
    "deployment_id": "dep_001",
    "status": "QUEUED"
  },
  "request_id": "req_deploy_start_001"
}
```

### 7.6 查询部署状态

- URL: `GET /api/v1/deployments/{deployment_id}`
- 功能说明: 查看部署进度、日志与线上地址。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "deployment_id": "dep_001",
    "status": "SUCCESS",
    "logs": [
      "Install dependencies",
      "Build completed",
      "Upload artifacts"
    ],
    "live_url": "https://apps.example.com/p/p_100"
  },
  "request_id": "req_deploy_status_001"
}
```

### 7.7 创建分享链接（Share）

- URL: `POST /api/v1/projects/{project_id}/shares`
- 功能说明: 对应 **Share** 按钮，生成可访问链接。
- 请求体:

```json
{
  "scope": "read",
  "expires_at": "2026-04-01T00:00:00Z",
  "password": ""
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "share_id": "sh_001",
    "share_url": "https://app.example.com/share/sh_001"
  },
  "request_id": "req_share_create_001"
}
```

### 7.8 取消分享链接

- URL: `DELETE /api/v1/projects/{project_id}/shares/{share_id}`
- 功能说明: 失效已发出的分享链接。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "deleted",
  "data": {
    "success": true
  },
  "request_id": "req_share_delete_001"
}
```

## 8. 附件接口（生成与聊天共用）

### 8.1 上传附件（二进制）

- URL: `POST /api/v1/files`
- 功能说明: 上传图片、文档等，返回 `file_id` 给生成或聊天接口引用。
- 请求体: `multipart/form-data`

```json
{
  "file": "(binary)",
  "purpose": "chat"
}
```

- 响应体:

```json
{
  "code": 200,
  "message": "uploaded",
  "data": {
    "file_id": "f_002",
    "name": "design.png",
    "size": 48213,
    "mime_type": "image/png"
  },
  "request_id": "req_file_upload_001"
}
```

### 8.2 获取附件元数据

- URL: `GET /api/v1/files/{file_id}`
- 功能说明: 消息或任务详情页展示附件信息。
- 请求体: `无`
- 响应体:

```json
{
  "code": 200,
  "message": "ok",
  "data": {
    "file_id": "f_002",
    "name": "design.png",
    "size": 48213,
    "mime_type": "image/png",
    "download_url": "https://cdn.example.com/f_002"
  },
  "request_id": "req_file_meta_001"
}
```

## 9. 实现优先级建议

第一阶段（必须）：`2.x`、`3.1~3.5`、`4.1~4.2`、`5.2~5.4`、`6.1~6.2`。  
第二阶段（高优先）：`6.3`、`7.1~7.3`。  
第三阶段（增强）：`7.4~7.8`、`8.x`、`3.6`。

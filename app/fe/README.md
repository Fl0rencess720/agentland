# Agentland 前端

React 19 工作区，用于管理 Agentland 项目、Agent Run、Sandbox 文件和应用预览。

## 本地运行

```bash
npm install
npm run dev
```

API 前缀默认为 `/api/v1`。开发服务器将 `/api` 代理到 `http://127.0.0.1:18081`，可用 `VITE_DEV_API_TARGET` 覆盖。跨来源部署可将 `VITE_API_BASE_URL` 设为完整后端前缀，例如 `https://api.example.com/api/v1`，并在后端的 `SERVER_HTTP_CORS_ALLOWED_ORIGINS` 中加入前端来源。所有 API、GitHub OAuth 和 SSE 请求都会携带浏览器凭据。

应用后端返回形如 `http://<token>.localhost:18081/p/<token>/` 的绝对预览地址；前端仅为与主页面来源不同的 HTTP(S) 地址启用预览。

## 验证

```bash
npm run lint
npm run test:run
npm run build
npm run test:e2e
```

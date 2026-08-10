---
name: dockerfile
description: Prepare a frontend project for a secure, reproducible production OCI image build.
---

# Frontend Dockerfile

Inspect the repository before writing files. Read `package.json`, the lockfile, framework configuration, build scripts, output directory, and server start command.

Use the lockfile to select exactly one package manager:

- `package-lock.json`: `npm ci`
- `pnpm-lock.yaml`: Corepack plus `pnpm install --frozen-lockfile`
- `yarn.lock`: Corepack plus `yarn install --immutable`
- `bun.lock` or `bun.lockb`: `bun install --frozen-lockfile`

For a static SPA, use a multi-stage Dockerfile. Build with the matching Node or Bun image, then copy only the build output into an unprivileged static HTTP server image listening on port `8080`. End the final stage with an explicit numeric non-zero `USER`. Configure SPA history fallback when the router requires it. Do not ship source files, package-manager caches, or build tools in the runtime stage.

For SSR frameworks such as Next.js, Nuxt, Remix, or SvelteKit with a server adapter, use the framework's production server output and end the final stage with an explicit numeric non-zero `USER`. Bind the server to `0.0.0.0:8080`, set `PORT=8080`, use `EXPOSE 8080`, and start it with the package's production command. Prefer standalone output where the framework supports it.

Create or repair `.dockerignore`. It must exclude at least:

```text
.git
.agentland
node_modules
.env
.env.*
*.log
coverage
dist
build
.next
.nuxt
```

Allow an explicit public example environment file such as `.env.example`; never copy secrets into an image or use secret values in `ARG` or `ENV`. Keep the build context minimal. Use deterministic dependency installation, explicit base-image versions, multi-stage builds, and a non-root final stage. Do not use privileged BuildKit entitlements, host networking, Docker socket mounts, SSH forwarding, or build secrets unless the platform explicitly supplies them.

After writing `Dockerfile` and `.dockerignore`, reread both files. Validate that the lockfile, install command, build command, output path, runtime command, non-root user, and port `8080` agree with the project. Run a lightweight project build when dependencies are already available; do not publish an image from this skill.

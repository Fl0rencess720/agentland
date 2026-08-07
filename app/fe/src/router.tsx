import { createRootRoute, createRoute, createRouter, Outlet, redirect } from '@tanstack/react-router';
import Login from './components/Login';
import Projects from './components/Projects';
import Workspace from './components/Workspace';
import { getAuthSession } from './stores/auth';

function requireAuth() {
  const { accessToken, refreshToken } = getAuthSession();
  if (!accessToken && !refreshToken) throw redirect({ to: '/login' });
}

const rootRoute = createRootRoute({ component: () => <Outlet /> });
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  beforeLoad: () => {
    const { accessToken, refreshToken } = getAuthSession();
    throw redirect({ to: accessToken || refreshToken ? '/projects' : '/login' });
  },
});
const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: Login });
const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects',
  beforeLoad: requireAuth,
  component: Projects,
});
const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$projectId',
  beforeLoad: requireAuth,
  component: Workspace,
});

const routeTree = rootRoute.addChildren([indexRoute, loginRoute, projectsRoute, projectRoute]);
export const router = createRouter({ routeTree, defaultPreload: 'intent' });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}

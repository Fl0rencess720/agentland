import { beforeEach, describe, expect, it } from 'vitest';
import {
  GITHUB_OAUTH_STATE_KEY,
  githubOAuthStatesEqual,
  saveGitHubOAuthState,
  takeGitHubOAuthState,
} from './oauth';

describe('GitHub OAuth state', () => {
  beforeEach(() => sessionStorage.clear());

  it('stores the state for one callback attempt', () => {
    saveGitHubOAuthState('state-1');
    expect(sessionStorage.getItem(GITHUB_OAUTH_STATE_KEY)).toBe('state-1');
    expect(takeGitHubOAuthState()).toBe('state-1');
    expect(takeGitHubOAuthState()).toBe('');
  });

  it('compares states without an early string comparison', async () => {
    await expect(githubOAuthStatesEqual('state-1', 'state-1')).resolves.toBe(true);
    await expect(githubOAuthStatesEqual('state-1', 'state-2')).resolves.toBe(false);
  });
});

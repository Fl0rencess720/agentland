export const GITHUB_OAUTH_STATE_KEY = 'agentland.github_oauth_state';

export function saveGitHubOAuthState(state: string) {
  sessionStorage.setItem(GITHUB_OAUTH_STATE_KEY, state);
}

export function takeGitHubOAuthState() {
  const state = sessionStorage.getItem(GITHUB_OAUTH_STATE_KEY) ?? '';
  sessionStorage.removeItem(GITHUB_OAUTH_STATE_KEY);
  return state;
}

export async function githubOAuthStatesEqual(left: string, right: string) {
  const encoder = new TextEncoder();
  const [leftHash, rightHash] = await Promise.all([
    crypto.subtle.digest('SHA-256', encoder.encode(left)),
    crypto.subtle.digest('SHA-256', encoder.encode(right)),
  ]);
  const leftBytes = new Uint8Array(leftHash);
  const rightBytes = new Uint8Array(rightHash);
  let difference = 0;
  for (let index = 0; index < leftBytes.length; index += 1) {
    difference |= leftBytes[index] ^ rightBytes[index];
  }
  return difference === 0;
}

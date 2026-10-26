// Thin fetch wrapper for the JSON API.

// Identifies this tab, so it can skip its own changes when they come back over
// the live stream. crypto.randomUUID needs HTTPS; getRandomValues doesn't.
export const CLIENT_ID = Array.from(crypto.getRandomValues(new Uint8Array(12)),
  (b) => b.toString(16).padStart(2, '0')).join('');

export class ApiError extends Error {
  constructor(status, message, code) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function api(method, path, body) {
  const headers = { 'X-Client-ID': CLIENT_ID };
  const init = { method, headers };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch('/api' + path, init);
  } catch {
    throw new ApiError(0, "Can't reach the server. Check your connection.", 'network');
  }
  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    // non-JSON error page (e.g. from a proxy)
  }
  if (!res.ok) {
    throw new ApiError(res.status, data?.error || `${res.status} ${res.statusText}`, data?.code);
  }
  return data;
}

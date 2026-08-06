/**
 * Render an unknown rejection as something a user can act on.
 *
 * Grafana's backendSrv rejects with a plain FetchError object rather than an
 * Error, so `String(err)` yields "[object Object]". That is what the panel
 * editor and the config page used to show in place of the 401 or 429 the backend
 * went to the trouble of forwarding, leaving no way to tell an expired key from
 * a throttled one.
 */
export function errorText(err: unknown): string {
  if (typeof err === 'string') {
    return err;
  }
  if (err instanceof Error) {
    return err.message;
  }
  if (!err || typeof err !== 'object') {
    return 'Unknown error';
  }

  const fetchError = err as {
    status?: number;
    statusText?: string;
    message?: string;
    data?: { error?: string; message?: string } | string;
  };

  // The backend's resource routes answer with {"error": "..."}; other Grafana
  // layers use {"message": "..."} or put the text on the rejection itself.
  const { data } = fetchError;
  const detail =
    (typeof data === 'string' ? data : (data?.error ?? data?.message)) ?? fetchError.message;

  if (detail && fetchError.status) {
    return `${fetchError.status}: ${detail}`;
  }
  if (detail) {
    return detail;
  }
  if (fetchError.status) {
    return `${fetchError.status} ${fetchError.statusText ?? ''}`.trim();
  }
  return 'Unknown error';
}

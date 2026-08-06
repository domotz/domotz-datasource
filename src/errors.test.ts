import { errorText } from './errors';

describe('errorText', () => {
  it('uses the message of a real Error', () => {
    expect(errorText(new Error('boom'))).toBe('boom');
  });

  it('passes a string through', () => {
    expect(errorText('boom')).toBe('boom');
  });

  // The case that mattered: backendSrv rejects with a plain object, so
  // String(err) produced "[object Object]" and the upstream detail was lost.
  it('reads the detail the backend forwarded on a FetchError', () => {
    const rejection = {
      status: 401,
      statusText: 'Unauthorized',
      data: { error: 'Invalid API Key' },
    };

    expect(errorText(rejection)).toBe('401: Invalid API Key');
  });

  it('distinguishes a throttled key from a rejected one', () => {
    expect(
      errorText({ status: 429, data: { error: 'Daily quota exceeded' } })
    ).toBe('429: Daily quota exceeded');
  });

  it('falls back to message, then to status text', () => {
    expect(errorText({ message: 'network down' })).toBe('network down');
    expect(errorText({ status: 502, statusText: 'Bad Gateway' })).toBe('502 Bad Gateway');
  });

  it('accepts a string body', () => {
    expect(errorText({ status: 500, data: 'upstream exploded' })).toBe('500: upstream exploded');
  });

  it('never yields "[object Object]"', () => {
    for (const value of [{}, null, undefined, 42, [], { data: {} }]) {
      expect(errorText(value)).not.toContain('object Object');
    }
  });
});

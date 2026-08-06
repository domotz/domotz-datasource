import { DataSourceInstanceSettings, ScopedVars } from '@grafana/data';

import { DataSource } from './datasource';
import { DomotzDataSourceOptions, DomotzQuery, Scope } from './types';

const replace = jest.fn((target?: string) => target ?? '');

jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  getTemplateSrv: () => ({ replace }),
  getBackendSrv: () => ({ fetch: jest.fn() }),
}));

function makeDataSource(): DataSource {
  const settings = {
    id: 1,
    uid: 'domotz-test',
    type: 'domotz-datasource',
    name: 'Domotz',
    jsonData: {},
    meta: {},
    readOnly: false,
    access: 'proxy',
  } as unknown as DataSourceInstanceSettings<DomotzDataSourceOptions>;

  return new DataSource(settings);
}

function query(overrides: Partial<DomotzQuery> = {}): DomotzQuery {
  return {
    refId: 'A',
    scope: Scope.Device,
    agentId: '7',
    deviceId: '11',
    variableId: '99',
    ...overrides,
  };
}

describe('filterQuery', () => {
  // A half-filled editor is the normal state while the user is choosing.
  // Letting those through is what the old fromDashboard sentinel worked around.
  it.each([
    ['complete device query', query(), true],
    ['complete collector query', query({ scope: Scope.Collector, deviceId: '' }), true],
    ['missing collector', query({ agentId: '' }), false],
    ['missing device on device scope', query({ deviceId: '' }), false],
    ['missing variable', query({ variableId: '' }), false],
    ['missing scope', query({ scope: undefined as unknown as Scope }), false],
  ])('%s -> %s', (_name, q, expected) => {
    expect(makeDataSource().filterQuery(q)).toBe(expected);
  });

  it('does not require a device for collector-scoped queries', () => {
    const ds = makeDataSource();
    expect(ds.filterQuery(query({ scope: Scope.Collector, deviceId: '' }))).toBe(true);
  });
});

describe('applyTemplateVariables', () => {
  beforeEach(() => replace.mockClear());

  it('interpolates every identifier', () => {
    replace.mockImplementation((target?: string) => (target === '$collector' ? '200891' : target ?? ''));

    const result = makeDataSource().applyTemplateVariables(query({ agentId: '$collector' }), {} as ScopedVars);

    expect(result.agentId).toBe('200891');
    expect(result.deviceId).toBe('11');
    expect(result.variableId).toBe('99');
  });

  it('leaves the rest of the query untouched', () => {
    const original = query();
    const result = makeDataSource().applyTemplateVariables(original, {} as ScopedVars);

    expect(result.refId).toBe('A');
    expect(result.scope).toBe(Scope.Device);
    expect(original.agentId).toBe('7');
  });
});

describe('resource lookups', () => {
  beforeEach(() => {
    replace.mockReset();
    replace.mockImplementation((target?: string) => target ?? '');
  });

  it('short-circuits without issuing a request when ids are missing', async () => {
    const ds = makeDataSource();
    const getResource = jest.spyOn(ds, 'getResource');

    await expect(ds.getDevices(undefined)).resolves.toEqual([]);
    await expect(ds.getCollectorVariables('')).resolves.toEqual([]);
    await expect(ds.getDeviceVariables('7', '')).resolves.toEqual([]);

    expect(getResource).not.toHaveBeenCalled();
  });

  // Unfiltered, so the editor can hold one list and narrow it client-side.
  // Fetching the filtered list is what made no-history metrics unselectable.
  it('asks the backend for every metric and filters in the editor', async () => {
    const ds = makeDataSource();
    const getResource = jest.spyOn(ds, 'getResource').mockResolvedValue([]);

    await ds.getDeviceVariables('7', '11');

    expect(getResource).toHaveBeenCalledWith('collectors/7/devices/11/variables?hasHistory=false');
  });

  it('honours an explicit history-only request', async () => {
    const ds = makeDataSource();
    const getResource = jest.spyOn(ds, 'getResource').mockResolvedValue([]);

    await ds.getCollectorVariables('7', true);

    expect(getResource).toHaveBeenCalledWith('collectors/7/variables?hasHistory=true');
  });

  // The editor's fields advertise dashboard variables, and the backend's routes
  // parse their path segments as integers. Interpolating in the lookup is what
  // keeps a variable-driven editor from answering 400 to every request.
  it('interpolates a template variable before calling the backend', async () => {
    replace.mockImplementation((target?: string) => (target === '$collector' ? '200891' : target ?? ''));

    const ds = makeDataSource();
    const getResource = jest.spyOn(ds, 'getResource').mockResolvedValue([]);

    await ds.getDevices('$collector');

    expect(getResource).toHaveBeenCalledWith('collectors/200891/devices');
  });

  // A value that is not a plain number after interpolation cannot be a valid id,
  // so no request is issued - and nothing unencoded can reach the path.
  it.each(['a/b', '$undefined', '{11,12}', '  '])('issues no request for %p', async (raw) => {
    replace.mockImplementation((target?: string) => target ?? '');

    const ds = makeDataSource();
    const getResource = jest.spyOn(ds, 'getResource');

    await expect(ds.getDevices(raw)).resolves.toEqual([]);

    expect(getResource).not.toHaveBeenCalled();
  });
});

describe('cache invalidation', () => {
  it('posts to the backend so a newly-added device becomes visible', async () => {
    const ds = makeDataSource();
    const postResource = jest.spyOn(ds, 'postResource').mockResolvedValue(undefined);

    await ds.invalidateCache();

    expect(postResource).toHaveBeenCalledWith('cache/invalidate');
  });
});

describe('defaults', () => {
  it('starts new queries in device scope', () => {
    expect(makeDataSource().getDefaultQuery().scope).toBe(Scope.Device);
  });

  it('registers template variable support', () => {
    expect(makeDataSource().variables).toBeDefined();
  });
});

describe('filterQuery with unresolved variables', () => {
  beforeEach(() => replace.mockReset());

  // A variable whose own query returned nothing is left by Grafana as the
  // literal "$deviceMetric". Sending it produced a red panel; the honest state
  // is "not pointed at anything yet".
  it('suppresses a query whose variable did not resolve', () => {
    replace.mockImplementation((t?: string) => (t === '$deviceMetric' ? '$deviceMetric' : t ?? ''));

    const q = query({ variableId: '$deviceMetric' });
    expect(makeDataSource().filterQuery(q)).toBe(false);
  });

  it('runs a query once every variable resolves', () => {
    replace.mockImplementation((t?: string) => (t === '$deviceMetric' ? '99' : t ?? ''));

    expect(makeDataSource().filterQuery(query({ variableId: '$deviceMetric' }))).toBe(true);
  });

  it('allows multi-value expansions through', () => {
    replace.mockImplementation((t?: string) => (t === '$deviceMetric' ? '{10,20}' : t ?? ''));

    expect(makeDataSource().filterQuery(query({ variableId: '$deviceMetric' }))).toBe(true);
  });
});

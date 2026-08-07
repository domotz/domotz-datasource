import { DataSourceInstanceSettings, ScopedVars } from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';

import { DomotzVariableSupport } from './variables';
import {
  Collector,
  DEFAULT_QUERY,
  Device,
  DomotzDataSourceOptions,
  DomotzQuery,
  Scope,
  Usage,
  Variable,
} from './types';

export class DataSource extends DataSourceWithBackend<DomotzQuery, DomotzDataSourceOptions> {
  constructor(instanceSettings: DataSourceInstanceSettings<DomotzDataSourceOptions>) {
    super(instanceSettings);
    this.variables = new DomotzVariableSupport(this);
  }

  getDefaultQuery(): Partial<DomotzQuery> {
    return DEFAULT_QUERY;
  }

  /**
   * Suppress half-filled queries.
   *
   * A partially completed editor is the normal state while the user is still
   * choosing, and firing those at the backend produced the spurious errors the
   * previous version worked around with a `fromDashboard: "FROM_DASHBOARD"`
   * sentinel threaded through the query model. This is the supported hook for
   * the same job.
   */
  filterQuery(query: DomotzQuery): boolean {
    if (!query.scope || !query.agentId || !query.variableId) {
      return false;
    }
    if (query.scope === Scope.Device && !query.deviceId) {
      return false;
    }

    // A dashboard variable with nothing selected - one whose own query returned
    // no options, which happens whenever a device exposes no metrics - is left
    // by Grafana as the literal "$deviceMetric". Sending that produced a red
    // panel reading `expected a numeric id`, when the honest answer is that
    // this panel has not been pointed at anything yet.
    return !this.hasUnresolvedVariable(query);
  }

  /**
   * True when any identifier still contains a template variable reference, or
   * refers to a variable that currently resolves to nothing.
   *
   * The empty case is not hypothetical: a multi-value variable whose own query
   * returned no options interpolates to "" rather than being left as the
   * literal "$deviceMetric", so a `$`-check alone lets the query through and the
   * backend answers `a variable must be selected` in red. Selecting several
   * devices used to do exactly that. Nothing has been chosen yet, so the honest
   * rendering is no data.
   */
  private hasUnresolvedVariable(query: DomotzQuery): boolean {
    const srv = getTemplateSrv();
    return [query.agentId, query.deviceId, query.variableId].filter(Boolean).some((raw) => {
      const interpolated = srv.replace(raw!).trim();
      return interpolated === '' || interpolated.includes('$');
    });
  }

  applyTemplateVariables(query: DomotzQuery, scopedVars: ScopedVars): DomotzQuery {
    const srv = getTemplateSrv();
    return {
      ...query,
      agentId: srv.replace(query.agentId, scopedVars),
      deviceId: srv.replace(query.deviceId, scopedVars),
      variableId: srv.replace(query.variableId, scopedVars),
    };
  }

  // --- editor lookups, served by the backend so the key stays server-side ---

  /**
   * Resolve a field that may hold a template variable into a concrete id.
   *
   * The editor invites dashboard variables into these fields, so a lookup can be
   * handed "$collector" verbatim. Interpolating here is what makes that work:
   * `applyTemplateVariables` runs only for panel queries, never for the editor's
   * own lookups, so without this the resource routes - which parse their path
   * segments as integers - answered every variable-driven editor with 400 and
   * left the Device and Metric pickers permanently empty.
   *
   * A value that is still not a plain number after interpolation (an undefined
   * variable, or a multi-value one) yields undefined and the caller skips the
   * request: an empty list beats a request the backend can only reject.
   */
  private resolveId(raw: string | undefined): string | undefined {
    const ids = this.resolveIds(raw);
    return ids.length === 1 ? ids[0] : undefined;
  }

  /**
   * Resolve a field that may hold a multi-value template variable into every id
   * it names.
   *
   * Grafana renders a multi-value variable as a glob - `{71,72}` - which
   * `resolveId` rejects, since one lookup path needs exactly one id. A device
   * metric list does not: several devices can be selected at once, and the
   * metrics worth offering are the ones they share.
   */
  private resolveIds(raw: string | undefined): string[] {
    if (!raw) {
      return [];
    }
    let interpolated = getTemplateSrv().replace(raw).trim();
    if (interpolated.startsWith('{') && interpolated.endsWith('}')) {
      interpolated = interpolated.slice(1, -1);
    }
    return interpolated
      .split(',')
      .map((part) => part.trim().replace(/^"|"$/g, ''))
      .filter((part) => /^\d+$/.test(part));
  }

  getCollectors(): Promise<Collector[]> {
    return this.getResource('collectors');
  }

  getDevices(collectorId: string | undefined): Promise<Device[]> {
    const collector = this.resolveId(collectorId);
    if (!collector) {
      return Promise.resolve([]);
    }
    return this.getResource(`collectors/${collector}/devices`);
  }

  /**
   * Devices belonging to any of several collectors.
   *
   * Fans out rather than adding a route: device ids are unique across
   * collectors so there is nothing to merge or de-duplicate, and each per
   * collector call is already cached in the backend.
   */
  async getDevicesForCollectors(collectorIds: string | undefined): Promise<Device[]> {
    const collectors = this.resolveIds(collectorIds);
    if (collectors.length === 0) {
      return [];
    }
    const lists = await Promise.all(
      collectors.map((id) => this.getResource(`collectors/${id}/devices`) as Promise<Device[]>)
    );
    return lists.flat();
  }

  /**
   * `historyOnly` defaults to false so the editor can hold one list and filter it
   * client-side. Fetching the filtered list was what made no-history metrics
   * unselectable, even though the backend still plots their current value.
   */
  getCollectorVariables(collectorId: string | undefined, historyOnly = false): Promise<Variable[]> {
    const collector = this.resolveId(collectorId);
    if (!collector) {
      return Promise.resolve([]);
    }
    return this.getResource(`collectors/${collector}/variables?hasHistory=${historyOnly}`);
  }

  getDeviceVariables(
    collectorId: string | undefined,
    deviceId: string | undefined,
    historyOnly = false
  ): Promise<Variable[]> {
    const collector = this.resolveId(collectorId);
    const device = this.resolveId(deviceId);
    if (!collector || !device) {
      return Promise.resolve([]);
    }
    return this.getResource(`collectors/${collector}/devices/${device}/variables?hasHistory=${historyOnly}`);
  }

  /**
   * Metrics exposed by a set of devices, listed once each by sensor path.
   *
   * Backs the device-metric template variable, which is the one place several
   * devices can be in play at once. Selecting the returned path addresses the
   * metric on whichever of those devices actually has it - a variable id could
   * only ever mean one of them, which is why picking "ifNumber" for three
   * devices used to be impossible.
   */
  /**
   * Collector metrics across several collectors, listed once each by path.
   *
   * The collector-level counterpart of getSharedDeviceVariables: every site
   * measures Download and Latency, each under its own variable id, so the path
   * is what lets one selection mean "this measurement, at all these sites".
   */
  getSharedCollectorVariables(collectorIds: string | undefined, historyOnly = false): Promise<Variable[]> {
    const collectors = this.resolveIds(collectorIds);
    if (collectors.length === 0) {
      return Promise.resolve([]);
    }
    return this.getResource(`shared-collector-variables?hasHistory=${historyOnly}&collectors=${collectors.join(',')}`);
  }

  async getSharedDeviceVariables(
    collectorIds: string | undefined,
    deviceIds: string | undefined,
    historyOnly = false
  ): Promise<Variable[]> {
    const collectors = this.resolveIds(collectorIds);
    const devices = this.resolveIds(deviceIds);
    if (collectors.length === 0 || devices.length === 0) {
      return [];
    }

    // Each collector sweeps its own devices; a device id belonging to another
    // collector simply matches nothing there.
    const lists = await Promise.all(
      collectors.map(
        (id) =>
          this.getResource(
            `collectors/${id}/shared-variables?hasHistory=${historyOnly}&devices=${devices.join(',')}`
          ) as Promise<Variable[]>
      )
    );

    // The backend de-duplicates within a collector; across collectors is this
    // side's job, and it is the same rule - one entry per sensor path.
    const seen = new Set<string>();
    return lists.flat().filter((v) => {
      if (!v.path || seen.has(v.path)) {
        return false;
      }
      seen.add(v.path);
      return true;
    });
  }

  getUsage(): Promise<Usage> {
    return this.getResource('usage');
  }

  /**
   * Drop the backend's cached metadata, backing the editor's refresh control.
   *
   * Metadata is cached for five minutes to protect the API key's concurrency
   * budget, which means a collector or device just added in the Domotz portal is
   * invisible until it expires - indistinguishable from the editor being broken.
   */
  invalidateCache(): Promise<void> {
    return this.postResource('cache/invalidate');
  }
}

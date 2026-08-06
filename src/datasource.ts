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

  /** True when any identifier still contains a template variable reference. */
  private hasUnresolvedVariable(query: DomotzQuery): boolean {
    const srv = getTemplateSrv();
    return [query.agentId, query.deviceId, query.variableId]
      .filter(Boolean)
      .some((raw) => srv.replace(raw!).includes('$'));
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
    if (!raw) {
      return undefined;
    }
    const interpolated = getTemplateSrv().replace(raw).trim();
    return /^\d+$/.test(interpolated) ? interpolated : undefined;
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

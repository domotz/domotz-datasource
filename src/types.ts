import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

/** Whether a query targets a collector's own metrics or one of its devices. */
export enum Scope {
  Collector = 'collector',
  Device = 'device',
}

/**
 * The persisted shape of a panel query.
 *
 * Identifiers are stored as strings rather than numbers so a field can hold a
 * dashboard template variable (`$collector`) until `applyTemplateVariables`
 * interpolates it. The backend accepts either encoding.
 *
 * Nothing else is stored. Display names, units and current values are resolved
 * from the API at query time, so a renamed device updates every legend that
 * references it instead of leaving stale text baked into the dashboard JSON.
 */
export interface DomotzQuery extends DataQuery {
  scope: Scope;
  agentId: string;
  deviceId: string;
  variableId: string;
}

export const DEFAULT_QUERY: Partial<DomotzQuery> = {
  scope: Scope.Device,
};

export enum VariableKind {
  Collectors = 'collectors',
  Devices = 'devices',
  DeviceVariables = 'deviceVariables',
  CollectorVariables = 'collectorVariables',
}

/**
 * Query shape for a dashboard template variable of type "Query".
 *
 * Extends DataQuery because CustomVariableSupport runs variable queries
 * through the same DataQueryRequest machinery as panel queries.
 */
export interface DomotzVariableQuery extends DataQuery {
  kind: VariableKind;
  /** Required for device and variable lookups; may itself be `$collector`. */
  agentId?: string;
  /** Required for device-variable lookups; may itself be `$device`. */
  deviceId?: string;
}

export interface DomotzDataSourceOptions extends DataSourceJsonData {
  path?: string;
}

/** Held in secureJsonData; never sent to the browser. */
export interface DomotzSecureJsonData {
  apiKey?: string;
}

// --- resource payloads, mirroring pkg/domotz models -----------------------

export interface Collector {
  id: number;
  display_name: string;
  status?: { value: string; last_change: string };
}

export interface Device {
  id: number;
  display_name: string;
  /** Present on IP devices; absent for devices reached via a third party. */
  ip_addresses?: string[];
  /** MAC address. Only local IP devices report one. */
  hw_address?: string;
}

export interface Variable {
  id: number;
  device_id?: number;
  label: string;
  metric: string;
  path: string;
  unit: string;
  has_history: boolean;
  value: string;
  /** Resolved server-side so the picker and the panel legend cannot disagree. */
  displayLabel: string;
}

export interface Usage {
  daily_usage: number;
  daily_limit: number;
}

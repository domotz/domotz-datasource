import { CustomVariableSupport, DataQueryRequest, DataQueryResponse, MetricFindValue } from '@grafana/data';
import { Observable, from } from 'rxjs';

import type { DataSource } from './datasource';
import { deviceOption, variableOptions } from './labels';
import { DomotzVariableQuery, Variable, VariableKind } from './types';
import { VariableQueryEditor } from './components/VariableQueryEditor';

/**
 * Template-variable support.
 *
 * This is what makes one dashboard reusable across sites: a `$collector`
 * variable feeding repeated panels, rather than every panel hard-pinned to a
 * single collector/device/variable triple. The previous version had no
 * variable support at all - `applyTemplateVariables` was commented out - so a
 * 50-site MSP had to clone the dashboard 50 times.
 */
/**
 * Variables list only metrics that record history.
 *
 * A variable exists to drive panels, and a panel plots a series - offering a
 * firmware version or a serial number here means the first value Grafana
 * auto-selects is often one that can never draw a line. The query editor still
 * reaches those through its History only toggle, where the choice is explicit.
 */
const HISTORY_ONLY = true;

export class DomotzVariableSupport extends CustomVariableSupport<DataSource, DomotzVariableQuery> {
  constructor(private readonly datasource: DataSource) {
    super();
    // CustomVariableSupport calls `query` as a bare function reference rather
    // than as a method, so it needs its receiver bound here.
    this.query = this.query.bind(this);
  }

  editor = VariableQueryEditor;

  query(request: DataQueryRequest<DomotzVariableQuery>): Observable<DataQueryResponse> {
    const target = request.targets[0];
    return from(this.execute(target).then((values) => ({ data: values })));
  }

  private async execute(query: DomotzVariableQuery): Promise<MetricFindValue[]> {
    if (!query?.kind) {
      return [];
    }

    // Interpolation happens in the data source's lookup methods, which every
    // caller shares - a chained variable ($device depending on $collector) and
    // the query editor need the same treatment.
    const { agentId, deviceId } = query;

    switch (query.kind) {
      case VariableKind.Collectors: {
        const collectors = await this.datasource.getCollectors();
        return collectors.map((c) => ({ text: c.display_name, value: String(c.id) }));
      }

      case VariableKind.Devices: {
        const devices = await this.datasource.getDevices(agentId);
        // Same label as the query editor, so a device reads the same wherever
        // it appears. The address stays out of it: a variable dropdown has no
        // second line to put it on, and Grafana matches its regex against the
        // value, so nothing is gained by lengthening the text.
        return devices.map((d) => ({ text: deviceOption(d).label ?? String(d.id), value: String(d.id) }));
      }

      case VariableKind.CollectorVariables: {
        return asMetrics(await this.datasource.getCollectorVariables(agentId, HISTORY_ONLY));
      }

      case VariableKind.DeviceVariables: {
        return asMetrics(await this.datasource.getDeviceVariables(agentId, deviceId, HISTORY_ONLY));
      }

      default:
        return [];
    }
  }
}

/**
 * Metric options for a variable dropdown, disambiguated the same way the query
 * editor does it.
 *
 * Without this a collector's TCP-service sensors all resolve to the same label
 * - one real account lists `443` five times - and the dropdown offers five
 * identical entries with no way to tell them apart. The dropdown has no second
 * line, so the disambiguating path has to go in the text.
 */
function asMetrics(variables: Variable[]): MetricFindValue[] {
  return variableOptions(variables).map((o) => ({ text: o.label ?? o.value, value: o.value }));
}

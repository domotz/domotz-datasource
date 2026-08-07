import { CustomVariableSupport, DataQueryRequest, DataQueryResponse, MetricFindValue } from '@grafana/data';
import { Observable, from } from 'rxjs';

import type { DataSource } from './datasource';
import { collectorOption, deviceOption, flattenOption, variableOptions } from './labels';
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
        return collectors.map((c) => ({ text: flattenOption(collectorOption(c)), value: String(c.id) }));
      }

      case VariableKind.Devices: {
        // Several collectors may be selected: a device list that spans sites is
        // what makes a multi-site dashboard possible at all.
        const devices = await this.datasource.getDevicesForCollectors(agentId);
        // The editor's two lines, folded into the one line a variable dropdown
        // shows - so a device reads the same in the header bar as in the query
        // editor, and stays findable by address there too.
        return devices.map((d) => ({ text: flattenOption(deviceOption(d)), value: String(d.id) }));
      }

      case VariableKind.CollectorVariables: {
        // By path, for the same reason device metrics are: every collector
        // measures Download under its own variable id, and only the path is
        // common to all of them.
        const variables = await this.datasource.getSharedCollectorVariables(agentId, HISTORY_ONLY);
        return asPathMetrics(variables);
      }

      case VariableKind.DeviceVariables: {
        // Addressed by sensor path, not id: the same metric carries a different
        // id on every device, so an id-valued variable can only ever describe
        // one of the selected devices. The path is what they share.
        return asPathMetrics(await this.datasource.getSharedDeviceVariables(agentId, deviceId, HISTORY_ONLY));
      }

      default:
        return [];
    }
  }
}

/**
 * Metric options carrying the sensor path as their value.
 *
 * Labels are disambiguated the same way the query editor does it: without that
 * a collector's TCP-service sensors all resolve to the same label - one real
 * account lists `443` five times - and the dropdown offers five identical
 * entries with no way to tell them apart. The dropdown has no second line, so
 * the disambiguating path has to go in the text.
 *
 * The live reading that the query editor shows on its second line is
 * deliberately left out: variable options are cached and refreshed on the
 * dashboard's own schedule, so a value baked into the text would sooner or
 * later be a number that is no longer true.
 */
function asPathMetrics(variables: Variable[]): MetricFindValue[] {
  return variableOptions(
    variables.filter((v) => v.path),
    (v) => v.path
  ).map((o) => ({ text: o.label ?? o.value, value: o.value }));
}

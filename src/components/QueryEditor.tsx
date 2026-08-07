import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { QueryEditorProps, SelectableValue } from '@grafana/data';
import {
  Alert,
  Combobox,
  ComboboxOption,
  IconButton,
  InlineField,
  InlineFieldRow,
  InlineSwitch,
  RadioButtonGroup,
} from '@grafana/ui';

import { DataSource } from '../datasource';
import { errorText } from '../errors';
import { collectorOption, deviceOption, filterOptions, variableOptions as buildVariableOptions } from '../labels';
import { DomotzDataSourceOptions, DomotzQuery, Scope, Variable } from '../types';

type Props = QueryEditorProps<DataSource, DomotzQuery, DomotzDataSourceOptions>;

const SCOPE_OPTIONS: Array<SelectableValue<Scope>> = [
  { label: 'Device', value: Scope.Device, description: 'A metric belonging to one device on a collector' },
  { label: 'Collector', value: Scope.Collector, description: 'A metric belonging to the collector itself' },
];

const LABEL_WIDTH = 14;
const FIELD_WIDTH = 44;

const VARIABLE_TOOLTIP =
  'Supports dashboard variables, e.g. $collector. A multi-value variable draws one series per value, up to 20 per panel.';

/**
 * Cascading Collector -> Device -> Variable picker.
 *
 * Built on Combobox rather than Select: Select is deprecated as of Grafana 12,
 * and its object-valued API (whole entities passed as the value, resolved with
 * getOptionLabel) is what let display names leak into the saved query in the
 * first place. Combobox takes scalar values, so the editor can only persist
 * identifiers.
 */
export function QueryEditor({ query, onChange, onRunQuery, datasource }: Props) {
  const scope = query.scope ?? Scope.Device;
  const [error, setError] = useState<string | undefined>();

  // Element ids must be unique per query editor instance. They used to be
  // fixed strings, so a panel with several queries rendered four elements
  // sharing each id - and InlineField links its label with htmlFor, so
  // clicking any label (the History only switch most visibly) always toggled
  // query A's control. useId guarantees uniqueness; refId gives the tests a
  // stable handle. Colons are stripped because useId emits ":r1:", which is
  // not a valid CSS id selector.
  const uid = useId().replace(/:/g, '');
  const fieldId = (name: string) => `domotz-${name}-${uid}`;
  const testId = (name: string) => `domotz-${name}-${query.refId}`;

  // Options are loaded eagerly per level rather than per keystroke: the
  // backend caches them, and the lists are small enough that client-side
  // filtering beats a request per character against a quota-limited API.
  const [collectors, setCollectors] = useState<Array<ComboboxOption<string>>>([]);
  const [devices, setDevices] = useState<Array<ComboboxOption<string>>>([]);
  const [allVariables, setAllVariables] = useState<Variable[]>([]);
  const [loading, setLoading] = useState({ collectors: false, devices: false, variables: false });

  // The metric list is fetched unfiltered and narrowed here, so switching the
  // toggle costs nothing upstream and a saved no-history metric always resolves
  // to its real label rather than a bare id.
  const [historyOnly, setHistoryOnly] = useState(true);

  // The Combobox loaders below await these rather than reading state, so
  // opening a picker while its fetch is still in flight waits for the result
  // instead of showing an empty list it would never retry.
  const pending = useRef({
    collectors: Promise.resolve([] as Array<ComboboxOption<string>>),
    devices: Promise.resolve([] as Array<ComboboxOption<string>>),
    variables: Promise.resolve([] as Variable[]),
  });
  const historyOnlyRef = useRef(historyOnly);
  historyOnlyRef.current = historyOnly;

  // Bumped by the refresh control to re-run every lookup after the backend has
  // dropped its cached metadata.
  const [reloadKey, setReloadKey] = useState(0);
  const [refreshing, setRefreshing] = useState(false);

  const report = useCallback((err: unknown) => {
    setError(errorText(err));
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading((s) => ({ ...s, collectors: true }));

    const request = datasource.getCollectors().then((list) => list.map(collectorOption));
    pending.current.collectors = request.catch(() => []);

    request
      .then((options) => {
        if (cancelled) {
          return;
        }
        setError(undefined);
        setCollectors(options);
      })
      .catch(report)
      .finally(() => !cancelled && setLoading((s) => ({ ...s, collectors: false })));

    return () => {
      cancelled = true;
    };
  }, [datasource, report, reloadKey]);

  useEffect(() => {
    if (scope !== Scope.Device || !query.agentId) {
      setDevices([]);
      return;
    }
    let cancelled = false;
    setLoading((s) => ({ ...s, devices: true }));

    const request = datasource.getDevices(query.agentId).then((list) => list.map(deviceOption));
    pending.current.devices = request.catch(() => []);

    request
      .then((options) => {
        if (!cancelled) {
          setDevices(options);
        }
      })
      .catch(report)
      .finally(() => !cancelled && setLoading((s) => ({ ...s, devices: false })));

    return () => {
      cancelled = true;
    };
  }, [datasource, query.agentId, scope, report, reloadKey]);

  useEffect(() => {
    const ready = scope === Scope.Collector ? Boolean(query.agentId) : Boolean(query.agentId && query.deviceId);
    if (!ready) {
      setAllVariables([]);
      return;
    }
    let cancelled = false;
    setLoading((s) => ({ ...s, variables: true }));

    const request =
      scope === Scope.Collector
        ? datasource.getCollectorVariables(query.agentId)
        : datasource.getDeviceVariables(query.agentId, query.deviceId);

    pending.current.variables = request.catch(() => []);

    request
      .then((list) => {
        if (!cancelled) {
          setAllVariables(list);
        }
      })
      .catch(report)
      .finally(() => !cancelled && setLoading((s) => ({ ...s, variables: false })));

    return () => {
      cancelled = true;
    };
  }, [datasource, query.agentId, query.deviceId, scope, report, reloadKey]);

  const variableOptions = useMemo(() => {
    const listed = historyOnly ? allVariables.filter((v) => v.has_history) : allVariables;
    return buildVariableOptions(listed);
  }, [allVariables, historyOnly]);

  // Combobox only searches the label when given a plain array, and does no
  // filtering at all when given a loader. Supplying loaders is what lets the
  // second line - a device's addresses, a metric's path - stay searchable
  // without lengthening the label. The lists are already in memory, so these
  // resolve immediately.
  const loadCollectors = useCallback(
    (input: string) => pending.current.collectors.then((options) => filterOptions(options, input)),
    []
  );
  const loadDevices = useCallback(
    (input: string) => pending.current.devices.then((options) => filterOptions(options, input)),
    []
  );
  const loadVariables = useCallback(
    (input: string) =>
      pending.current.variables.then((list) => {
        const listed = historyOnlyRef.current ? list.filter((v) => v.has_history) : list;
        return filterOptions(buildVariableOptions(listed), input);
      }),
    []
  );

  // An async Combobox renders the raw value unless handed the whole option, so
  // a saved selection would otherwise show as a bare id.
  const selected = (options: Array<ComboboxOption<string>>, value: string) =>
    options.find((o) => o.value === value) ?? (value || null);

  // Every change writes `scope` back explicitly. getDefaultQuery only supplies
  // it for rendering - the persisted query starts without it - so spreading
  // `...query` alone would save a query with no scope, which filterQuery then
  // rejects and no request is ever issued.
  //
  // A change higher up the cascade also invalidates everything below it,
  // otherwise a stale device id survives a collector switch and the panel
  // silently queries a device belonging to another site.
  const onScopeChange = (next: Scope) => {
    onChange({ ...query, scope: next, deviceId: '', variableId: '' });
  };

  const onCollectorChange = (option: ComboboxOption<string> | null) => {
    onChange({ ...query, scope, agentId: option?.value ?? '', deviceId: '', variableId: '' });
  };

  const onDeviceChange = (option: ComboboxOption<string> | null) => {
    onChange({ ...query, scope, deviceId: option?.value ?? '', variableId: '' });
  };

  const onVariableChange = (option: ComboboxOption<string> | null) => {
    onChange({ ...query, scope, variableId: option?.value ?? '' });
    onRunQuery();
  };

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    datasource
      .invalidateCache()
      .then(() => {
        setError(undefined);
        setReloadKey((key) => key + 1);
      })
      .catch(report)
      .finally(() => setRefreshing(false));
  }, [datasource, report]);

  const variablePlaceholder = useMemo(() => {
    if (!query.agentId) {
      return 'Select a collector first';
    }
    if (scope === Scope.Device && !query.deviceId) {
      return 'Select a device first';
    }
    if (variableOptions.length) {
      return 'Select a metric';
    }
    return historyOnly ? 'No metrics with history' : 'No metrics';
  }, [query.agentId, query.deviceId, scope, variableOptions.length, historyOnly]);

  return (
    <>
      {error && (
        <Alert title="Could not load Domotz metadata" severity="error" onRemove={() => setError(undefined)}>
          {error}
        </Alert>
      )}

      <InlineFieldRow>
        <InlineField label="Scope" labelWidth={LABEL_WIDTH} tooltip="Query a device metric or a collector metric">
          <RadioButtonGroup options={SCOPE_OPTIONS} value={scope} onChange={onScopeChange} />
        </InlineField>
      </InlineFieldRow>

      <InlineFieldRow>
        <InlineField label="Collector" labelWidth={LABEL_WIDTH} tooltip={VARIABLE_TOOLTIP} grow>
          <Combobox
            id={fieldId('collector')}
            data-testid={testId('collector')}
            options={loadCollectors}
            value={selected(collectors, query.agentId)}
            onChange={onCollectorChange}
            loading={loading.collectors}
            placeholder="Select a collector"
            width={FIELD_WIDTH}
            isClearable
            createCustomValue
            customValueDescription="Use a dashboard variable"
          />
        </InlineField>
        <IconButton
          name="sync"
          aria-label="Refresh collector, device and metric lists"
          tooltip="Reload from Domotz. Lists are cached for five minutes to protect the API quota."
          onClick={onRefresh}
          disabled={refreshing}
        />
      </InlineFieldRow>

      {scope === Scope.Device && (
        <InlineFieldRow>
          <InlineField label="Device" labelWidth={LABEL_WIDTH} tooltip={VARIABLE_TOOLTIP} grow>
            <Combobox
              id={fieldId('device')}
              data-testid={testId('device')}
              options={loadDevices}
              value={selected(devices, query.deviceId)}
              onChange={onDeviceChange}
              loading={loading.devices}
              disabled={!query.agentId}
              placeholder={query.agentId ? 'Select a device' : 'Select a collector first'}
              width={FIELD_WIDTH}
              isClearable
              createCustomValue
              customValueDescription="Use a dashboard variable"
            />
          </InlineField>
        </InlineFieldRow>
      )}

      <InlineFieldRow>
        <InlineField label="Metric" labelWidth={LABEL_WIDTH} tooltip={VARIABLE_TOOLTIP} grow>
          <Combobox
            id={fieldId('variable')}
            data-testid={testId('variable')}
            options={loadVariables}
            value={selected(variableOptions, query.variableId)}
            onChange={onVariableChange}
            loading={loading.variables}
            disabled={!variableOptions.length && !query.variableId}
            placeholder={variablePlaceholder}
            width={FIELD_WIDTH}
            isClearable
            createCustomValue
            customValueDescription="Use a dashboard variable"
          />
        </InlineField>
      </InlineFieldRow>

      <InlineFieldRow>
        <InlineField
          label="History only"
          labelWidth={LABEL_WIDTH}
          tooltip="Metrics without history are graphed as their current value, as a single point."
        >
          <InlineSwitch
            id={fieldId('history-only')}
            data-testid={testId('history-only')}
            value={historyOnly}
            onChange={(event) => setHistoryOnly(event.currentTarget.checked)}
          />
        </InlineField>
      </InlineFieldRow>
    </>
  );
}

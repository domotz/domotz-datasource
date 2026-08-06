import React from 'react';
import { QueryEditorProps } from '@grafana/data';
import { Combobox, ComboboxOption, InlineField, InlineFieldRow } from '@grafana/ui';

import { DataSource } from '../datasource';
import { DomotzDataSourceOptions, DomotzQuery, DomotzVariableQuery, VariableKind } from '../types';

type Props = QueryEditorProps<DataSource, DomotzQuery, DomotzDataSourceOptions, DomotzVariableQuery>;

const KIND_OPTIONS: Array<ComboboxOption<VariableKind>> = [
  { label: 'Collectors', value: VariableKind.Collectors, description: 'Every collector on the account' },
  { label: 'Devices', value: VariableKind.Devices, description: 'Devices belonging to one collector' },
  {
    label: 'Device metrics',
    value: VariableKind.DeviceVariables,
    description: 'History-capable metrics of one device',
  },
  {
    label: 'Collector metrics',
    value: VariableKind.CollectorVariables,
    description: "History-capable metrics of the collector itself",
  },
];

const LABEL_WIDTH = 16;

/**
 * Editor for a dashboard template variable of type "Query".
 *
 * The dependent fields accept free text on purpose: a Devices variable is
 * normally chained off a Collectors variable ("$collector"), not off a
 * hard-coded id.
 */
export function VariableQueryEditor({ query, onChange }: Props) {
  const kind = query?.kind;
  const needsCollector = kind !== undefined && kind !== VariableKind.Collectors;
  const needsDevice = kind === VariableKind.DeviceVariables;

  const update = (patch: Partial<DomotzVariableQuery>) => {
    onChange({ ...query, ...patch } as DomotzVariableQuery);
  };

  const onKindChange = (option: ComboboxOption<VariableKind> | null) => {
    update({ kind: option?.value });
  };

  const onTextChange =
    (field: 'agentId' | 'deviceId') =>
    (option: ComboboxOption<string> | null) => {
      update({ [field]: option?.value ?? '' });
    };

  return (
    <>
      <InlineFieldRow>
        <InlineField label="Query type" labelWidth={LABEL_WIDTH} grow>
          <Combobox
            id="domotz-variable-kind"
            options={KIND_OPTIONS}
            value={kind ?? null}
            onChange={onKindChange}
            placeholder="Select what to list"
            width={40}
            isClearable
          />
        </InlineField>
      </InlineFieldRow>

      {needsCollector && (
        <InlineFieldRow>
          <InlineField
            label="Collector"
            labelWidth={LABEL_WIDTH}
            tooltip="A collector id, or another variable such as $collector"
            grow
          >
            <Combobox
              options={[]}
              value={query?.agentId || null}
              onChange={onTextChange('agentId')}
              placeholder="$collector"
              width={40}
              isClearable
              createCustomValue
              customValueDescription="Use this value"
            />
          </InlineField>
        </InlineFieldRow>
      )}

      {needsDevice && (
        <InlineFieldRow>
          <InlineField
            label="Device"
            labelWidth={LABEL_WIDTH}
            tooltip="A device id, or another variable such as $device"
            grow
          >
            <Combobox
              options={[]}
              value={query?.deviceId || null}
              onChange={onTextChange('deviceId')}
              placeholder="$device"
              width={40}
              isClearable
              createCustomValue
              customValueDescription="Use this value"
            />
          </InlineField>
        </InlineFieldRow>
      )}
    </>
  );
}

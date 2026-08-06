import React, { ChangeEvent } from 'react';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { FieldSet, InlineField, Input, SecretInput, TextLink } from '@grafana/ui';

import { DomotzDataSourceOptions, DomotzSecureJsonData } from '../types';
import { UsagePanel } from './UsagePanel';

type Props = DataSourcePluginOptionsEditorProps<DomotzDataSourceOptions, DomotzSecureJsonData>;

const DEFAULT_API_URL = 'https://api-eu-west-1-cell-1.domotz.com/public-api/v1/';
const LABEL_WIDTH = 18;

export function ConfigEditor({ onOptionsChange, options }: Props) {
  const { jsonData, secureJsonFields, secureJsonData } = options;

  const onPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    onOptionsChange({ ...options, jsonData: { ...jsonData, path: event.target.value } });
  };

  const onAPIKeyChange = (event: ChangeEvent<HTMLInputElement>) => {
    onOptionsChange({ ...options, secureJsonData: { apiKey: event.target.value } });
  };

  const onResetAPIKey = () => {
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, apiKey: false },
      secureJsonData: { ...secureJsonData, apiKey: '' },
    });
  };

  return (
    <>
      <FieldSet label="Domotz API">
        <InlineField
          label="API URL"
          labelWidth={LABEL_WIDTH}
          interactive
          tooltip="The Public API endpoint for your cell. Find it in the Domotz portal under API keys."
        >
          <Input
            id="config-editor-path"
            onChange={onPathChange}
            value={jsonData.path ?? ''}
            placeholder={DEFAULT_API_URL}
            width={52}
          />
        </InlineField>

        <InlineField
          label="API key"
          labelWidth={LABEL_WIDTH}
          interactive
          tooltip="Stored encrypted on the Grafana server and never exposed to the browser."
        >
          <SecretInput
            required
            id="config-editor-api-key"
            isConfigured={Boolean(secureJsonFields?.apiKey)}
            value={secureJsonData?.apiKey ?? ''}
            placeholder="Enter your API key"
            width={52}
            onReset={onResetAPIKey}
            onChange={onAPIKeyChange}
          />
        </InlineField>

        <p>
          <TextLink href="https://help.domotz.com/admin-global-features/domotz-api/" external>
            How to create a Domotz API key
          </TextLink>
        </p>
      </FieldSet>

      {/* Only renders once the data source has been saved and has a uid. */}
      <UsagePanel uid={options.uid} configured={Boolean(secureJsonFields?.apiKey)} />
    </>
  );
}

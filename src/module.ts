import { DataSourcePlugin } from '@grafana/data';

import { DataSource } from './datasource';
import { ConfigEditor } from './components/ConfigEditor';
import { QueryEditor } from './components/QueryEditor';
import { DomotzDataSourceOptions, DomotzQuery } from './types';

export const plugin = new DataSourcePlugin<DataSource, DomotzQuery, DomotzDataSourceOptions>(DataSource)
  .setConfigEditor(ConfigEditor)
  .setQueryEditor(QueryEditor);

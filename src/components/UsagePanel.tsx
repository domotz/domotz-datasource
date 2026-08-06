import React, { useEffect, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2 } from '@grafana/data';
import { getDataSourceSrv } from '@grafana/runtime';
import { Alert, FieldSet, LoadingPlaceholder, useStyles2 } from '@grafana/ui';

import { DataSource } from '../datasource';
import { errorText } from '../errors';
import { Usage } from '../types';

interface Props {
  uid: string;
  configured: boolean;
}

/**
 * Daily API quota for the configured key.
 *
 * A plain bar, not a chart: this replaces a recharts pie that pulled recharts,
 * decimal.js and prop-types into the bundle to render two numbers.
 *
 * It lives inside the config editor rather than a separate config page because
 * PluginConfigPageProps exposes no handle on the data source instance, whereas
 * the config editor receives its uid directly.
 */
export function UsagePanel({ uid, configured }: Props) {
  const styles = useStyles2(getStyles);
  const [usage, setUsage] = useState<Usage | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!uid || !configured) {
      return;
    }
    let cancelled = false;
    setLoading(true);

    getDataSourceSrv()
      .get(uid)
      .then((ds) => (ds as DataSource).getUsage())
      .then((result) => !cancelled && setUsage(result))
      .catch((err) => !cancelled && setError(errorText(err)))
      .finally(() => !cancelled && setLoading(false));

    return () => {
      cancelled = true;
    };
  }, [uid, configured]);

  // Nothing useful to show before the data source has been saved with a key.
  if (!uid || !configured) {
    return null;
  }

  return (
    <FieldSet label="API usage">
      {loading && <LoadingPlaceholder text="Loading API usage..." />}

      {error && !loading && (
        <Alert title="Could not load API usage" severity="warning">
          {error}
        </Alert>
      )}

      {usage && !loading && !error && renderUsage(usage, styles)}
    </FieldSet>
  );
}

function renderUsage(usage: Usage, styles: ReturnType<typeof getStyles>) {
  if (usage.daily_limit <= 0) {
    return <Alert title="No quota reported for this key" severity="info" />;
  }

  const used = Math.min(usage.daily_usage, usage.daily_limit);
  const percent = (used / usage.daily_limit) * 100;

  return (
    <div className={styles.wrapper}>
      <div
        className={styles.track}
        role="meter"
        aria-label="Daily API usage"
        aria-valuenow={used}
        aria-valuemin={0}
        aria-valuemax={usage.daily_limit}
      >
        <div className={styles.fill} style={{ width: `${Math.max(percent, 0.5)}%` }} />
      </div>
      <p className={styles.caption}>
        {usage.daily_usage.toLocaleString()} of {usage.daily_limit.toLocaleString()} requests used today (
        {percent.toFixed(percent < 1 ? 2 : 1)}%)
      </p>
      <p className={styles.note}>
        Dashboards consume this quota on every refresh. Collector, device and metric lists are cached in the plugin
        backend for five minutes to keep that cost down.
      </p>
    </div>
  );
}

const getStyles = (theme: GrafanaTheme2) => ({
  wrapper: css({ maxWidth: '600px' }),
  track: css({
    width: '100%',
    height: theme.spacing(1.5),
    borderRadius: theme.shape.radius.pill,
    backgroundColor: theme.colors.background.secondary,
    overflow: 'hidden',
  }),
  fill: css({
    height: '100%',
    backgroundColor: theme.colors.primary.main,
    transition: 'width 200ms ease',
  }),
  caption: css({ marginTop: theme.spacing(1), color: theme.colors.text.primary }),
  note: css({ color: theme.colors.text.secondary, fontSize: theme.typography.bodySmall.fontSize }),
});

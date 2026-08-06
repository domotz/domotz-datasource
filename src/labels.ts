import { ComboboxOption } from '@grafana/ui';

import { Device, Variable } from './types';

/**
 * The one separator used for every compound label and description in the
 * editor, so device labels, metric labels and their second lines all read the
 * same way. Change it here or not at all.
 */
export const SEPARATOR = ' · ';

/** Joins the parts that are actually present, with the shared separator. */
export function join(...parts: Array<string | undefined | null>): string {
  return parts.filter((p): p is string => Boolean(p && p.trim())).join(SEPARATOR);
}

/**
 * Filters options on their label *and* their description.
 *
 * Combobox only searches the label when it is handed a plain array:
 * `itemToString` returns `label ?? value` and never reads `description`. Given
 * an async loader it does no filtering of its own at all, so supplying one and
 * matching here is what lets the second line stay searchable while the label
 * stays short.
 *
 * Matching is token-wise substring rather than fuzzy, because the things on
 * the second line are addresses and sensor paths: typing "192.168.11" should
 * mean that prefix, not a scattering of those characters. Tokens may match in
 * any order, so "nas 192" and "192 nas" both work.
 */
export function filterOptions<T extends ComboboxOption<string>>(options: T[], input: string): T[] {
  const tokens = input.toLowerCase().split(/\s+/).filter(Boolean);
  if (!tokens.length) {
    return options;
  }
  return options.filter((option) => {
    const haystack = `${option.label ?? ''} ${option.description ?? ''}`.toLowerCase();
    return tokens.every((token) => haystack.includes(token));
  });
}

/**
 * Device picker option: the name on the first line, addresses on the second.
 *
 * Both lines are searchable - see filterOptions - so keeping the address off
 * the label costs nothing in findability and makes a list of a hundred devices
 * scannable.
 */
export function deviceOption(device: Device): ComboboxOption<string> {
  const name = device.display_name?.trim() || `Device ${device.id}`;
  const ip = device.ip_addresses?.[0];

  return {
    label: name,
    value: String(device.id),
    // An unidentified device is named after its own address; repeating it on
    // the second line would read "192.168.11.211 · 192.168.11.211 · 02:42:...".
    description: join(ip === name ? undefined : ip, device.hw_address) || undefined,
  };
}

/**
 * Metric picker options, disambiguated only where they need to be.
 *
 * Some devices expose several variables resolving to exactly the same label -
 * a QNAP reports `/sys/fs/cgroup/systemd - Free Size` from two sensors - and
 * the picker then offers indistinguishable entries. `path` carries the sensor
 * path and its index, so it is what tells them apart.
 *
 * The path is appended to the *label* only for labels that actually collide,
 * because there it is the only way to see which is which. Every metric carries
 * its path on the second line regardless, and that line is searchable, so the
 * label does not have to grow just to make the path findable.
 */
export function variableOptions(variables: Variable[]): Array<ComboboxOption<string>> {
  const counts = new Map<string, number>();
  for (const v of variables) {
    counts.set(v.displayLabel, (counts.get(v.displayLabel) ?? 0) + 1);
  }

  return variables.map((v) => {
    const ambiguous = (counts.get(v.displayLabel) ?? 0) > 1;
    return {
      label: ambiguous ? join(v.displayLabel, shortPath(v.path)) : v.displayLabel,
      value: String(v.id),
      description: describeVariable(v),
    };
  });
}

/** How many trailing path segments identify a sensor instance. */
const PATH_SEGMENTS = 2;

/**
 * The tail of a sensor path - normally `<index>/<leaf>`.
 *
 * Full paths run to ninety-odd characters
 * (`snmp/preset/host-resources-fixed-disks/host_resources_..._table/34/storage-free-size`)
 * and the dropdown truncates them, which hides the index in the middle -
 * precisely the part that tells two otherwise identical metrics apart. The
 * last couple of segments carry the index and the leaf, and fit.
 */
export function shortPath(path: string, segments = PATH_SEGMENTS): string {
  const parts = path.split('/').filter(Boolean);
  if (parts.length <= segments) {
    return parts.join('/');
  }
  return `…/${parts.slice(-segments).join('/')}`;
}

/** Second line for a metric: its current reading, unit, path tail and history flag. */
export function describeVariable(v: Variable): string | undefined {
  const reading = join(v.unit ? `${v.value} ${v.unit}` : v.value);
  const description = join(reading, shortPath(v.path), v.has_history ? undefined : 'no history');
  return description || undefined;
}

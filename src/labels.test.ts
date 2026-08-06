import { SEPARATOR, describeVariable, deviceOption, filterOptions, join, shortPath, variableOptions } from './labels';
import { Device, Variable } from './types';

function device(over: Partial<Device> = {}): Device {
  return { id: 1, display_name: 'NAS', ...over };
}

function variable(over: Partial<Variable> = {}): Variable {
  return {
    id: 1,
    label: 'Free Size',
    metric: '',
    path: 'storage/14/free',
    unit: 'B',
    has_history: true,
    value: '0.0',
    displayLabel: 'Free Size',
    ...over,
  };
}

describe('separator consistency', () => {
  // The separator is shared so device labels, metric labels and every second
  // line read the same way. A drift here is exactly what makes the editor look
  // inconsistent.
  it('uses one separator everywhere', () => {
    const d = deviceOption(device({ ip_addresses: ['10.0.0.12'], hw_address: 'AA:BB' })).description!;
    const m = variableOptions([variable({ id: 1 }), variable({ id: 2 })])[0];

    expect(d).toContain(SEPARATOR);
    expect(m.label).toContain(SEPARATOR);
    expect(m.description).toContain(SEPARATOR);
    for (const text of [d, m.label!, m.description!]) {
      expect(text).not.toMatch(/\s{2,}/);
      expect(text).not.toMatch(/·\S|\S·/);
    }
  });

  it('omits parts that are missing instead of leaving empty slots', () => {
    expect(join('a', undefined, 'b', '', null)).toBe(`a${SEPARATOR}b`);
  });
});

describe('deviceOption', () => {
  // The label stays short and scannable; the addresses go on the second line,
  // which filterOptions still searches.
  it('keeps the name on the label and the addresses on the second line', () => {
    const o = deviceOption(device({ ip_addresses: ['192.168.1.20'], hw_address: '00:08:9B:CD:12:34' }));
    expect(o.label).toBe('NAS');
    expect(o.description).toBe('192.168.1.20 · 00:08:9B:CD:12:34');
  });

  it('drops the MAC for devices that report none', () => {
    expect(deviceOption(device({ display_name: 'Laptop', ip_addresses: ['192.168.1.55'] })).description).toBe(
      '192.168.1.55'
    );
  });

  it('has no second line when the device reports no addresses', () => {
    const o = deviceOption(device({ display_name: 'Hue Bulb' }));
    expect(o.label).toBe('Hue Bulb');
    expect(o.description).toBeUndefined();
  });

  it('uses the first address when a device has several', () => {
    expect(deviceOption(device({ ip_addresses: ['10.0.0.1', '10.0.0.2'] })).description).toBe('10.0.0.1');
  });

  it('falls back to the id when a device has no name', () => {
    expect(deviceOption(device({ id: 77, display_name: '' })).label).toBe('Device 77');
  });

  // Unidentified devices are named after their own address upstream, which
  // otherwise rendered the IP on both lines.
  it('does not repeat the IP when it is also the device name', () => {
    const o = deviceOption(
      device({ display_name: '192.168.11.211', ip_addresses: ['192.168.11.211'], hw_address: '02:42:1B:92:D1:C8' })
    );
    expect(o.label).toBe('192.168.11.211');
    expect(o.description).toBe('02:42:1B:92:D1:C8');
  });
});

describe('filterOptions', () => {
  const devices = [
    deviceOption(device({ id: 1, display_name: 'NAS', ip_addresses: ['192.168.11.200'], hw_address: 'AA:BB:CC' })),
    deviceOption(device({ id: 2, display_name: 'Printer', ip_addresses: ['192.168.11.37'], hw_address: 'DD:EE:FF' })),
  ];

  it('matches the label', () => {
    expect(filterOptions(devices, 'nas').map((o) => o.value)).toEqual(['1']);
  });

  // The whole point of moving addresses off the label.
  it('matches the second line, so IP and MAC stay searchable', () => {
    expect(filterOptions(devices, '192.168.11.37').map((o) => o.value)).toEqual(['2']);
    expect(filterOptions(devices, 'dd:ee').map((o) => o.value)).toEqual(['2']);
  });

  it('matches metric paths and units on the second line', () => {
    const metrics = variableOptions([
      variable({ id: 1, displayLabel: 'In Traffic', path: 'snmp/if/2/in-traffic', unit: 'B/s', value: '100' }),
      variable({ id: 2, displayLabel: 'CPU Usage', path: 'system/cpu/usage', unit: '%', value: '23' }),
    ]);
    expect(filterOptions(metrics, 'in-traffic').map((o) => o.value)).toEqual(['1']);
    expect(filterOptions(metrics, 'cpu/usage').map((o) => o.value)).toEqual(['2']);
  });

  it('requires every token but ignores their order', () => {
    expect(filterOptions(devices, 'nas 192').map((o) => o.value)).toEqual(['1']);
    expect(filterOptions(devices, '192 nas').map((o) => o.value)).toEqual(['1']);
    expect(filterOptions(devices, 'nas printer')).toEqual([]);
  });

  it('returns everything for an empty search', () => {
    expect(filterOptions(devices, '   ')).toHaveLength(2);
  });

  it('is case insensitive', () => {
    expect(filterOptions(devices, 'PRINTER').map((o) => o.value)).toEqual(['2']);
  });
});

describe('variableOptions', () => {
  it('leaves unique labels untouched', () => {
    const options = variableOptions([
      variable({ id: 1, displayLabel: 'In Traffic' }),
      variable({ id: 2, displayLabel: 'Out Traffic' }),
    ]);
    expect(options.map((o) => o.label)).toEqual(['In Traffic', 'Out Traffic']);
  });

  // Real case: a QNAP reports this same label from two different sensors, and
  // the picker offered two indistinguishable entries.
  it('disambiguates colliding labels with the path', () => {
    const options = variableOptions([
      variable({ id: 14141240, displayLabel: '/sys/fs/cgroup/systemd - Free Size', path: 'storage/14/free' }),
      variable({ id: 14408129, displayLabel: '/sys/fs/cgroup/systemd - Free Size', path: 'storage/27/free' }),
      variable({ id: 999, displayLabel: 'CPU Usage', path: 'system/cpu/usage' }),
    ]);

    expect(options[0].label).toBe('/sys/fs/cgroup/systemd - Free Size · …/14/free');
    expect(options[1].label).toBe('/sys/fs/cgroup/systemd - Free Size · …/27/free');
    // Both keep their index, which is what tells them apart.
    expect(options[0].label).not.toBe(options[1].label);
    expect(options[2].label).toBe('CPU Usage');
  });

  it('disambiguates every member of a collision, not just the later ones', () => {
    const options = variableOptions([
      variable({ id: 1, displayLabel: 'Dup', path: 'a/1' }),
      variable({ id: 2, displayLabel: 'Dup', path: 'a/2' }),
      variable({ id: 3, displayLabel: 'Dup', path: 'a/3' }),
    ]);
    expect(new Set(options.map((o) => o.label)).size).toBe(3);
    options.forEach((o) => expect(o.label).toContain('Dup · a/'));
  });

  it('keeps the id as the option value', () => {
    expect(variableOptions([variable({ id: 42 })])[0].value).toBe('42');
  });
});

describe('describeVariable', () => {
  it('shows reading, unit and path', () => {
    expect(describeVariable(variable({ value: '95', unit: '%', path: 'system/ram/usage' }))).toBe('95 % · …/ram/usage');
  });

  it('marks metrics that keep no history', () => {
    expect(describeVariable(variable({ has_history: false }))).toContain('no history');
  });

  it('omits the reading when the metric has no value yet', () => {
    expect(describeVariable(variable({ value: '', unit: '', path: 'a/b' }))).toBe('a/b');
  });
});

describe('shortPath', () => {
  // Real path from a QNAP: the index sits in the middle and the dropdown
  // truncates it away, which is exactly the character that disambiguates.
  it('keeps the index and leaf from a long path', () => {
    const long =
      'snmp/preset/host-resources-fixed-disks/host_resources_partitions_and_volumes_table/34/storage-free-size';
    expect(shortPath(long)).toBe('…/34/storage-free-size');
    expect(shortPath(long).length).toBeLessThan(30);
  });

  it('leaves short paths whole, with no ellipsis', () => {
    expect(shortPath('a/b')).toBe('a/b');
    expect(shortPath('single')).toBe('single');
  });

  it('tolerates empty and trailing-slash paths', () => {
    expect(shortPath('')).toBe('');
    expect(shortPath('a/b/c/')).toBe('…/b/c');
  });
});

/**
 * FilterWidgetApt — Phase 8 Plan 04 (MIRROR-16..21).
 *
 * Renders the APT / Debian-specific `SyncFilter` form used by both
 * CreateRepoDialog and RepoSettingsTab's Mirror config card. Emits a
 * JSON shape matching the Go struct in
 * internal/protocol/deb/upstream_parse.go:SyncFilter:
 *
 *   type AptFilter = {
 *     Suites?: string[];
 *     Components?: string[];
 *     Arches?: string[];
 *     Names?: string[];
 *     Globs?: string[];
 *   }
 *
 * PASCAL CASE is intentional — the Go SyncFilter struct carries NO
 * `json:` tags, so encoding/json serialises the field names verbatim.
 * The backend validator (internal/api/mirror_validate.go) round-trips
 * against these exact keys. Do not rename.
 *
 * UX notes:
 *   - Suites is a comma-separated free-text field (e.g. "focal,jammy").
 *   - Components shows four hard-coded checkboxes (main / universe /
 *     restricted / multiverse) plus a free-text "other" for custom
 *     third-party repo components.
 *   - Arches shows three hard-coded checkboxes (amd64 / arm64 / i386)
 *     plus a free-text "other" entry.
 *   - Names + Globs are plain comma-separated free-text.
 *   - Empty arrays normalise to `undefined` on emit so JSON.stringify
 *     omits the key. The backend treats empty == mirror-everything.
 */

import { Checkbox } from '@/components/ui/checkbox';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { AptFilter } from '@/api/types';

export interface FilterWidgetAptProps {
  value: AptFilter;
  onChange: (next: AptFilter) => void;
  disabled?: boolean;
}

const DEFAULT_COMPONENTS = ['main', 'universe', 'restricted', 'multiverse'];
const DEFAULT_ARCHES = ['amd64', 'arm64', 'i386'];

// csvToArr splits a comma-separated user input into trimmed non-empty
// strings. "focal, jammy," → ["focal", "jammy"]. Empty string → [].
function csvToArr(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

function arrToCsv(arr: string[] | undefined): string {
  return (arr ?? []).join(', ');
}

// setOrUndef emits `undefined` for empty arrays so JSON payloads omit
// the key entirely. The backend validator treats a missing key as
// "mirror everything in this dimension" — the same semantic as an
// empty array, but smaller on the wire and easier to grep for in logs.
function setOrUndef<T>(arr: T[]): T[] | undefined {
  return arr.length === 0 ? undefined : arr;
}

export function FilterWidgetApt({
  value,
  onChange,
  disabled,
}: FilterWidgetAptProps) {
  const components = value.Components ?? [];
  const arches = value.Arches ?? [];

  // "Other" free-text entries are the current set of components/arches
  // that aren't in the default list. Stored as a CSV string in the
  // local text field so the user can type freely.
  const otherComponents = components
    .filter((c) => !DEFAULT_COMPONENTS.includes(c));
  const otherArches = arches.filter((a) => !DEFAULT_ARCHES.includes(a));

  const toggleComponent = (c: string, checked: boolean) => {
    const next = checked
      ? Array.from(new Set([...components, c]))
      : components.filter((x) => x !== c);
    onChange({ ...value, Components: setOrUndef(next) });
  };
  const toggleArch = (a: string, checked: boolean) => {
    const next = checked
      ? Array.from(new Set([...arches, a]))
      : arches.filter((x) => x !== a);
    onChange({ ...value, Arches: setOrUndef(next) });
  };

  const updateOtherComponents = (raw: string) => {
    const otherNext = csvToArr(raw);
    const kept = components.filter((c) => DEFAULT_COMPONENTS.includes(c));
    onChange({ ...value, Components: setOrUndef([...kept, ...otherNext]) });
  };
  const updateOtherArches = (raw: string) => {
    const otherNext = csvToArr(raw);
    const kept = arches.filter((a) => DEFAULT_ARCHES.includes(a));
    onChange({ ...value, Arches: setOrUndef([...kept, ...otherNext]) });
  };

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="apt-suites">Suites (comma-separated)</Label>
        <Input
          id="apt-suites"
          placeholder="focal, jammy"
          value={arrToCsv(value.Suites)}
          onChange={(e) =>
            onChange({ ...value, Suites: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
        <p className="text-xs text-muted-foreground">
          Leave blank to sync every suite the upstream Release advertises.
        </p>
      </div>

      <div className="space-y-2">
        <Label>Components</Label>
        <div className="flex flex-wrap gap-x-4 gap-y-2 text-sm">
          {DEFAULT_COMPONENTS.map((c) => {
            const checked = components.includes(c);
            return (
              <label
                key={c}
                className="inline-flex items-center gap-2 cursor-pointer"
              >
                <Checkbox
                  checked={checked}
                  onCheckedChange={(v) => toggleComponent(c, v === true)}
                  disabled={disabled}
                />
                <span className="font-mono">{c}</span>
              </label>
            );
          })}
        </div>
        <Input
          placeholder="other (comma-separated)"
          value={arrToCsv(otherComponents)}
          onChange={(e) => updateOtherComponents(e.target.value)}
          disabled={disabled}
          aria-label="Other components"
        />
      </div>

      <div className="space-y-2">
        <Label>Architectures</Label>
        <div className="flex flex-wrap gap-x-4 gap-y-2 text-sm">
          {DEFAULT_ARCHES.map((a) => {
            const checked = arches.includes(a);
            return (
              <label
                key={a}
                className="inline-flex items-center gap-2 cursor-pointer"
              >
                <Checkbox
                  checked={checked}
                  onCheckedChange={(v) => toggleArch(a, v === true)}
                  disabled={disabled}
                />
                <span className="font-mono">{a}</span>
              </label>
            );
          })}
        </div>
        <Input
          placeholder="other (comma-separated)"
          value={arrToCsv(otherArches)}
          onChange={(e) => updateOtherArches(e.target.value)}
          disabled={disabled}
          aria-label="Other architectures"
        />
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="apt-names">Package names (optional allowlist)</Label>
        <Input
          id="apt-names"
          placeholder="nginx, curl, openssh-server"
          value={arrToCsv(value.Names)}
          onChange={(e) =>
            onChange({ ...value, Names: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
        <p className="text-xs text-muted-foreground">
          Leave blank to mirror every package the upstream Packages index
          lists.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="apt-globs">Filename globs (optional)</Label>
        <Input
          id="apt-globs"
          placeholder="*-dev_*.deb"
          value={arrToCsv(value.Globs)}
          onChange={(e) =>
            onChange({ ...value, Globs: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
      </div>
    </div>
  );
}

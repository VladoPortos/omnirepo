/**
 * FilterWidgetRpm.
 *
 * RPM's SyncFilter struct (internal/protocol/rpm/upstream_parse.go)
 * carries `Names []string` + `Globs []string` — no Arches (arch
 * filtering is explicitly out of scope; if operators need it, the Go
 * struct grows first).
 *
 * Wire JSON: `{ Names?: string[], Globs?: string[] }` — PascalCase
 * because encoding/json serialises Go field names verbatim when no
 * `json:` tag is present.
 */

import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { RpmFilter } from '@/api/types';

export interface FilterWidgetRpmProps {
  value: RpmFilter;
  onChange: (next: RpmFilter) => void;
  disabled?: boolean;
}

function csvToArr(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

function arrToCsv(arr: string[] | undefined): string {
  return (arr ?? []).join(', ');
}

function setOrUndef<T>(arr: T[]): T[] | undefined {
  return arr.length === 0 ? undefined : arr;
}

export function FilterWidgetRpm({
  value,
  onChange,
  disabled,
}: FilterWidgetRpmProps) {
  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="rpm-names">Package names (optional allowlist)</Label>
        <Input
          id="rpm-names"
          placeholder="kernel, podman, openssh-server"
          value={arrToCsv(value.Names)}
          onChange={(e) =>
            onChange({ ...value, Names: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
        <p className="text-xs text-muted-foreground">
          Leave blank to mirror every package the upstream primary.xml
          lists.
        </p>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="rpm-globs">Filename globs (optional)</Label>
        <Input
          id="rpm-globs"
          placeholder="*-devel-*.rpm, *-debuginfo-*.rpm"
          value={arrToCsv(value.Globs)}
          onChange={(e) =>
            onChange({ ...value, Globs: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
        <p className="text-xs text-muted-foreground">
          Match on the package filename. Combine with the allowlist for
          narrow subset mirrors.
        </p>
      </div>
    </div>
  );
}

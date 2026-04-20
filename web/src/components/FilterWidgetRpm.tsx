/**
 * FilterWidgetRpm — Phase 8 Plan 04 (MIRROR-16..21).
 *
 * RPM's SyncFilter struct (internal/protocol/rpm/upstream_parse.go)
 * carries only `Names []string` — no Arches, no Globs. This widget
 * reflects that truth. If an operator needs arch filtering, the Go
 * struct has to grow the field first; that is explicitly out of scope
 * for Phase 8 per plan 08-04 Task 1 notes.
 *
 * Wire JSON: `{ Names?: string[] }` — PascalCase because encoding/json
 * serialises Go field names verbatim when no `json:` tag is present.
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
    </div>
  );
}

/**
 * FilterWidgetPypi — Phase 8 Plan 04 (MIRROR-16..21).
 *
 * PyPI SyncFilter (internal/protocol/pypi/upstream_parse.go) exposes
 * two fields: Names (project allowlist) + Globs (filename pattern
 * match against .whl / .tar.gz etc). Both optional; empty == mirror
 * everything.
 *
 * Wire JSON: `{ Names?: string[]; Globs?: string[] }` — PascalCase.
 */

import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { PypiFilter } from '@/api/types';

export interface FilterWidgetPypiProps {
  value: PypiFilter;
  onChange: (next: PypiFilter) => void;
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

export function FilterWidgetPypi({
  value,
  onChange,
  disabled,
}: FilterWidgetPypiProps) {
  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="pypi-names">Project names (optional allowlist)</Label>
        <Input
          id="pypi-names"
          placeholder="numpy, pandas, scipy"
          value={arrToCsv(value.Names)}
          onChange={(e) =>
            onChange({ ...value, Names: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
        <p className="text-xs text-muted-foreground">
          Project names are matched case-insensitively and PEP 503
          normalised by the backend. Leave blank to mirror every project
          the upstream index lists.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="pypi-globs">Filename globs (optional)</Label>
        <Input
          id="pypi-globs"
          placeholder="*-py3-none-any.whl"
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

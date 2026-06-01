/**
 * FilterWidgetHelm — Phase 8 Plan 04 (MIRROR-16..21).
 *
 * Helm SyncFilter (internal/protocol/helm/upstream_parse.go) exposes
 * Names (chart allowlist) + Globs (filename pattern). Identical shape
 * to PyPI but the semantics are chart names, not project names.
 *
 * Wire JSON: `{ Names?: string[]; Globs?: string[] }` — PascalCase.
 */

import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { HelmFilter } from '@/api/types';

export interface FilterWidgetHelmProps {
  value: HelmFilter;
  onChange: (next: HelmFilter) => void;
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

export function FilterWidgetHelm({
  value,
  onChange,
  disabled,
}: FilterWidgetHelmProps) {
  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="helm-names">Chart names (optional allowlist)</Label>
        <Input
          id="helm-names"
          placeholder="redis, postgresql, nginx"
          value={arrToCsv(value.Names)}
          onChange={(e) =>
            onChange({ ...value, Names: setOrUndef(csvToArr(e.target.value)) })
          }
          disabled={disabled}
        />
        <p className="text-xs text-muted-foreground">
          Chart names are matched case-insensitively. Leave blank to
          mirror every chart the upstream index.yaml lists.
        </p>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="helm-globs">Filename globs (optional)</Label>
        <Input
          id="helm-globs"
          placeholder="*-17.*.tgz"
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

/**
 * MirrorConfigSection — Phase 8 Plan 04 (MIRROR-16..21).
 *
 * Shared widget for the mirror-at-creation form (CreateRepoDialog) and
 * the Mirror config card in RepoSettingsTab. Composes the four
 * protocol-specific FilterWidgets plus the upstream URL / cred picker /
 * scan toggle affordances and emits a single MirrorConfigValue.
 *
 * Behavioural contract:
 *   - Only appears for protocol ∈ {deb, rpm, pypi, helm}. The caller is
 *     responsible for the protocol-gate; this component trusts the
 *     `protocol` prop.
 *   - `hideCheckbox` skips the "is_mirror" opt-in checkbox and forces
 *     the content region open. Used by RepoSettingsTab where the card
 *     only renders for repos that are already mirrors.
 *   - `urlReadonly` renders the upstream URL input as readonly (the
 *     backend enforces immutability via 400 repo.mirror_url_immutable
 *     per D-02). Used by RepoSettingsTab.
 *
 * Wire contract: the `mirror_filter` key inside MirrorConfigValue
 * serialises to the protocol's SyncFilter JSON with PascalCase keys
 * (Names, Globs, Suites, Components, Arches) matching the Go struct
 * field defaults in internal/protocol/{deb,rpm,pypi,helm}/upstream_parse.go.
 */

import { Checkbox } from '@/components/ui/checkbox';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { FilterWidgetApt } from './FilterWidgetApt';
import { FilterWidgetRpm } from './FilterWidgetRpm';
import { FilterWidgetPypi } from './FilterWidgetPypi';
import { FilterWidgetHelm } from './FilterWidgetHelm';
import { useUpstreamCreds } from '@/api/queries';
import type {
  AnyFilter,
  AptFilter,
  HelmFilter,
  MirrorConfigValue,
  PypiFilter,
  RpmFilter,
} from '@/api/types';

export type MirrorProtocol = 'deb' | 'rpm' | 'pypi' | 'helm';

export interface MirrorConfigSectionProps {
  protocol: MirrorProtocol;
  projectName: string;
  value: MirrorConfigValue;
  onChange: (next: MirrorConfigValue) => void;
  /** RepoSettingsTab: upstream URL is immutable post-creation. */
  urlReadonly?: boolean;
  /** RepoSettingsTab: the card only shows on existing mirror repos, so
   *  the "This repo is a mirror…" checkbox is redundant there. */
  hideCheckbox?: boolean;
  disabled?: boolean;
}

// protocolCredKinds maps the UI protocol token to the `kind` values the
// backend's upstream_creds table uses. "deb" in UI == "apt" in the cred
// kind column (historical: the UI settled on "deb" as the repo-type
// token but the cred kind was named after the tool, not the packaging
// format).
const protocolCredKinds: Record<MirrorProtocol, string[]> = {
  deb: ['apt', 'deb'],
  rpm: ['rpm'],
  pypi: ['pypi'],
  helm: ['helm'],
};

function renderFilterWidget(
  protocol: MirrorProtocol,
  value: AnyFilter,
  onChange: (next: AnyFilter) => void,
  disabled?: boolean,
) {
  switch (protocol) {
    case 'deb':
      return (
        <FilterWidgetApt
          value={value as AptFilter}
          onChange={(next) => onChange(next)}
          disabled={disabled}
        />
      );
    case 'rpm':
      return (
        <FilterWidgetRpm
          value={value as RpmFilter}
          onChange={(next) => onChange(next)}
          disabled={disabled}
        />
      );
    case 'pypi':
      return (
        <FilterWidgetPypi
          value={value as PypiFilter}
          onChange={(next) => onChange(next)}
          disabled={disabled}
        />
      );
    case 'helm':
      return (
        <FilterWidgetHelm
          value={value as HelmFilter}
          onChange={(next) => onChange(next)}
          disabled={disabled}
        />
      );
  }
}

export function MirrorConfigSection({
  protocol,
  projectName,
  value,
  onChange,
  urlReadonly,
  hideCheckbox,
  disabled,
}: MirrorConfigSectionProps) {
  const credsQ = useUpstreamCreds(projectName);
  const allowedKinds = protocolCredKinds[protocol];
  const filteredCreds = (credsQ.data ?? []).filter((c) =>
    allowedKinds.includes(c.kind),
  );

  const isOpen = hideCheckbox || value.is_mirror;

  return (
    <div className="space-y-4 rounded-lg border border-dashed p-4">
      {!hideCheckbox && (
        <label className="inline-flex items-start gap-2 cursor-pointer">
          <Checkbox
            checked={value.is_mirror}
            onCheckedChange={(v) =>
              onChange({ ...value, is_mirror: v === true })
            }
            disabled={disabled}
            aria-label="This repo is a mirror of an upstream"
          />
          <span className="text-sm">
            <span className="font-medium">
              This repo is a mirror of an upstream
            </span>
            <span className="block text-xs text-muted-foreground">
              Uploads will be disabled. A background job pulls from the
              configured URL.
            </span>
          </span>
        </label>
      )}

      {isOpen && (
        <div className="space-y-4 pl-6">
          <div className="space-y-1.5">
            <Label htmlFor="mirror-url">Upstream URL</Label>
            <Input
              id="mirror-url"
              type="url"
              placeholder={protocolPlaceholder(protocol)}
              value={value.mirror_upstream_url}
              readOnly={urlReadonly}
              disabled={disabled}
              onChange={(e) =>
                onChange({ ...value, mirror_upstream_url: e.target.value })
              }
              required
            />
            {urlReadonly && (
              <p className="text-xs text-muted-foreground">
                URL cannot be changed after creation. Delete and recreate the
                repo to point at a different upstream.
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label>Filters</Label>
            {renderFilterWidget(
              protocol,
              value.mirror_filter,
              (next) => onChange({ ...value, mirror_filter: next }),
              disabled,
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="mirror-cred">Credential (optional)</Label>
            <select
              id="mirror-cred"
              value={value.mirror_cred_id == null ? '' : String(value.mirror_cred_id)}
              onChange={(e) => {
                const raw = e.target.value;
                if (raw === '') {
                  onChange({ ...value, mirror_cred_id: null });
                  return;
                }
                const n = Number(raw);
                onChange({
                  ...value,
                  mirror_cred_id: Number.isFinite(n) ? n : null,
                });
              }}
              disabled={disabled || credsQ.isLoading}
              className="h-8 w-full rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50"
            >
              <option value="">(none — anonymous access)</option>
              {filteredCreds.map((c) => (
                <option key={c.id} value={String(c.id)}>
                  {c.host}
                  {c.username ? ` (${c.username})` : ''}
                </option>
              ))}
            </select>
            {filteredCreds.length === 0 && !credsQ.isLoading && (
              <p className="text-xs text-muted-foreground">
                No {protocol}-kind credentials stored for this project. Add one
                under Project settings → Upstream credentials if the upstream
                requires auth.
              </p>
            )}
          </div>

          <label className="inline-flex items-start gap-2 cursor-pointer">
            <Checkbox
              checked={value.scan_on_sync}
              onCheckedChange={(v) =>
                onChange({ ...value, scan_on_sync: v === true })
              }
              disabled={disabled}
              aria-label="Scan synced artifacts with Trivy"
            />
            <span className="text-sm">
              <span className="font-medium">
                Scan synced artifacts with Trivy
              </span>
              <span className="block text-xs text-muted-foreground">
                Scanning thousands of freshly-mirrored packages is slow — off
                by default. Flip on once the mirror is healthy.
              </span>
            </span>
          </label>

          <div className="rounded-md bg-muted/50 p-3 text-xs text-muted-foreground space-y-1">
            <p>Uploads are disabled on mirror repos (403 repo_is_mirror).</p>
            <p>Upstream URL cannot be changed after creation.</p>
          </div>
        </div>
      )}
    </div>
  );
}

function protocolPlaceholder(protocol: MirrorProtocol): string {
  switch (protocol) {
    case 'deb':
      return 'https://archive.ubuntu.com/ubuntu';
    case 'rpm':
      return 'https://mirror.centos.org/centos/9/BaseOS/x86_64/os/';
    case 'pypi':
      return 'https://pypi.org/simple/';
    case 'helm':
      return 'https://charts.bitnami.com/bitnami';
  }
}

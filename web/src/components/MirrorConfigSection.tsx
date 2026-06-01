/**
 * MirrorConfigSection.
 *
 * Shared widget for the mirror-at-creation form (CreateRepoDialog) and
 * the Mirror config card in RepoSettingsTab. Composes the four
 * protocol-specific FilterWidgets plus the upstream URL / cred picker /
 * scan toggle affordances and emits a single MirrorConfigValue.
 *
 * Behavioural contract:
 *   - Only appears for protocol ∈ {deb, rpm, pypi, helm, git}. The caller
 *     is responsible for the protocol-gate; this component trusts the
 *     `protocol` prop. The set includes 'git' (HTTPS+PAT mirror, all-refs,
 *     no filter widget).
 *   - `hideCheckbox` skips the "is_mirror" opt-in checkbox and forces
 *     the content region open. Used by RepoSettingsTab where the card
 *     only renders for repos that are already mirrors.
 *   - `urlReadonly` renders the upstream URL input as readonly (the
 *     backend enforces immutability via 400 repo.mirror_url_immutable).
 *     Used by RepoSettingsTab.
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
import { useRoleFor } from '@/hooks/useAuth';
import type {
  AnyFilter,
  AptFilter,
  HelmFilter,
  MirrorConfigValue,
  PypiFilter,
  RpmFilter,
} from '@/api/types';

export type MirrorProtocol = 'deb' | 'rpm' | 'pypi' | 'helm' | 'git';

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
  /** RepoSettingsTab: the URL is already shown as a CopyInline row at
   *  the top of the Mirror config card. Skip the duplicate input here. */
  hideUrl?: boolean;
  disabled?: boolean;
}

// protocolCredKinds maps the UI protocol token to the `kind` values the
// backend's upstream_creds table uses. The UI's 'deb' repo-type token
// maps to cred kind 'apt' — single canonical value since the 'deb'
// cred-kind alias was retired.
//
// Helm mirrors with oci:// upstreams authenticate via Helm SDK's
// ClientOptBasicAuth — kind='basic' is accepted alongside the
// HTTP-only 'helm' kind.
// Git mirrors authenticate over HTTPS+PAT — kind='basic' only; SSH key
// auth deferred to a later release.
const protocolCredKinds: Record<MirrorProtocol, string[]> = {
  deb: ['apt'],
  rpm: ['rpm'],
  pypi: ['pypi'],
  helm: ['helm', 'basic'],
  git: ['basic'],
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
    case 'git':
      // Git mirror is all-refs (PlainCloneContext with Mirror:true +
      // FetchContext with Tags:AllTags). No filter UI; glob
      // include/exclude deferred to a later release.
      return null;
  }
}

export function MirrorConfigSection({
  protocol,
  projectName,
  value,
  onChange,
  urlReadonly,
  hideCheckbox,
  hideUrl,
  disabled,
}: MirrorConfigSectionProps) {
  const credsQ = useUpstreamCreds(projectName);
  const allowedKinds = protocolCredKinds[protocol];
  const filteredCreds = (credsQ.data ?? []).filter((c) =>
    allowedKinds.includes(c.kind),
  );

  // drift_purge is maintainer-gated. Viewers see the checkbox read-only;
  // non-members never reach this component because the repo settings
  // route gates them upstream.
  const myRole = useRoleFor(projectName);
  const driftGateDisabled = disabled || myRole === 'viewer';

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
            <span className="font-semibold">
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
          {!hideUrl && (
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
            {protocol === 'git' && (
              // Passive LFS-warning helper text. The mirror stores
              // pointer files only — clients cloning via OmniRepo will
              // not resolve LFS objects. Operators self-select; no
              // fetch-before-create round-trip.
              <p
                className="text-xs text-muted-foreground"
                data-testid="git-mirror-lfs-warning"
              >
                Git LFS objects are not mirrored. The mirror stores pointer
                files only; clients cloning via OmniRepo must disable LFS
                (GIT_LFS_SKIP_SMUDGE=1) or source LFS objects separately.
              </p>
            )}
          </div>
          )}

          {protocol !== 'git' && (
            // Git mirror has no filter widget — hide the Filters label
            // entirely so the mirror panel does not show an empty heading.
            <div className="space-y-2">
              <Label>Filters</Label>
              {renderFilterWidget(
                protocol,
                value.mirror_filter,
                (next) => onChange({ ...value, mirror_filter: next }),
                disabled,
              )}
            </div>
          )}

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
              <span className="font-semibold">
                Scan synced artifacts with Trivy
              </span>
              <span className="block text-xs text-muted-foreground">
                Scanning thousands of freshly-mirrored packages is slow — off
                by default. Flip on once the mirror is healthy.
              </span>
            </span>
          </label>

          <label className="inline-flex items-start gap-2 cursor-pointer">
            <Checkbox
              checked={value.drift_purge}
              onCheckedChange={(v) =>
                onChange({ ...value, drift_purge: v === true })
              }
              disabled={driftGateDisabled}
              aria-label="Auto-remove mirror rows whose upstream entry vanished"
            />
            <span className="text-sm">
              <span className="font-semibold">
                Auto-purge rows that vanish from upstream
              </span>
              <span className="block text-xs text-muted-foreground">
                Auto-remove mirror rows whose upstream entry vanished. Purged rows
                go to Trash for the configured retention window.
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
    case 'git':
      // Git mirror upstream over HTTPS+PAT.
      return 'https://github.com/owner/repo.git';
  }
}

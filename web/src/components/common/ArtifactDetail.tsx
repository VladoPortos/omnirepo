/**
 * Shared detail panel for the expanded-row accordion on every repo
 * content page (F-T17 follow-up). Renders a deterministic two-column
 * metadata grid plus an optional scan-findings strip and a download
 * button. Keeps the visual language consistent across RPM / DEB /
 * docker / pypi / helm / raw so users see the same shape everywhere.
 */

import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Download, ShieldAlert } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { formatBytes, formatDate } from '@/lib/format';

export interface ArtifactDetailField {
  label: string;
  value: ReactNode;
}

export interface ArtifactDetailProps {
  title: string;
  /** Optional one-line tagline under the title (e.g., summary, description). */
  subtitle?: string;
  fields: ArtifactDetailField[];
  /** Size in bytes — rendered in the metadata grid if > 0. */
  sizeBytes?: number;
  /** ISO string — rendered in the metadata grid if set. */
  uploadedAt?: string;
  /** Scan findings breakdown. Omitting suppresses the strip entirely. */
  severity?: {
    status: string;
    counts: Record<string, number>;
  };
  /**
   * In-app route to the standalone scan report page. When set, the
   * severity strip renders a "View full scan report" link-button that
   * takes the user to the per-artifact CVE table. Only meaningful when
   * the latest scan exists and has vulnerabilities; callers are
   * expected to guard on scan-status themselves.
   */
  scanReportURL?: string;
  /** Absolute URL the Download button points at; omit to hide. */
  downloadURL?: string;
  downloadLabel?: string;
}

const SEVERITY_ORDER: Array<keyof ArtifactDetailProps['severity'] extends
  infer _ ? 'critical' | 'high' | 'medium' | 'low' : never> = [
  'critical',
  'high',
  'medium',
  'low',
];

const SEVERITY_CLASS: Record<string, string> = {
  critical: 'bg-status-critical text-status-critical-fg',
  high: 'bg-status-high text-status-high-fg',
  medium: 'bg-status-medium text-status-medium-fg',
  low: 'bg-status-low text-status-low-fg',
};

function truncateDigest(d: string): string {
  if (!d) return '';
  const body = d.includes(':') ? d.split(':')[1] : d;
  return body.length > 19 ? `${d.slice(0, d.indexOf(':') + 1 || 0)}${body.slice(0, 16)}…` : d;
}

export function ArtifactDigest({ value }: { value: string }) {
  if (!value) return null;
  return (
    <code className="font-mono text-xs text-muted-foreground" title={value}>
      {truncateDigest(value)}
    </code>
  );
}

export function SeverityStrip({
  status,
  counts,
}: {
  status: string;
  counts: Record<string, number>;
}) {
  const total = SEVERITY_ORDER.reduce((s, k) => s + (counts?.[k] ?? 0), 0);

  if (status === '' || status === 'pending') {
    return (
      <p className="text-xs text-muted-foreground">
        Not yet scanned. Queue a scan to see vulnerability findings.
      </p>
    );
  }
  if (status === 'running') {
    return (
      <p className="text-xs text-muted-foreground">Scan in progress…</p>
    );
  }
  if (status === 'failed') {
    return (
      <p className="text-xs text-destructive">
        Scan failed. Check the Scan Results tab for details.
      </p>
    );
  }
  if (total === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        No vulnerabilities found. Last scan reported clean.
      </p>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      {SEVERITY_ORDER.map((sev) => {
        const n = counts?.[sev] ?? 0;
        if (n === 0) return null;
        return (
          <Badge
            key={sev}
            className={`capitalize ${SEVERITY_CLASS[sev] ?? ''}`}
          >
            {n.toLocaleString()} {sev}
          </Badge>
        );
      })}
    </div>
  );
}

export function ArtifactDetail({
  title,
  subtitle,
  fields,
  sizeBytes,
  uploadedAt,
  severity,
  scanReportURL,
  downloadURL,
  downloadLabel,
}: ArtifactDetailProps) {
  const allFields: ArtifactDetailField[] = [...fields];
  if (typeof sizeBytes === 'number' && sizeBytes > 0) {
    allFields.push({ label: 'Size', value: formatBytes(sizeBytes) });
  }
  if (uploadedAt) {
    allFields.push({
      label: 'Uploaded',
      value: formatDate(uploadedAt),
    });
  }
  return (
    <div className="space-y-3">
      <div>
        <h4 className="font-semibold leading-tight">{title}</h4>
        {subtitle && (
          <p className="text-xs text-muted-foreground">{subtitle}</p>
        )}
      </div>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
        {allFields.map((f) => (
          <div key={f.label} className="contents">
            <dt className="font-medium text-muted-foreground">{f.label}</dt>
            <dd className="min-w-0 break-words">{f.value || '—'}</dd>
          </div>
        ))}
      </dl>
      {severity && (
        <div>
          <p className="mb-1 text-xs font-medium text-muted-foreground">
            Scan findings
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <SeverityStrip status={severity.status} counts={severity.counts} />
            {scanReportURL && severity.status === 'done' && (
              <Button
                variant="outline"
                size="sm"
                nativeButton={false}
                render={<Link to={scanReportURL} />}
              >
                <ShieldAlert className="mr-1.5 size-3.5" />
                View full scan report
              </Button>
            )}
          </div>
        </div>
      )}
      {downloadURL && (
        <div>
          <Button
            variant="outline"
            size="sm"
            nativeButton={false}
            render={<a href={downloadURL} />}
          >
            <Download className="mr-1.5 size-4" />
            {downloadLabel ?? 'Download'}
          </Button>
        </div>
      )}
    </div>
  );
}

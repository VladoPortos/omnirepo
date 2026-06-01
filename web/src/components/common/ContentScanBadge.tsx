/**
 * ContentScanBadge — renders the scan_severity field returned by
 * GET /projects/.../repos/.../content.
 *
 * The backend now surfaces the latest scan state as a single string:
 *   '' (no scan), 'scanning', 'failed', 'clean', 'low'..'critical', 'unknown'
 *
 * SeverityBadge only knows critical..low/unknown, so this shim handles
 * the three extra states ('' | scanning | failed | clean) and delegates
 * to SeverityBadge for the rest. Keeping this isolated avoids bloating
 * the generic SeverityBadge with content-table-specific logic.
 */

import { Loader2, ShieldCheck, ShieldX } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { SeverityBadge } from './SeverityBadge';

interface Props {
  severity: string;
}

export function ContentScanBadge({ severity }: Props) {
  if (!severity) {
    return <span className="text-xs text-muted-foreground">Not scanned</span>;
  }
  if (severity === 'scanning') {
    return (
      <Badge variant="outline">
        <Loader2 className="mr-1 size-3 animate-spin" />
        Scanning
      </Badge>
    );
  }
  if (severity === 'failed') {
    return (
      <Badge
        variant="outline"
        className="bg-destructive/10 text-destructive border-destructive/20"
      >
        <ShieldX className="mr-1 size-3" />
        Failed
      </Badge>
    );
  }
  if (severity === 'clean') {
    return (
      <Badge
        variant="outline"
        className="bg-teal-500/10 text-teal-600 border-teal-500/20 dark:text-teal-400"
      >
        <ShieldCheck className="mr-1 size-3" />
        Clean
      </Badge>
    );
  }
  return <SeverityBadge severity={severity} />;
}

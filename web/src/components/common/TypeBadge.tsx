/**
 * Badge with repo type icon + label. Uses lucide-react icons.
 */

import {
  Container,
  Package,
  Code,
  Ship,
  Boxes,
  Hexagon,
  FolderArchive,
  GitBranch,
  File,
  Database,
} from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import type { RepoType } from '@/api/types';

const typeConfig: Record<RepoType, { icon: typeof Container; label: string }> = {
  docker: { icon: Container, label: 'Docker' },
  rpm: { icon: Package, label: 'RPM' },
  deb: { icon: Package, label: 'APT' },
  pypi: { icon: Code, label: 'PyPI' },
  helm: { icon: Ship, label: 'Helm' },
  go: { icon: Boxes, label: 'Go' },
  npm: { icon: Hexagon, label: 'npm' },
  maven: { icon: FolderArchive, label: 'Maven' },
  git: { icon: GitBranch, label: 'Git' },
  raw: { icon: File, label: 'RAW' },
  s3: { icon: Database, label: 'S3' },
};

interface TypeBadgeProps {
  type: RepoType;
  className?: string;
}

export function TypeBadge({ type, className }: TypeBadgeProps) {
  const config = typeConfig[type];
  if (!config) return null;
  const Icon = config.icon;

  return (
    <Badge variant="secondary" className={cn('gap-1', className)}>
      <Icon className="size-3" data-icon="inline-start" />
      {config.label}
    </Badge>
  );
}

/**
 * Utility formatters for display values throughout the UI.
 */

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const;

/**
 * Format a byte count into a human-readable string.
 * e.g. 1536 -> "1.5 KB", 1073741824 -> "1.0 GB"
 */
export function formatBytes(n: number): string {
  if (n < 0) return '0 B';
  if (n === 0) return '0 B';
  const i = Math.min(
    Math.floor(Math.log(n) / Math.log(1024)),
    BYTE_UNITS.length - 1,
  );
  const value = n / Math.pow(1024, i);
  return `${value.toFixed(i === 0 ? 0 : 1)} ${BYTE_UNITS[i]}`;
}

const RELATIVE_THRESHOLDS = [
  { max: 60, divisor: 1, unit: 'second' },
  { max: 3600, divisor: 60, unit: 'minute' },
  { max: 86400, divisor: 3600, unit: 'hour' },
  { max: 604800, divisor: 86400, unit: 'day' },
  { max: 2592000, divisor: 604800, unit: 'week' },
  { max: 31536000, divisor: 2592000, unit: 'month' },
  { max: Infinity, divisor: 31536000, unit: 'year' },
] as const;

/**
 * Format an ISO 8601 date string into a relative ("3 hours ago") or
 * absolute ("Jan 15, 2026") string. Falls back to absolute for dates
 * older than 30 days.
 */
export function formatDate(iso: string): string {
  const date = new Date(iso);
  if (isNaN(date.getTime())) return iso;

  const diffSeconds = Math.floor((Date.now() - date.getTime()) / 1000);

  if (diffSeconds < 0) {
    // Future date -- show absolute
    return date.toLocaleDateString('en-US', {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    });
  }

  if (diffSeconds < 5) return 'just now';

  for (const { max, divisor, unit } of RELATIVE_THRESHOLDS) {
    if (diffSeconds < max) {
      const value = Math.floor(diffSeconds / divisor);
      if (value > 30 && unit === 'day') {
        // Beyond ~30 days, show absolute date
        return date.toLocaleDateString('en-US', {
          month: 'short',
          day: 'numeric',
          year: 'numeric',
        });
      }
      return `${value} ${unit}${value !== 1 ? 's' : ''} ago`;
    }
  }

  return date.toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
  });
}

/**
 * Capitalize severity string for display.
 */
export function formatSeverity(s: string): string {
  if (!s) return '';
  return s.charAt(0).toUpperCase() + s.slice(1).toLowerCase();
}

/**
 * Format a duration in milliseconds into a human-readable string.
 * e.g. 3661000 -> "1h 1m 1s"
 */
export function formatDuration(ms: number): string {
  if (ms < 0) ms = 0;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;

  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) {
    return remainingSeconds > 0 ? `${minutes}m ${remainingSeconds}s` : `${minutes}m`;
  }

  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  if (remainingMinutes > 0) {
    return `${hours}h ${remainingMinutes}m`;
  }
  return `${hours}h`;
}

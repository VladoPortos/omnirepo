/**
 * Split-pane diff viewer using react-diff-viewer-continued per D-39.
 * Used for commit details and branch comparison.
 */

import ReactDiffViewer, { DiffMethod } from 'react-diff-viewer-continued';

interface DiffViewerProps {
  oldValue: string;
  newValue: string;
  oldTitle?: string;
  newTitle?: string;
  splitView?: boolean;
}

export function DiffViewer({
  oldValue,
  newValue,
  oldTitle,
  newTitle,
  splitView = true,
}: DiffViewerProps) {
  const isDark = document.documentElement.classList.contains('dark');

  return (
    <ReactDiffViewer
      oldValue={oldValue}
      newValue={newValue}
      leftTitle={oldTitle}
      rightTitle={newTitle}
      splitView={splitView}
      compareMethod={DiffMethod.LINES}
      useDarkTheme={isDark}
    />
  );
}

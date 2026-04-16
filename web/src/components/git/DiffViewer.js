import { jsx as _jsx } from "react/jsx-runtime";
/**
 * Split-pane diff viewer using react-diff-viewer-continued per D-39.
 * Used for commit details and branch comparison.
 */
import ReactDiffViewer, { DiffMethod } from 'react-diff-viewer-continued';
export function DiffViewer({ oldValue, newValue, oldTitle, newTitle, splitView = true, }) {
    const isDark = document.documentElement.classList.contains('dark');
    return (_jsx(ReactDiffViewer, { oldValue: oldValue, newValue: newValue, leftTitle: oldTitle, rightTitle: newTitle, splitView: splitView, compareMethod: DiffMethod.LINES, useDarkTheme: isDark }));
}

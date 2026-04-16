import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Branch/tag selector dropdown for Git repo browser.
 * Groups refs into Branches and Tags sections.
 */
import { GitBranch, Tag } from 'lucide-react';
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue, } from '@/components/ui/select';
export function RefSelector({ refs, currentRef, onRefChange, loading, }) {
    const branches = refs.filter((r) => r.type === 'branch');
    const tags = refs.filter((r) => r.type === 'tag');
    return (_jsxs(Select, { value: currentRef, onValueChange: (val) => { if (val)
            onRefChange(val); }, disabled: loading, children: [_jsx(SelectTrigger, { className: "w-48", children: _jsx(SelectValue, { children: currentRef ? (_jsxs("span", { className: "flex items-center gap-1.5", children: [_jsx(GitBranch, { className: "size-3.5" }), currentRef] })) : ('Select ref...') }) }), _jsxs(SelectContent, { children: [branches.length > 0 && (_jsxs(SelectGroup, { children: [_jsx(SelectLabel, { children: _jsxs("span", { className: "flex items-center gap-1.5", children: [_jsx(GitBranch, { className: "size-3.5" }), "Branches"] }) }), branches.map((b) => (_jsx(SelectItem, { value: b.name, children: b.name }, `branch-${b.name}`)))] })), tags.length > 0 && (_jsxs(SelectGroup, { children: [_jsx(SelectLabel, { children: _jsxs("span", { className: "flex items-center gap-1.5", children: [_jsx(Tag, { className: "size-3.5" }), "Tags"] }) }), tags.map((t) => (_jsx(SelectItem, { value: t.name, children: t.name }, `tag-${t.name}`)))] })), refs.length === 0 && !loading && (_jsx("div", { className: "px-2 py-4 text-center text-sm text-muted-foreground", children: "No refs found" }))] })] }));
}

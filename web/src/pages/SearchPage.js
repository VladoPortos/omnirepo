import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Global search page per D-06, D-47.
 * Debounced text input + clickable filter chips for kind/severity/project.
 * Results fade-in staggered via framer-motion.
 */
import { useState, useMemo, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { motion, AnimatePresence } from 'framer-motion';
import { Search as SearchIcon } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue, } from '@/components/ui/select';
import { FilterChips } from '@/components/common/FilterChips';
import { TypeBadge } from '@/components/common/TypeBadge';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { useSearch, useProjects } from '@/api/queries';
const KIND_OPTIONS = [
    { label: 'Repos', value: 'repo' },
    { label: 'Artifacts', value: 'artifact' },
    { label: 'CVEs', value: 'cve' },
];
const SEVERITY_OPTIONS = [
    { label: 'Critical', value: 'critical' },
    { label: 'High', value: 'high' },
    { label: 'Medium', value: 'medium' },
    { label: 'Low', value: 'low' },
];
function useDebounce(value, delay) {
    const [debounced, setDebounced] = useState(value);
    useMemo(() => {
        const timer = setTimeout(() => setDebounced(value), delay);
        return () => clearTimeout(timer);
    }, [value, delay]);
    return debounced;
}
const resultVariants = {
    hidden: { opacity: 0, y: 8 },
    visible: (i) => ({
        opacity: 1,
        y: 0,
        transition: { delay: i * 0.04, duration: 0.2, ease: 'easeOut' },
    }),
    exit: { opacity: 0, y: -4, transition: { duration: 0.1 } },
};
function resultRoute(result) {
    // location format is "project/repo" or "project"
    const parts = result.location.split('/');
    if (result.kind === 'repo' && parts.length >= 2) {
        return `/projects/${parts[0]}`;
    }
    if (parts.length >= 1) {
        return `/projects/${parts[0]}`;
    }
    return '/search';
}
export function SearchPage() {
    const navigate = useNavigate();
    const [query, setQuery] = useState('');
    const [kindFilters, setKindFilters] = useState([]);
    const [severityFilters, setSeverityFilters] = useState([]);
    const [projectFilter, setProjectFilter] = useState('');
    const debouncedQuery = useDebounce(query, 300);
    // Derive single filter values from multi-select (API takes single values)
    const kindParam = kindFilters.length === 1 ? kindFilters[0] : undefined;
    const severityParam = severityFilters.length === 1 ? severityFilters[0] : undefined;
    const { data, isLoading, isFetching } = useSearch(debouncedQuery, kindParam, severityParam, projectFilter || undefined);
    const { data: projectsData } = useProjects();
    const projects = projectsData?.items ?? [];
    // Client-side filter when multiple kind/severity chips selected
    const filteredResults = useMemo(() => {
        if (!data?.items)
            return [];
        let results = data.items;
        if (kindFilters.length > 1) {
            results = results.filter((r) => kindFilters.includes(r.kind));
        }
        if (severityFilters.length > 1) {
            results = results.filter((r) => severityFilters.includes(r.severity?.toLowerCase()));
        }
        return results;
    }, [data?.items, kindFilters, severityFilters]);
    const handleResultClick = useCallback((result) => {
        navigate(resultRoute(result));
    }, [navigate]);
    const showLoading = isLoading || isFetching;
    const hasQuery = debouncedQuery.length > 0;
    const hasResults = filteredResults.length > 0;
    return (_jsxs("div", { className: "space-y-6", children: [_jsx("h1", { className: "text-[28px] font-semibold leading-tight", children: "Search" }), _jsxs("div", { className: "relative max-w-2xl", children: [_jsx(SearchIcon, { className: "absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" }), _jsx(Input, { value: query, onChange: (e) => setQuery(e.target.value), placeholder: "Search repositories, artifacts, and CVEs...", className: "pl-9 h-10", autoFocus: true })] }), _jsxs("div", { className: "flex flex-wrap items-center gap-4", children: [_jsxs("div", { className: "space-y-1", children: [_jsx("span", { className: "text-xs text-muted-foreground font-medium", children: "Kind" }), _jsx(FilterChips, { options: KIND_OPTIONS, selected: kindFilters, onChange: setKindFilters })] }), _jsxs("div", { className: "space-y-1", children: [_jsx("span", { className: "text-xs text-muted-foreground font-medium", children: "Severity" }), _jsx(FilterChips, { options: SEVERITY_OPTIONS, selected: severityFilters, onChange: setSeverityFilters })] }), _jsxs("div", { className: "space-y-1", children: [_jsx("span", { className: "text-xs text-muted-foreground font-medium", children: "Project" }), _jsxs(Select, { value: projectFilter, onValueChange: (val) => setProjectFilter(!val || val === '__all__' ? '' : val), children: [_jsx(SelectTrigger, { size: "sm", className: "w-[180px]", children: _jsx(SelectValue, { placeholder: "All projects" }) }), _jsxs(SelectContent, { children: [_jsx(SelectItem, { value: "__all__", children: "All projects" }), projects.map((p) => (_jsx(SelectItem, { value: p.name, children: p.name }, p.id)))] })] })] })] }), _jsxs("div", { className: "max-w-3xl space-y-2", children: [showLoading && hasQuery && (_jsx("div", { className: "space-y-2", children: Array.from({ length: 5 }).map((_, i) => (_jsxs("div", { className: "rounded-lg border p-4 space-y-2", children: [_jsx(Skeleton, { className: "h-4 w-48" }), _jsx(Skeleton, { className: "h-3 w-64" })] }, i))) })), !showLoading && hasQuery && !hasResults && (_jsxs("div", { className: "rounded-lg border p-8 text-center", children: [_jsx("h2", { className: "text-lg font-semibold", children: "No results found" }), _jsx("p", { className: "text-muted-foreground mt-1", children: "Try a different search term or adjust your filters." })] })), !hasQuery && (_jsxs("div", { className: "rounded-lg border p-8 text-center text-muted-foreground", children: [_jsx(SearchIcon, { className: "mx-auto mb-3 size-8 opacity-50" }), _jsx("p", { children: "Start typing to search across repositories, artifacts, and CVEs." })] })), _jsx(AnimatePresence, { mode: "popLayout", children: !showLoading &&
                            filteredResults.map((result, i) => (_jsx(motion.div, { custom: i, variants: resultVariants, initial: "hidden", animate: "visible", exit: "exit", layout: true, children: _jsx("button", { type: "button", className: "w-full text-left rounded-lg border p-4 hover:bg-muted/50 transition-colors focus-visible:ring-2 focus-visible:ring-ring", onClick: () => handleResultClick(result), children: _jsxs("div", { className: "flex items-start gap-3", children: [_jsx("div", { className: "mt-0.5", children: result.kind === 'repo' ? (_jsx(TypeBadge, { type: result.name.split('/').pop() || 'raw' })) : result.kind === 'cve' && result.severity ? (_jsx(SeverityBadge, { severity: result.severity })) : (_jsx("span", { className: "inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-xs font-medium", children: result.kind })) }), _jsxs("div", { className: "flex-1 min-w-0", children: [_jsx("p", { className: "font-semibold text-sm truncate", children: result.name }), _jsx("p", { className: "text-xs text-muted-foreground truncate", children: result.location })] }), result.score > 0 && (_jsx("span", { className: "text-xs text-muted-foreground shrink-0", children: result.score.toFixed(1) }))] }) }) }, `${result.kind}-${result.entity_id}`))) }), data?.next_cursor && !showLoading && (_jsx("div", { className: "flex justify-center pt-2", children: _jsx(Button, { variant: "outline", size: "sm", children: "Load more" }) }))] })] }));
}

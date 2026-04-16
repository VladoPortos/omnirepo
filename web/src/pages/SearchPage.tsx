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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { FilterChips } from '@/components/common/FilterChips';
import { TypeBadge } from '@/components/common/TypeBadge';
import { SeverityBadge } from '@/components/common/SeverityBadge';
import { useSearch, useProjects } from '@/api/queries';
import type { SearchResult, RepoType } from '@/api/types';

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

function useDebounce(value: string, delay: number): string {
  const [debounced, setDebounced] = useState(value);

  useMemo(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);

  return debounced;
}

const resultVariants = {
  hidden: { opacity: 0, y: 8 },
  visible: (i: number) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.04, duration: 0.2, ease: 'easeOut' as const },
  }),
  exit: { opacity: 0, y: -4, transition: { duration: 0.1 } },
};

function resultRoute(result: SearchResult): string {
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
  const [kindFilters, setKindFilters] = useState<string[]>([]);
  const [severityFilters, setSeverityFilters] = useState<string[]>([]);
  const [projectFilter, setProjectFilter] = useState<string>('');

  const debouncedQuery = useDebounce(query, 300);

  // Derive single filter values from multi-select (API takes single values)
  const kindParam = kindFilters.length === 1 ? kindFilters[0] : undefined;
  const severityParam = severityFilters.length === 1 ? severityFilters[0] : undefined;

  const { data, isLoading, isFetching } = useSearch(
    debouncedQuery,
    kindParam,
    severityParam,
    projectFilter || undefined,
  );

  const { data: projectsData } = useProjects();
  const projects = projectsData?.items ?? [];

  // Client-side filter when multiple kind/severity chips selected
  const filteredResults = useMemo(() => {
    if (!data?.items) return [];
    let results = data.items;
    if (kindFilters.length > 1) {
      results = results.filter((r) => kindFilters.includes(r.kind));
    }
    if (severityFilters.length > 1) {
      results = results.filter((r) =>
        severityFilters.includes(r.severity?.toLowerCase()),
      );
    }
    return results;
  }, [data?.items, kindFilters, severityFilters]);

  const handleResultClick = useCallback(
    (result: SearchResult) => {
      navigate(resultRoute(result));
    },
    [navigate],
  );

  const showLoading = isLoading || isFetching;
  const hasQuery = debouncedQuery.length > 0;
  const hasResults = filteredResults.length > 0;

  return (
    <div className="space-y-6">
      {/* Header */}
      <h1 className="text-[28px] font-semibold leading-tight">Search</h1>

      {/* Search input */}
      <div className="relative max-w-2xl">
        <SearchIcon className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search repositories, artifacts, and CVEs..."
          className="pl-9 h-10"
          autoFocus
        />
      </div>

      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-4">
        <div className="space-y-1">
          <span className="text-xs text-muted-foreground font-medium">Kind</span>
          <FilterChips
            options={KIND_OPTIONS}
            selected={kindFilters}
            onChange={setKindFilters}
          />
        </div>

        <div className="space-y-1">
          <span className="text-xs text-muted-foreground font-medium">Severity</span>
          <FilterChips
            options={SEVERITY_OPTIONS}
            selected={severityFilters}
            onChange={setSeverityFilters}
          />
        </div>

        <div className="space-y-1">
          <span className="text-xs text-muted-foreground font-medium">Project</span>
          <Select
            value={projectFilter}
            onValueChange={(val) => setProjectFilter(!val || val === '__all__' ? '' : val)}
          >
            <SelectTrigger size="sm" className="w-[180px]">
              <SelectValue placeholder="All projects" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all__">All projects</SelectItem>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.name}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {/* Results */}
      <div className="max-w-3xl space-y-2">
        {showLoading && hasQuery && (
          <div className="space-y-2">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="rounded-lg border p-4 space-y-2">
                <Skeleton className="h-4 w-48" />
                <Skeleton className="h-3 w-64" />
              </div>
            ))}
          </div>
        )}

        {!showLoading && hasQuery && !hasResults && (
          <div className="rounded-lg border p-8 text-center">
            <h2 className="text-lg font-semibold">No results found</h2>
            <p className="text-muted-foreground mt-1">
              Try a different search term or adjust your filters.
            </p>
          </div>
        )}

        {!hasQuery && (
          <div className="rounded-lg border p-8 text-center text-muted-foreground">
            <SearchIcon className="mx-auto mb-3 size-8 opacity-50" />
            <p>Start typing to search across repositories, artifacts, and CVEs.</p>
          </div>
        )}

        <AnimatePresence mode="popLayout">
          {!showLoading &&
            filteredResults.map((result, i) => (
              <motion.div
                key={`${result.kind}-${result.entity_id}`}
                custom={i}
                variants={resultVariants}
                initial="hidden"
                animate="visible"
                exit="exit"
                layout
              >
                <button
                  type="button"
                  className="w-full text-left rounded-lg border p-4 hover:bg-muted/50 transition-colors focus-visible:ring-2 focus-visible:ring-ring"
                  onClick={() => handleResultClick(result)}
                >
                  <div className="flex items-start gap-3">
                    <div className="mt-0.5">
                      {result.kind === 'repo' ? (
                        <TypeBadge type={result.name.split('/').pop() as RepoType || 'raw'} />
                      ) : result.kind === 'cve' && result.severity ? (
                        <SeverityBadge severity={result.severity} />
                      ) : (
                        <span className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-xs font-medium">
                          {result.kind}
                        </span>
                      )}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="font-semibold text-sm truncate">{result.name}</p>
                      <p className="text-xs text-muted-foreground truncate">
                        {result.location}
                      </p>
                    </div>
                    {result.score > 0 && (
                      <span className="text-xs text-muted-foreground shrink-0">
                        {result.score.toFixed(1)}
                      </span>
                    )}
                  </div>
                </button>
              </motion.div>
            ))}
        </AnimatePresence>

        {/* Load more */}
        {data?.next_cursor && !showLoading && (
          <div className="flex justify-center pt-2">
            <Button variant="outline" size="sm">
              Load more
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

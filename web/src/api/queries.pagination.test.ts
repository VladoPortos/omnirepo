import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useProjects, useRepoContent, useRepoScans, useScanVulnerabilities } from './queries';
import { api } from './client';

vi.mock('@tanstack/react-query', async (original) => ({
  ...await original<typeof import('@tanstack/react-query')>(),
  useQuery: (options: unknown) => options,
}));
vi.mock('./client', async (original) => ({
  ...await original<typeof import('./client')>(),
  api: { get: vi.fn() },
}));

async function fetchQuery(result: unknown): Promise<unknown> {
  return (result as { queryFn: () => Promise<unknown> }).queryFn();
}

describe('complete API pagination', () => {
  beforeEach(() => vi.mocked(api.get).mockReset());

  it('loads every project for the project list and selectors', async () => {
    const projects = Array.from({ length: 51 }, (_, id) => ({ id }));
    vi.mocked(api.get).mockImplementation(async (_path, params) => {
      const offset = Number(params?.cursor ?? 0);
      return { items: projects.slice(offset, offset + 50), next_cursor: offset === 0 ? '50' : null };
    });
    expect(await fetchQuery(useProjects())).toEqual({ items: projects, next_cursor: null });
  });

  it('keeps RAW files beyond the first page available to folder and file views', async () => {
    const rows = Array.from({ length: 101 }, (_, id) => ({ name: `folder/${id}` }));
    vi.mocked(api.get).mockImplementation(async (_path, params) => {
      const offset = Number(params?.offset ?? 0);
      return { items: rows.slice(offset, offset + 100), total: rows.length, next_offset: offset === 0 ? 100 : null };
    });
    expect(await fetchQuery(useRepoContent('project', 'raw', 'files'))).toMatchObject({ items: rows, total: 101 });
  });

  it('does not share the complete RAW tree cache with a bounded page', () => {
    const all = useRepoContent('project', 'raw', 'files') as unknown as { queryKey: unknown[] };
    const page = useRepoContent('project', 'raw', 'files', { limit: 100 }) as unknown as { queryKey: unknown[] };
    expect(all.queryKey).not.toEqual(page.queryKey);
  });

  it('fails a report when a later page fails rather than returning partial findings', async () => {
    vi.mocked(api.get).mockResolvedValueOnce(Array.from({ length: 1000 }, (_, id) => ({ id })));
    vi.mocked(api.get).mockRejectedValueOnce(new Error('page unavailable'));
    await expect(fetchQuery(useScanVulnerabilities(42))).rejects.toThrow('page unavailable');
  });

  it('continues scan history beyond the server maximum page size', async () => {
    const rows = Array.from({ length: 601 }, (_, id) => ({ id }));
    vi.mocked(api.get).mockImplementation(async (_path, params) => {
      const offset = Number(params?.offset ?? 0);
      return rows.slice(offset, offset + Math.min(Number(params?.limit ?? 100), 500));
    });
    expect(await fetchQuery(useRepoScans('project', 'raw', 'files', { limit: 600 }))).toEqual(rows.slice(0, 600));
  });

  it('loads the full vulnerability report, including rows past 1000', async () => {
    const rows = Array.from({ length: 1001 }, (_, id) => ({ id }));
    vi.mocked(api.get).mockImplementation(async (_path, params) => {
      const offset = Number(params?.offset ?? 0);
      return rows.slice(offset, offset + 1000);
    });
    expect(await fetchQuery(useScanVulnerabilities(42))).toEqual(rows);
    expect(api.get).toHaveBeenLastCalledWith('/scans/42/vulnerabilities', { limit: '1000', offset: '1000' });
  });
});

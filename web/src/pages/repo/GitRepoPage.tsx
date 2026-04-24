/**
 * Git repository detail page per D-11.
 * File tree browser, syntax-highlighted file viewer, commit log,
 * refs, blame, diff, and branch comparison.
 */

import { useState, useCallback, useEffect, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { GitBranch, Terminal } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { CopyButton } from '@/components/common/CopyButton';
import { EmptyState } from '@/components/common/EmptyState';
import { SnippetList } from '@/components/common/SnippetList';
import { StatusBadge } from '@/components/common/StatusBadge';
import { SyncNowButton } from '@/components/SyncNowButton';
import { RepoPageLayout } from './RepoPageLayout';
import { RefSelector } from '@/components/git/RefSelector';
import { FileTree } from '@/components/git/FileTree';
import { FileViewer } from '@/components/git/FileViewer';
import { BlameViewer } from '@/components/git/BlameViewer';
import { CommitLog } from '@/components/git/CommitLog';
import { CommitDetail } from '@/components/git/CommitDetail';
import { BranchCompare } from '@/components/git/BranchCompare';
import { useGitRefs, useGitTree, useGitBlob } from '@/api/queries';
import { useRoleFor } from '@/hooks/useAuth';
import type { Repo, GitTreeEntry } from '@/api/types';

interface GitRepoPageProps {
  repo: Repo;
}

export function GitRepoPage({ repo }: GitRepoPageProps) {
  const { name: projectName } = useParams<{ name: string }>();
  const hostname = window.location.host;

  const [currentRef, setCurrentRef] = useState('');
  const [currentPath, setCurrentPath] = useState('');
  const [viewingFile, setViewingFile] = useState<string | null>(null);
  const [tab, setTab] = useState('files');
  const [showBlame, setShowBlame] = useState(false);
  const [viewingCommit, setViewingCommit] = useState<string | null>(null);

  // Fetch refs
  // RBAC-06: role-aware upload permission gate.
  const myRole = useRoleFor(projectName ?? '');
  const isMaintainer = myRole === 'maintainer';
  const canUpload = isMaintainer;

  const { data: refsData, isLoading: refsLoading } = useGitRefs(
    projectName!,
    repo.name,
  );
  const refs = useMemo(() => refsData?.items ?? [], [refsData?.items]);

  // Default to first branch (HEAD-like) or first ref
  useEffect(() => {
    if (refs.length > 0 && !currentRef) {
      const main = refs.find(
        (r) => r.type === 'branch' && (r.name === 'main' || r.name === 'master'),
      );
      setCurrentRef(main?.name ?? refs[0].name);
    }
  }, [refs, currentRef]);

  // Fetch tree for current path
  const { data: treeData, isLoading: treeLoading } = useGitTree(
    projectName!,
    repo.name,
    currentRef,
    currentPath,
  );
  const treeEntries = treeData?.items ?? [];

  // Fetch file content when viewing a file
  const { data: fileData, isLoading: fileLoading } = useGitBlob(
    projectName!,
    repo.name,
    currentRef,
    viewingFile ?? '',
  );

  const handleNavigate = useCallback((entry: GitTreeEntry) => {
    if (entry.type === 'tree') {
      setCurrentPath(entry.path);
      setViewingFile(null);
      setShowBlame(false);
    } else {
      setViewingFile(entry.path);
      setShowBlame(false);
    }
  }, []);

  const handleBack = useCallback(() => {
    if (showBlame) {
      setShowBlame(false);
      return;
    }
    if (viewingFile) {
      setViewingFile(null);
      setShowBlame(false);
      return;
    }
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    setCurrentPath(parts.join('/'));
  }, [showBlame, viewingFile, currentPath]);

  const handleRefChange = useCallback((ref: string) => {
    setCurrentRef(ref);
    setCurrentPath('');
    setViewingFile(null);
    setShowBlame(false);
    setViewingCommit(null);
  }, []);

  const cloneUrl = `${window.location.protocol}//${hostname}/${projectName}/git/${repo.name}.git`;

  // EMPTY-03 for empty git repos: no refs = no commits pushed yet.
  // When zero refs, show an EmptyState with the git push snippet inline
  // instead of the empty Files/Commits/Refs tabs dance.
  //
  // Plan 11-10 / GITMIRROR-03 (D-09): mirror repos get a different empty
  // state — "Mirror is empty" + SyncNowButton — because the push snippet
  // is misleading (mirror repos refuse receive-pack with 403
  // mirror.push_rejected per plan 11-07). Refs appear after the first
  // successful sync.
  if (!refsLoading && refs.length === 0) {
    return (
      <RepoPageLayout repo={repo}>
        <div className="space-y-4">
          <div className="flex items-center gap-2 rounded-md border bg-muted/30 px-3 py-1.5">
            <GitBranch className="size-4 text-muted-foreground" />
            <code className="text-xs">{cloneUrl}</code>
            <CopyButton text={cloneUrl} />
            {repo.is_mirror && (
              <StatusBadge
                status="warning"
                label="Read-only mirror"
                size="sm"
              />
            )}
          </div>
          {repo.is_mirror ? (
            <div className="space-y-4">
              <SyncNowButton
                projectName={projectName ?? ''}
                repoType="git"
                repoName={repo.name}
                upstreamUrl={repo.mirror_upstream_url}
                // D-07: no filter widget for git mirrors (all-refs mode).
                filterSummary={undefined}
              />
              <EmptyState
                icon={Terminal}
                title="Mirror is empty"
                description="Run a sync to populate this mirror from its upstream."
              />
            </div>
          ) : canUpload ? (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Upload your first artifact using the snippet below."
            >
              <SnippetList
                repoType="git"
                projectName={projectName ?? ''}
                repoName={repo.name}
                hostname={hostname}
                className="w-full max-w-2xl"
                // Plan 11-10 / D-09: hide push instructions on mirror
                // repos. This branch is only reached when !repo.is_mirror,
                // but passing the prop explicitly documents the contract
                // (SnippetList is a reusable primitive and other callers
                // may pipe a mirror-state value through).
                hidePush={repo.is_mirror}
              />
            </EmptyState>
          ) : (
            <EmptyState
              icon={Terminal}
              title="No artifacts yet"
              description="Ask a maintainer to upload an artifact."
            />
          )}
        </div>
      </RepoPageLayout>
    );
  }

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
        {/* Plan 11-10 / GITMIRROR-03 (D-09): mirror Sync Now affordance,
            parallel to HelmRepoPage / AptRepoPage / RpmRepoPage / PypiRepoPage.
            Renders only when is_mirror is true so dev repos stay unchanged. */}
        {repo.is_mirror && (
          <SyncNowButton
            projectName={projectName ?? ''}
            repoType="git"
            repoName={repo.name}
            upstreamUrl={repo.mirror_upstream_url}
            // D-07: no filter UI for git mirrors (all-refs mirror mode).
            filterSummary={undefined}
          />
        )}

        {/* Top bar: ref selector + clone URL */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <RefSelector
            refs={refs}
            currentRef={currentRef}
            onRefChange={handleRefChange}
            loading={refsLoading}
          />
          <div className="flex items-center gap-2 rounded-md border bg-muted/30 px-3 py-1.5">
            <GitBranch className="size-4 text-muted-foreground" />
            <code className="text-xs">{cloneUrl}</code>
            <CopyButton text={cloneUrl} />
            {repo.is_mirror && (
              <StatusBadge
                status="warning"
                label="Read-only mirror"
                size="sm"
              />
            )}
          </div>
        </div>

        {/* Tabs: Files, Commits, Refs, Compare */}
        <Tabs defaultValue="files" value={tab} onValueChange={(v) => { setTab(v); setViewingCommit(null); }}>
          <TabsList>
            <TabsTrigger value="files">Files</TabsTrigger>
            <TabsTrigger value="commits">Commits</TabsTrigger>
            <TabsTrigger value="refs">Refs</TabsTrigger>
            <TabsTrigger value="compare">Compare</TabsTrigger>
          </TabsList>

          <TabsContent value="files">
            {viewingFile ? (
              showBlame ? (
                <BlameViewer
                  projectName={projectName!}
                  repoName={repo.name}
                  currentRef={currentRef}
                  filePath={viewingFile}
                  onBack={handleBack}
                />
              ) : (
                <FileViewer
                  file={fileData}
                  loading={fileLoading}
                  onBack={handleBack}
                  onBlame={() => setShowBlame(true)}
                  downloadUrl={`/api/v1/projects/${projectName}/repos/${repo.name}/git/blob/${currentRef}/${viewingFile}?raw=1`}
                />
              )
            ) : (
              <FileTree
                entries={treeEntries}
                loading={treeLoading}
                currentPath={currentPath}
                onNavigate={handleNavigate}
                onBack={currentPath ? handleBack : undefined}
              />
            )}
          </TabsContent>

          <TabsContent value="commits">
            {viewingCommit ? (
              <CommitDetail
                projectName={projectName!}
                repoName={repo.name}
                sha={viewingCommit}
                onBack={() => setViewingCommit(null)}
              />
            ) : (
              <CommitLog
                projectName={projectName!}
                repoName={repo.name}
                currentRef={currentRef}
                onCommitClick={setViewingCommit}
              />
            )}
          </TabsContent>

          <TabsContent value="refs">
            <div className="space-y-4 py-4">
              {refs.filter((r) => r.type === 'branch').length > 0 && (
                <div>
                  <h3 className="mb-2 text-sm font-semibold">Branches</h3>
                  <div className="space-y-1">
                    {refs
                      .filter((r) => r.type === 'branch')
                      .map((r) => (
                        <div
                          key={r.name}
                          className="flex items-center justify-between rounded-md border px-3 py-2"
                        >
                          <Button
                            variant="link"
                            size="sm"
                            className="h-auto p-0"
                            onClick={() => { handleRefChange(r.name); setTab('files'); }}
                          >
                            {r.name}
                          </Button>
                          <code className="text-xs text-muted-foreground">
                            {r.sha.slice(0, 8)}
                          </code>
                        </div>
                      ))}
                  </div>
                </div>
              )}
              {refs.filter((r) => r.type === 'tag').length > 0 && (
                <div>
                  <h3 className="mb-2 text-sm font-semibold">Tags</h3>
                  <div className="space-y-1">
                    {refs
                      .filter((r) => r.type === 'tag')
                      .map((r) => (
                        <div
                          key={r.name}
                          className="flex items-center justify-between rounded-md border px-3 py-2"
                        >
                          <Button
                            variant="link"
                            size="sm"
                            className="h-auto p-0"
                            onClick={() => { handleRefChange(r.name); setTab('files'); }}
                          >
                            {r.name}
                          </Button>
                          <code className="text-xs text-muted-foreground">
                            {r.sha.slice(0, 8)}
                          </code>
                        </div>
                      ))}
                  </div>
                </div>
              )}
              {refs.length === 0 && (
                <p className="text-center text-sm text-muted-foreground">
                  No refs found. Push some code to get started.
                </p>
              )}
            </div>
          </TabsContent>

          <TabsContent value="compare">
            <BranchCompare
              projectName={projectName!}
              repoName={repo.name}
              refs={refs}
              defaultBase={refs.find((r) => r.type === 'branch' && r.name === 'main')?.name}
              defaultHead={currentRef !== 'main' ? currentRef : ''}
            />
          </TabsContent>
        </Tabs>
      </div>
    </RepoPageLayout>
  );
}

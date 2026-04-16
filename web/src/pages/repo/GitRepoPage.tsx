/**
 * Git repository detail page per D-11.
 * File tree browser, syntax-highlighted file viewer, commit log,
 * refs, blame, diff, and branch comparison.
 */

import { useState, useCallback, useEffect } from 'react';
import { useParams } from 'react-router-dom';
import { GitBranch } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { CopyButton } from '@/components/common/CopyButton';
import { RepoPageLayout } from './RepoPageLayout';
import { RefSelector } from '@/components/git/RefSelector';
import { FileTree } from '@/components/git/FileTree';
import { FileViewer } from '@/components/git/FileViewer';
import { BlameViewer } from '@/components/git/BlameViewer';
import { CommitLog } from '@/components/git/CommitLog';
import { CommitDetail } from '@/components/git/CommitDetail';
import { BranchCompare } from '@/components/git/BranchCompare';
import { useGitRefs, useGitTree, useGitBlob } from '@/api/queries';
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
  const { data: refsData, isLoading: refsLoading } = useGitRefs(
    projectName!,
    repo.name,
  );
  const refs = refsData?.items ?? [];

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

  const cloneUrl = `${window.location.protocol}//${hostname}/${projectName}/${repo.name}.git`;

  return (
    <RepoPageLayout repo={repo}>
      <div className="space-y-4">
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

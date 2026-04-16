/**
 * Git repository detail page per D-11.
 * File tree browser, syntax-highlighted file viewer, commit log,
 * refs, blame, diff, and branch comparison.
 */

import { useState, useCallback, useEffect } from 'react';
import { useParams } from 'react-router-dom';
import { GitBranch } from 'lucide-react';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { CopyButton } from '@/components/common/CopyButton';
import { RepoPageLayout } from './RepoPageLayout';
import { RefSelector } from '@/components/git/RefSelector';
import { FileTree } from '@/components/git/FileTree';
import { FileViewer } from '@/components/git/FileViewer';
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
    if (viewingFile) {
      setViewingFile(null);
      setShowBlame(false);
      return;
    }
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    setCurrentPath(parts.join('/'));
  }, [viewingFile, currentPath]);

  const handleRefChange = useCallback((ref: string) => {
    setCurrentRef(ref);
    setCurrentPath('');
    setViewingFile(null);
    setShowBlame(false);
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

        {/* Tabs: Files, Commits, Refs */}
        <Tabs defaultValue="files" value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="files">Files</TabsTrigger>
            <TabsTrigger value="commits">Commits</TabsTrigger>
            <TabsTrigger value="refs">Refs</TabsTrigger>
          </TabsList>

          <TabsContent value="files">
            {viewingFile ? (
              showBlame ? (
                // BlameViewer will be wired in Task 2
                <div className="py-8 text-center text-sm text-muted-foreground">
                  Blame view loading...
                </div>
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
            {/* CommitLog will be wired in Task 2 */}
            <div className="py-8 text-center text-sm text-muted-foreground">
              Commit log loading...
            </div>
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
                          <span className="text-sm">{r.name}</span>
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
                          <span className="text-sm">{r.name}</span>
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
        </Tabs>
      </div>
    </RepoPageLayout>
  );
}

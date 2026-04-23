/**
 * Admin TLS Certificates page (D-23).
 * Current cert info, upload form, history table.
 */

import { useState, useCallback, useRef } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { TLSCertInfo, TLSHistoryEntry } from '@/api/types';
import { DataTable, type ColumnDef } from '@/components/common/DataTable';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import { toast } from 'sonner';
import { formatDate } from '@/lib/format';
import { ShieldCheck, Upload, Clock, Fingerprint } from 'lucide-react';
import { EmptyState } from '@/components/common/EmptyState';

// ---------- Hooks ----------

function useTLSCurrent() {
  return useQuery({
    queryKey: ['admin', 'tls', 'current'],
    queryFn: () => api.get<TLSCertInfo>('/admin/tls/current'),
    staleTime: 30_000,
  });
}

function useTLSHistory() {
  return useQuery({
    queryKey: ['admin', 'tls', 'history'],
    queryFn: () => api.get<{ items: TLSHistoryEntry[] }>('/admin/tls/history'),
    staleTime: 30_000,
  });
}

function useTLSUpload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { cert: string; key: string }) => {
      const fd = new FormData();
      fd.append(
        'cert',
        new Blob([body.cert], { type: 'application/x-pem-file' }),
        'cert.pem',
      );
      fd.append(
        'key',
        new Blob([body.key], { type: 'application/x-pem-file' }),
        'key.pem',
      );
      return api.postForm<void>('/admin/tls/upload', fd);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'tls'] });
      toast.success('Certificate uploaded and hot-swapped successfully.');
    },
  });
}

// ---------- Component ----------

export default function TLSPage() {
  const { data: currentCert, isLoading: loadingCert } = useTLSCurrent();
  const { data: historyData, isLoading: loadingHistory } = useTLSHistory();
  const uploadMutation = useTLSUpload();

  const [certPem, setCertPem] = useState('');
  const [keyPem, setKeyPem] = useState('');
  const certFileRef = useRef<HTMLInputElement>(null);
  const keyFileRef = useRef<HTMLInputElement>(null);

  const handleFileRead = useCallback(
    (setter: (v: string) => void) => (e: React.ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0];
      if (!file) return;
      const reader = new FileReader();
      reader.onload = () => {
        setter(reader.result as string);
      };
      reader.readAsText(file);
      e.target.value = '';
    },
    [],
  );

  const handleUpload = useCallback(async () => {
    if (!certPem || !keyPem) {
      toast.error('Both certificate and key PEM are required.');
      return;
    }
    try {
      await uploadMutation.mutateAsync({ cert: certPem, key: keyPem });
      setCertPem('');
      setKeyPem('');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to upload certificate');
    }
  }, [certPem, keyPem, uploadMutation]);

  const isExpiringSoon = currentCert
    ? new Date(currentCert.not_after).getTime() - Date.now() < 30 * 86400 * 1000
    : false;

  const historyColumns: ColumnDef<TLSHistoryEntry>[] = [
    {
      id: 'uploaded_at',
      name: 'Uploaded',
      sortable: true,
      render: (row) => (
        <span className="text-sm">{formatDate(row.uploaded_at)}</span>
      ),
    },
    {
      id: 'uploaded_by',
      name: 'Uploaded By',
      accessor: (row) => row.uploaded_by,
    },
    {
      id: 'subject',
      name: 'Subject',
      accessor: (row) => row.subject,
    },
    {
      id: 'fingerprint',
      name: 'Fingerprint',
      render: (row) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.fingerprint_sha256.slice(0, 16)}...
        </span>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">TLS Certificates</h1>
        <p className="text-sm text-muted-foreground">
          Manage HTTPS certificates for this OmniRepo instance.
        </p>
      </div>

      {/* Current Certificate Card */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="size-5" />
            Current Certificate
          </CardTitle>
          <CardDescription>Active TLS certificate details</CardDescription>
        </CardHeader>
        <CardContent>
          {loadingCert ? (
            <div className="h-24 animate-pulse rounded-md bg-muted" />
          ) : currentCert && currentCert.source === 'uploaded' ? (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <span className="text-xs text-muted-foreground">Subject</span>
                <p className="font-medium">{currentCert.subject}</p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Issuer</span>
                <p className="font-medium">{currentCert.issuer}</p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Not Before</span>
                <p className="flex items-center gap-2 font-medium">
                  <Clock className="size-3.5" />
                  {formatDate(currentCert.not_before)}
                </p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Not After</span>
                <p className="flex items-center gap-2 font-medium">
                  <Clock className="size-3.5" />
                  {formatDate(currentCert.not_after)}
                  {isExpiringSoon && (
                    <Badge variant="destructive" className="text-xs">
                      Expiring soon
                    </Badge>
                  )}
                </p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Source</span>
                <p>
                  <Badge variant={currentCert.source === 'uploaded' ? 'default' : 'secondary'}>
                    {currentCert.source}
                  </Badge>
                </p>
              </div>
              {currentCert.dns_names && currentCert.dns_names.length > 0 && (
                <div className="col-span-full">
                  <span className="text-xs text-muted-foreground">
                    Subject Alternative Names
                  </span>
                  <p className="flex flex-wrap gap-1.5 mt-1">
                    {currentCert.dns_names.map((n) => (
                      <Badge key={n} variant="outline" className="font-mono text-xs">
                        {n}
                      </Badge>
                    ))}
                  </p>
                </div>
              )}
              <div className="col-span-full">
                <span className="text-xs text-muted-foreground flex items-center gap-1">
                  <Fingerprint className="size-3" />
                  SHA-256 Fingerprint
                </span>
                <p className="font-mono text-xs break-all mt-1">
                  {currentCert.fingerprint_sha256}
                </p>
              </div>
            </div>
          ) : (
            <EmptyState
              icon={ShieldCheck}
              title="Using the default self-signed certificate"
              description="Upload a certificate and private key (PEM) to replace the self-signed default."
              primaryCTA={{
                label: 'Upload certificate',
                onClick: () =>
                  document
                    .getElementById('tls-upload')
                    ?.scrollIntoView({ behavior: 'smooth', block: 'start' }),
              }}
            />
          )}
        </CardContent>
      </Card>

      {/* Upload Form */}
      <Card id="tls-upload">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Upload className="size-5" />
            Upload Certificate
          </CardTitle>
          <CardDescription>
            Upload a new TLS certificate and private key (PEM format).
            The certificate will be hot-swapped without restarting the server.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label htmlFor="cert-pem">Certificate PEM</Label>
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => certFileRef.current?.click()}
                >
                  Browse file
                </Button>
              </div>
              <input
                ref={certFileRef}
                type="file"
                accept=".pem,.crt,.cer"
                className="hidden"
                onChange={handleFileRead(setCertPem)}
              />
              <Textarea
                id="cert-pem"
                rows={6}
                placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
                value={certPem}
                onChange={(e) => setCertPem(e.target.value)}
                className="font-mono text-xs"
              />
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label htmlFor="key-pem">Private Key PEM</Label>
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => keyFileRef.current?.click()}
                >
                  Browse file
                </Button>
              </div>
              <input
                ref={keyFileRef}
                type="file"
                accept=".pem,.key"
                className="hidden"
                onChange={handleFileRead(setKeyPem)}
              />
              <Textarea
                id="key-pem"
                rows={6}
                placeholder="-----BEGIN PRIVATE KEY-----&#10;...&#10;-----END PRIVATE KEY-----"
                value={keyPem}
                onChange={(e) => setKeyPem(e.target.value)}
                className="font-mono text-xs"
              />
            </div>
          </div>
          <Button
            onClick={handleUpload}
            disabled={!certPem || !keyPem || uploadMutation.isPending}
          >
            {uploadMutation.isPending ? 'Uploading...' : 'Upload Certificate'}
          </Button>
        </CardContent>
      </Card>

      {/* History */}
      <div className="space-y-3">
        <h2 className="text-lg font-semibold">Certificate History</h2>
        <DataTable
          columns={historyColumns}
          data={historyData?.items ?? []}
          loading={loadingHistory}
          emptyMessage="No certificate upload history."
        />
      </div>
    </div>
  );
}

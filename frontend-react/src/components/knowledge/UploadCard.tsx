/**
 * Upload documents to the knowledge base (SPEC §3.3 Knowledge, UI #16/#19/#33): a JSON file of
 * `[{"content": "...", "metadata": {...}}]`, checked for size before it is sent, and the full
 * result afterwards: every claim extracted, accepted or quarantined, with its score and conflicts.
 */
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { FileUp } from 'lucide-react';
import { useId, useRef, useState } from 'react';
import { useDataSource } from '@/app/services';
import { ChartTable } from '@/components/charts/ChartParts';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { notify } from '@/components/common/notify';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { qkc } from '@/data/queries';
import { kbClaimStatusLabel } from '@/domain/controlPresentation';
import { formatCount, formatRisk, formatUsd } from '@/domain/format';
import type { IngestResult } from '@/types/api';

export function UploadCard({ writeReason, maxBytes, simulated }: { writeReason: Reason | null; maxBytes: number | null; simulated: boolean }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const inputId = useId();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [localError, setLocalError] = useState<string | null>(null);
  const upload = useMutation({
    mutationFn: (f: File) => ds.uploadKbDocuments(f),
    onSuccess: (result: IngestResult) => {
      notify.success(`${formatCount(result.documents_ingested)} documents added`);
      void queryClient.invalidateQueries({ queryKey: qkc.kb.all });
    },
  });

  const choose = (f: File | null) => {
    upload.reset();
    setLocalError(null);
    if (f && maxBytes !== null && f.size > maxBytes) {
      setLocalError(`The file is ${formatCount(f.size)} bytes; this proxy accepts up to ${formatCount(maxBytes)}.`);
      setFile(null);
      return;
    }
    setFile(f);
  };

  const reason: Reason | null = writeReason ?? (file ? null : { text: 'Choose a JSON file first.' });
  const failure = upload.error ? describeError(upload.error) : null;
  const result = upload.data;

  return (
    <Card>
      <CardHeader
        level={2}
        title="Add documents"
        description='A JSON file: [{"content": "…", "metadata": {"id": "refund-policy.md"}}]. Claims are extracted and checked against what is already in the knowledge base.'
      />
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <label htmlFor={inputId} className="text-sm font-medium text-fg">
            File
          </label>
          <input
            ref={input}
            id={inputId}
            type="file"
            accept="application/json,.json"
            disabled={writeReason !== null}
            onChange={(e) => choose(e.target.files?.[0] ?? null)}
            className="text-sm text-fg file:mr-2 file:h-8 file:rounded-md file:border file:border-border-strong file:bg-surface file:px-3 file:text-sm file:font-medium file:text-fg"
          />
          <DisabledAction reason={reason}>
            {(p) => (
              <Button variant="primary" pending={upload.isPending} onClick={() => file && upload.mutate(file)} {...p}>
                <FileUp aria-hidden="true" />
                Upload
              </Button>
            )}
          </DisabledAction>
        </div>
        {localError ? (
          <p role="alert" className="text-sm text-fg">
            {localError}
          </p>
        ) : null}
        {failure ? (
          <div role="alert" className="space-y-0.5 rounded-md border border-border px-3 py-2">
            <p className="text-sm text-fg">The upload failed: {failure.message}</p>
            {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
          </div>
        ) : null}
        {result ? (
          <section aria-label="Upload result" className="space-y-2">
            <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
              {formatCount(result.documents_ingested)} documents · {formatCount(result.claims_accepted)} claims accepted ·{' '}
              {formatCount(result.claims_quarantined)} quarantined · {formatCount(result.conflicts_detected)} conflicts · cost{' '}
              {formatUsd(result.cost_usd)}
              <EvidenceBadge label={simulated ? 'simulated' : 'measured'} subject="Upload cost" basis="Reported by the judge that extracted and checked the claims." />
            </p>
            {result.warnings.length > 0 ? (
              <ul className="list-disc space-y-0.5 pl-5 text-sm text-fg-muted">
                {result.warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            ) : null}
            {result.claim_results.length > 0 ? (
              <ChartTable
                caption="Claims extracted from the upload"
                columns={[
                  { key: 'claim', label: 'Claim' },
                  { key: 'doc', label: 'Document' },
                  { key: 'status', label: 'Status' },
                  { key: 'score', label: 'Support', numeric: true },
                  { key: 'why', label: 'Why' },
                ]}
                rows={result.claim_results.map((c) => ({
                  claim: <span className="whitespace-normal text-fg">{c.claim}</span>,
                  doc: <span className="font-mono">{c.source_doc_id}</span>,
                  status: kbClaimStatusLabel(c.status),
                  score: formatRisk(c.entailment_score),
                  why: c.quarantine_reason ?? (c.conflicts_with.length > 0 ? `Conflicts with ${c.conflicts_with.join(', ')}` : '—'),
                }))}
              />
            ) : null}
          </section>
        ) : null}
      </CardContent>
    </Card>
  );
}

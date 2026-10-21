// @vitest-environment node
/**
 * Multipart uploads (POST /api/kb/documents), in Node's environment: jsdom's File and Blob do not
 * survive Node's fetch (their bytes arrive as "undefined"), while a browser sends them intact.
 */
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { createControlApi } from '@/test/control/handlers';
import { API, installMswServer, server } from '@/test/msw';
import { createHttpClient } from './http';
import { LiveControlPlane } from './live/liveControlPlane';

installMswServer();

describe('multipart uploads', () => {
  it('sends a FormData body as is, with the browser-made content type', async () => {
    let type: string | null = null;
    let text: string | null = null;
    let csrf: string | null = null;
    server.use(
      http.post(`${API}/api/kb/documents`, async ({ request }) => {
        type = request.headers.get('content-type');
        csrf = request.headers.get('x-trustagent-csrf');
        const form = await request.formData();
        text = await (form.get('file') as Blob).text();
        return HttpResponse.json({ ok: true });
      }),
    );
    const form = new FormData();
    form.append('file', new File(['[]'], 'docs.json'));
    await createHttpClient({ apiBase: API, crossOrigin: false }).request('/api/kb/documents', { method: 'POST', body: form });
    expect(type).toMatch(/^multipart\/form-data; boundary=/);
    expect(text).toBe('[]');
    expect(csrf).toBe('1');
  });

  it('uploads knowledge documents as the file field and reads the full result', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    const plane = new LiveControlPlane(createHttpClient({ apiBase: API, crossOrigin: true }));
    const file = new File(['[{"content":"Refunds within 30 days."}]'], 'docs.json', { type: 'application/json' });
    const result = await plane.uploadKbDocuments(file);
    expect(result.claims_accepted).toBe(5);
    expect(result.claim_results).toHaveLength(2);
    const body = api.callsTo('/api/kb/documents', 'POST')[0].body as { file: { name: string; text: string } };
    expect(body.file).toEqual({ name: 'docs.json', text: '[{"content":"Refunds within 30 days."}]' });
  });
});

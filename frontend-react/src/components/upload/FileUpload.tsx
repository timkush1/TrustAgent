import { useState, useRef } from 'react';

interface FileUploadProps {
  apiBaseUrl?: string;
}

export function FileUpload({ apiBaseUrl = 'http://localhost:8080' }: FileUploadProps) {
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [feedback, setFeedback] = useState<{ type: 'success' | 'error'; message: string } | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const uploadFile = async (file: File) => {
    if (!file.name.endsWith('.json')) {
      setFeedback({ type: 'error', message: 'Only JSON files are supported' });
      return;
    }

    if (file.size > 10 * 1024 * 1024) {
      setFeedback({ type: 'error', message: 'File too large (max 10MB)' });
      return;
    }

    setUploading(true);
    setFeedback(null);

    try {
      const formData = new FormData();
      formData.append('file', file);

      const res = await fetch(`${apiBaseUrl}/api/upload`, {
        method: 'POST',
        body: formData,
      });

      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: 'Upload failed' }));
        throw new Error(err.error || `HTTP ${res.status}`);
      }

      const data = await res.json();
      setFeedback({
        type: 'success',
        message: `Ingested ${data.documents_ingested} documents`,
      });
    } catch (err) {
      setFeedback({ type: 'error', message: err instanceof Error ? err.message : 'Unknown error' });
    } finally {
      setUploading(false);
    }
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragging(false);
    const file = e.dataTransfer.files[0];
    if (file) uploadFile(file);
  };

  const handleFileSelect = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) uploadFile(file);
    // Reset so the same file can be re-selected
    e.target.value = '';
  };

  return (
    <div className="p-4 border-b border-gray-700">
      <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-3">
        Upload Knowledge
      </h3>

      <div
        onDragOver={(e) => { e.preventDefault(); setDragging(true); }}
        onDragLeave={() => setDragging(false)}
        onDrop={handleDrop}
        onClick={() => fileInputRef.current?.click()}
        className="flex flex-col items-center justify-center p-4 rounded-lg border-2 border-dashed cursor-pointer transition-colors"
        style={{
          borderColor: dragging ? '#06b6d4' : '#374151',
          backgroundColor: dragging ? 'rgba(6, 182, 212, 0.05)' : 'transparent',
        }}
      >
        <input
          ref={fileInputRef}
          type="file"
          accept=".json"
          onChange={handleFileSelect}
          className="hidden"
        />

        {uploading ? (
          <span className="text-xs text-cyan-400">Uploading...</span>
        ) : (
          <>
            <span className="text-lg mb-1">📄</span>
            <span className="text-xs text-gray-500">
              Drop JSON file or click to browse
            </span>
            <span className="text-[10px] text-gray-600 mt-1">
              Format: [{"{"}"content": "...", "metadata": {"{}"}...{"}"}]
            </span>
          </>
        )}
      </div>

      {feedback && (
        <div
          className="mt-2 px-3 py-2 rounded-lg text-xs"
          style={{
            backgroundColor: feedback.type === 'success' ? 'rgba(0, 255, 136, 0.1)' : 'rgba(255, 51, 102, 0.1)',
            color: feedback.type === 'success' ? '#00ff88' : '#ff3366',
            border: `1px solid ${feedback.type === 'success' ? 'rgba(0, 255, 136, 0.2)' : 'rgba(255, 51, 102, 0.2)'}`,
          }}
        >
          {feedback.message}
        </div>
      )}
    </div>
  );
}

"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { FileText, RefreshCw, Share2, Trash2, Upload } from "lucide-react";
import { confirmDialog } from "@/components/ConfirmModal";
import PageHeading from "@/components/PageHeading";
import { ActionGroup } from "@/components/ui/ActionGroup";
import { Button } from "@/components/ui/Button";
import { MenuItem, OverflowMenu } from "@/components/ui/OverflowMenu";
import { EmptyState } from "@/components/ui/EmptyState";
import { Pill, type PillVariant } from "@/components/ui/Pill";
import { validateLibraryUploadBatch } from "@/lib/library-upload-contract";
import { LIBRARY_UPLOAD_ACCEPT } from "@neko/llm/library/formats";

type DocumentRow = {
  id: string;
  filename: string;
  relativePath: string;
  sizeBytes: number;
  status: string;
  skipReason: string | null;
  error: string | null;
  createdAt: string;
};

type ConceptRow = {
  id: string;
  path: string;
  type: string;
  title: string;
  description: string | null;
  body: string;
  status: string;
  sources: Array<{ resource: string }>;
  verified: Array<{ by: string; at: string }>;
  updatedAt: string;
};

type PackRow = {
  id: string;
  title: string;
  description: string;
  concepts: number;
};

type LibraryData = {
  isAdmin: boolean;
  documents: DocumentRow[];
  personal: ConceptRow[];
  team: ConceptRow[];
  pending: ConceptRow[];
};

async function fetchLibraryData(signal?: AbortSignal): Promise<LibraryData> {
  const response = await fetch("/api/library", { cache: "no-store", signal });
  if (!response.ok) throw new Error("Library could not be loaded.");
  const data = (await response.json()) as Partial<LibraryData>;
  return {
    isAdmin: data.isAdmin === true,
    documents: data.documents ?? [],
    personal: data.personal ?? [],
    team: data.team ?? [],
    pending: data.pending ?? [],
  };
}

function statusVariant(status: string): PillVariant {
  if (["cataloged", "stable", "approved", "ready"].includes(status)) {
    return "success";
  }
  if (["failed", "declined", "deprecated"].includes(status)) {
    return "danger";
  }
  if (
    [
      "pending",
      "processing",
      "review",
      "extracting",
      "extracted",
      "distilling",
    ].includes(status)
  ) {
    return "watch";
  }
  return "muted";
}

export default function LibraryPage() {
  const [data, setData] = useState<LibraryData>({
    isAdmin: false,
    documents: [],
    personal: [],
    team: [],
    pending: [],
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [packs, setPacks] = useState<PackRow[]>([]);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const importInputRef = useRef<HTMLInputElement | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setData(await fetchLibraryData());
      setError(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Library could not be loaded.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void fetchLibraryData(controller.signal)
      .then((loaded) => {
        setData(loaded);
        setError(null);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setError(
          cause instanceof Error ? cause.message : "Library could not be loaded.",
        );
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, []);

  const hasActiveDocuments = data.documents.some((document) =>
    ["uploaded", "extracting", "extracted", "distilling"].includes(
      document.status,
    ),
  );

  useEffect(() => {
    if (!hasActiveDocuments) return;
    const controller = new AbortController();
    let timer: number;
    const tick = async () => {
      try {
        const loaded = await fetchLibraryData(controller.signal);
        setData(loaded);
        setError(null);
      } catch (cause) {
        if (!controller.signal.aborted) {
          setError(
            cause instanceof Error
              ? cause.message
              : "Library could not be loaded.",
          );
        }
      } finally {
        if (!controller.signal.aborted) {
          timer = window.setTimeout(() => void tick(), 3_000);
        }
      }
    };
    timer = window.setTimeout(() => void tick(), 3_000);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [hasActiveDocuments]);

  const upload = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0) return;
      const selected = Array.from(files);
      const validation = validateLibraryUploadBatch(selected);
      if (!validation.ok) {
        setError(validation.error);
        if (fileInputRef.current) fileInputRef.current.value = "";
        return;
      }
      setUploading(true);
      try {
        const body = new FormData();
        for (const file of selected) {
          body.append("files", file);
        }
        const response = await fetch("/api/library/upload", {
          method: "POST",
          body,
        });
        if (!response.ok) {
          const payload = (await response.json().catch(() => null)) as
            | { error?: string }
            | null;
          throw new Error(payload?.error ?? "The documents could not be imported.");
        }
        setError(null);
        await refresh();
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "Upload failed.");
      } finally {
        setUploading(false);
        if (fileInputRef.current) fileInputRef.current.value = "";
      }
    },
    [refresh],
  );

  const act = useCallback(
    async (id: string, run: () => Promise<Response>, failure: string) => {
      setBusyId(id);
      try {
        const response = await run();
        if (!response.ok) throw new Error(failure);
        setError(null);
        await refresh();
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : failure);
      } finally {
        setBusyId(null);
      }
    },
    [refresh],
  );

  const share = useCallback(
    (id: string) =>
      act(
        id,
        () =>
          fetch(`/api/library/concepts/${encodeURIComponent(id)}/share`, {
            method: "POST",
          }),
        "Concept could not be shared.",
      ),
    [act],
  );

  const decide = useCallback(
    (id: string, action: "approve" | "decline" | "deprecate") =>
      act(
        id,
        () =>
          fetch(`/api/library/concepts/${encodeURIComponent(id)}/decide`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ action }),
          }),
        "Library review could not be saved.",
      ),
    [act],
  );

  const retryDocument = useCallback(
    (id: string) =>
      act(
        id,
        () =>
          fetch(`/api/library/documents/${encodeURIComponent(id)}/retry`, {
            method: "POST",
          }),
        "Retry could not be queued.",
      ),
    [act],
  );

  const removeDocument = useCallback(
    async (id: string) => {
      const ok = await confirmDialog({
        title: "Remove this document from your library?",
        description:
          "Concepts distilled from it will be archived. Files attached in a conversation stay with that conversation.",
        confirmLabel: "Remove",
        destructive: true,
      });
      if (!ok) return;
      await act(
        id,
        () =>
          fetch(`/api/library/documents/${encodeURIComponent(id)}`, {
            method: "DELETE",
          }),
        "Document could not be removed.",
      );
    },
    [act],
  );

  const archiveConcept = useCallback(
    async (id: string) => {
      const ok = await confirmDialog({
        title: "Archive this concept?",
        description: "It will stop appearing in your library search results.",
        confirmLabel: "Archive",
        destructive: true,
      });
      if (!ok) return;
      await act(
        id,
        () =>
          fetch(`/api/library/concepts/${encodeURIComponent(id)}`, {
            method: "DELETE",
          }),
        "Concept could not be archived.",
      );
    },
    [act],
  );

  useEffect(() => {
    if (!data.isAdmin) return;
    const controller = new AbortController();
    fetch("/api/library/packs", { cache: "no-store", signal: controller.signal })
      .then(async (res) => (res.ok ? res.json() : { packs: [] }))
      .then((payload: { packs?: PackRow[] }) => setPacks(payload.packs ?? []))
      .catch(() => {});
    return () => controller.abort();
  }, [data.isAdmin]);

  const exportBundle = useCallback(async () => {
    try {
      const res = await fetch("/api/library/export", { cache: "no-store" });
      if (!res.ok) throw new Error("Export failed.");
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = "library-okf-bundle.json";
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Export failed.");
    }
  }, []);

  const importBundle = useCallback(
    async (files: FileList | null) => {
      const file = files?.[0];
      if (!file) return;
      try {
        const parsed = JSON.parse(await file.text()) as unknown;
        const res = await fetch("/api/library/import", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(parsed),
        });
        if (!res.ok) {
          const payload = (await res.json().catch(() => null)) as
            | { error?: string }
            | null;
          throw new Error(payload?.error ?? "Import failed.");
        }
        setError(null);
        await refresh();
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "Import failed.");
      } finally {
        if (importInputRef.current) importInputRef.current.value = "";
      }
    },
    [refresh],
  );

  const installPack = useCallback(
    (id: string) =>
      act(
        id,
        () =>
          fetch(`/api/library/packs/${encodeURIComponent(id)}/install`, {
            method: "POST",
          }),
        "Pack could not be installed.",
      ),
    [act],
  );

  const { isAdmin, documents, personal, team, pending } = data;

  const conceptRow = (
    concept: ConceptRow,
    index: number,
    actions: React.ReactNode,
  ) => (
    <li key={concept.id} className="memory-review-row">
      <span className="library-index">{String(index + 1).padStart(2, "0")}</span>
      <div className="memory-review-copy">
        <div className="memory-review-meta">
          <span>{concept.type}</span>
          <span>{concept.path}</span>
          <span>{formatDate(concept.updatedAt)}</span>
        </div>
        <p>
          <button
            data-ui-bespoke-reason="inline concept detail toggle"
            type="button"
            className="library-concept-title"
            onClick={() =>
              setExpandedId(expandedId === concept.id ? null : concept.id)
            }
          >
            {concept.title}
          </button>
        </p>
        {concept.description ? <small>{concept.description}</small> : null}
        {expandedId === concept.id ? (
          <div className="library-concept-detail">
            <pre>{concept.body}</pre>
            {concept.sources.length > 0 ? (
              <small>
                Sources:{" "}
                {concept.sources.map((source, i) => (
                  <span key={source.resource}>
                    {i > 0 ? ", " : ""}
                    <a
                      href={`/api/work/files/${source.resource.replace(/^\//, "")}`}
                      target="_blank"
                      rel="noreferrer"
                    >
                      {source.resource.split("/").pop()}
                    </a>
                  </span>
                ))}
              </small>
            ) : null}
            {concept.verified.length > 0 ? (
              <small>
                Verified by {concept.verified[concept.verified.length - 1].by} on{" "}
                {formatDate(concept.verified[concept.verified.length - 1].at)}
              </small>
            ) : null}
          </div>
        ) : null}
      </div>
      <ActionGroup className="memory-review-actions">{actions}</ActionGroup>
    </li>
  );

  return (
    <div className="library-page document-library">
      <PageHeading
        title="Library"
        actions={
          <div className="library-head-stats" aria-label="Library status">
            <div>
              <strong>{String(documents.length).padStart(2, "0")}</strong>
              <span>documents</span>
            </div>
            <div data-state={pending.length > 0 ? "attention" : "clear"}>
              <strong>{String(pending.length).padStart(2, "0")}</strong>
              <span>pending</span>
            </div>
          </div>
        }
      />

      <main className="library-main">
        {error ? (
          <div className="library-error" role="alert">
            <div>
              <strong>Library unavailable</strong>
              <span>{error}</span>
            </div>
            <Button
              variant="danger"
              size="sm"
              className="shrink-0"
              onClick={() => void refresh()}
            >
              Retry
            </Button>
          </div>
        ) : null}

        {pending.length > 0 ? (
          <section className="library-section memory-review">
            <header className="library-section-head">
              <div>
                <span>Review queue</span>
                <h2>Shared with the team</h2>
              </div>
              <strong>{String(pending.length).padStart(2, "0")}</strong>
            </header>
            <ol className="memory-review-list">
              {pending.map((concept, index) =>
                conceptRow(
                  concept,
                  index,
                  <>
                    <Button
                      variant="primary"
                      size="sm"
                      disabled={busyId === concept.id}
                      onClick={() => void decide(concept.id, "approve")}
                    >
                      Approve
                    </Button>
                    <Button
                      size="sm"
                      disabled={busyId === concept.id}
                      onClick={() => void decide(concept.id, "decline")}
                    >
                      Decline
                    </Button>
                  </>,
                ),
              )}
            </ol>
          </section>
        ) : null}

        <section className="library-section">
          <header className="library-section-head">
            <div>
              <span>Uploads</span>
              <h2>Your documents</h2>
              <small id="library-upload-limit" className="library-upload-limit">
                Digital PDF, Word, PowerPoint, Excel, CSV, Markdown, or text;
                up to 100 MB at once.
              </small>
            </div>
            <ActionGroup className="memory-review-actions library-upload-actions">
              <input
                data-ui-bespoke-reason="library file picker"
                ref={fileInputRef}
                type="file"
                multiple
                accept={LIBRARY_UPLOAD_ACCEPT}
                hidden
                aria-describedby="library-upload-limit"
                onChange={(event) => void upload(event.target.files)}
              />
              <Button
                variant="primary"
                disabled={uploading}
                onClick={() => fileInputRef.current?.click()}
              >
                <Upload aria-hidden="true" strokeWidth={2} />
                {uploading ? "Uploading…" : "Upload"}
              </Button>
            </ActionGroup>
          </header>
          {loading ? (
            <div className="library-loading" role="status" aria-label="Loading library">
              <span />
              <span />
              <span />
            </div>
          ) : documents.length === 0 ? (
            <EmptyState
              className="library-empty"
              title="No documents yet"
              body="Upload a document above, or attach one in a conversation. OpenNeko distills durable knowledge into concepts it can cite."
            />
          ) : (
            <ol className="memory-review-list">
              {documents.map((doc, index) => (
                <li key={doc.id} className="memory-review-row">
                  <span className="library-index">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <div className="memory-review-copy">
                    <div className="memory-review-meta">
                      <span>
                        <FileText aria-hidden="true" strokeWidth={2} size={12} />{" "}
                        {doc.filename}
                      </span>
                      <span>{formatSize(doc.sizeBytes)}</span>
                      <span>{formatDate(doc.createdAt)}</span>
                    </div>
                    {doc.status === "skipped" && doc.skipReason ? (
                      <small>{doc.skipReason}</small>
                    ) : null}
                    {doc.status === "failed" && doc.error ? (
                      <small>{doc.error}</small>
                    ) : null}
                  </div>
                  <ActionGroup className="memory-review-actions">
                    <Pill variant={statusVariant(doc.status)}>
                      {humanize(doc.status)}
                    </Pill>
                    {doc.status === "failed" || doc.status === "skipped" ? (
                      <Button
                        size="sm"
                        disabled={busyId === doc.id}
                        onClick={() => void retryDocument(doc.id)}
                        title="Retry extracting and distilling this document"
                      >
                        <RefreshCw aria-hidden="true" strokeWidth={2} />
                        Retry
                      </Button>
                    ) : null}
                    <OverflowMenu label={`Actions for ${doc.filename}`}>
                      <MenuItem
                        danger
                        disabled={busyId === doc.id}
                        onClick={() => void removeDocument(doc.id)}
                      >
                        <Trash2 aria-hidden="true" strokeWidth={2} />
                        Remove from library
                      </MenuItem>
                    </OverflowMenu>
                  </ActionGroup>
                </li>
              ))}
            </ol>
          )}
        </section>

        <section className="library-section">
          <header className="library-section-head">
            <div>
              <span>Personal layer</span>
              <h2>Your concepts</h2>
            </div>
            <strong>{String(personal.length).padStart(2, "0")}</strong>
          </header>
          {personal.length === 0 && !loading ? (
            <EmptyState
              className="library-empty"
              title="No personal concepts"
              body="Distilled concepts stay private here until you share them with the team."
            />
          ) : (
            <ol className="memory-review-list">
              {personal.map((concept, index) =>
                conceptRow(
                  concept,
                  index,
                  <>
                    <Button
                      size="sm"
                      disabled={busyId === concept.id}
                      onClick={() => void share(concept.id)}
                    >
                      <Share2 aria-hidden="true" strokeWidth={2} />
                      Share with team
                    </Button>
                    <OverflowMenu label={`Actions for ${concept.title}`}>
                      <MenuItem
                        danger
                        disabled={busyId === concept.id}
                        onClick={() => void archiveConcept(concept.id)}
                      >
                        <Trash2 aria-hidden="true" strokeWidth={2} />
                        Archive concept
                      </MenuItem>
                    </OverflowMenu>
                  </>,
                ),
              )}
            </ol>
          )}
        </section>

        <section className="library-section">
          <header className="library-section-head">
            <div>
              <span>Team layer</span>
              <h2>Team library</h2>
            </div>
            <strong>{String(team.length).padStart(2, "0")}</strong>
          </header>
          {team.length === 0 && !loading ? (
            <EmptyState
              className="library-empty"
              title="No team concepts"
              body="Shared concepts appear here after an admin approves them for the workspace."
            />
          ) : (
            <ol className="memory-review-list">
              {team.map((concept, index) =>
                conceptRow(
                  concept,
                  index,
                  <>
                    <Pill variant={statusVariant(concept.status)}>
                      {humanize(concept.status)}
                    </Pill>
                    {isAdmin && concept.status === "stable" ? (
                      <OverflowMenu label={`Actions for ${concept.title}`}>
                        <MenuItem
                          danger
                          disabled={busyId === concept.id}
                          onClick={() => void decide(concept.id, "deprecate")}
                          title="Retire from the assistant's knowledge"
                        >
                          <Trash2 aria-hidden="true" strokeWidth={2} />
                          Deprecate concept
                        </MenuItem>
                      </OverflowMenu>
                    ) : null}
                  </>,
                ),
              )}
            </ol>
          )}
        </section>
        {isAdmin ? (
          <section className="library-section">
            <header className="library-section-head">
              <div>
                <span>Admin</span>
                <h2>Portability &amp; starter packs</h2>
              </div>
            </header>
            <ActionGroup
              align="start"
              className="memory-review-actions library-portability"
            >
              <Button size="sm" onClick={() => void exportBundle()}>
                Export OKF bundle
              </Button>
              <input data-ui-bespoke-reason="library file picker"
                ref={importInputRef}
                type="file"
                accept=".json,application/json"
                hidden
                onChange={(event) => void importBundle(event.target.files)}
              />
              <Button
                size="sm"
                onClick={() => importInputRef.current?.click()}
              >
                Import bundle
              </Button>
            </ActionGroup>
            {packs.length > 0 ? (
              <ol className="memory-review-list">
                {packs.map((pack, index) => (
                  <li key={pack.id} className="memory-review-row">
                    <span className="library-index">
                      {String(index + 1).padStart(2, "0")}
                    </span>
                    <div className="memory-review-copy">
                      <div className="memory-review-meta">
                        <span>{pack.id}</span>
                        <span>{pack.concepts} concepts</span>
                      </div>
                      <p>{pack.title}</p>
                      {pack.description ? <small>{pack.description}</small> : null}
                    </div>
                    <ActionGroup className="memory-review-actions">
                      <Button
                        variant="primary"
                        size="sm"
                        disabled={busyId === pack.id}
                        onClick={() => void installPack(pack.id)}
                      >
                        Install
                      </Button>
                    </ActionGroup>
                  </li>
                ))}
              </ol>
            ) : null}
          </section>
        ) : null}
      </main>
    </div>
  );
}

function humanize(value: string): string {
  return value.replace(/_/g, " ");
}

function formatDate(value: string): string {
  return new Date(value).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
  });
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

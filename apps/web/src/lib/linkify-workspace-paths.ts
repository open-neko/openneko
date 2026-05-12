// Shared markdown preprocessor that rewrites agent-mentioned absolute workspace
// paths into clickable links to /api/work/files. Applied wherever assistant
// prose is rendered — RunTimeline's ReactMarkdown invocations and the A2UI
// Markdown component used in synthesized surfaces.

const WORKSPACE_FILE_RE =
  /\/agents\/orgs\/[A-Za-z0-9-]+\/((?:runs|uploads|skills|memory)\/[A-Za-z0-9._\-/]+\.[A-Za-z0-9]+)/;

const INLINE_FILE_RE =
  /`([^`\n]*\/agents\/orgs\/[A-Za-z0-9-]+\/(?:runs|uploads|skills|memory)\/[A-Za-z0-9._\-/]+\.[A-Za-z0-9]+)`/g;

const BARE_FILE_RE =
  /(?<!\]\()(\/[A-Za-z0-9._\-/]*\/agents\/orgs\/[A-Za-z0-9-]+\/(?:runs|uploads|skills|memory)\/[A-Za-z0-9._\-/]+\.[A-Za-z0-9]+)(?!\))/g;

function relPathFor(absolute: string): string | null {
  const m = WORKSPACE_FILE_RE.exec(absolute);
  return m ? m[1] : null;
}

export function linkifyWorkspacePaths(md: string): string {
  if (!md) return md;
  return md
    .replace(INLINE_FILE_RE, (_full, path: string) => {
      const rel = relPathFor(path);
      if (!rel) return `\`${path}\``;
      const filename = path.split("/").slice(-1)[0];
      return `[\`${filename}\`](/api/work/files/${rel})`;
    })
    .replace(BARE_FILE_RE, (full: string, path: string) => {
      const rel = relPathFor(path);
      if (!rel) return full;
      const filename = path.split("/").slice(-1)[0];
      return `[${filename}](/api/work/files/${rel})`;
    });
}

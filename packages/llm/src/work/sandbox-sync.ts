import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { cp, mkdir, readdir, stat } from "node:fs/promises";
import path from "node:path";

type Entry = { hash: string; mode: number } | null;

/** Reconcile the actual filesystem, not a manifest the agent could leave stale. */
export const RECONCILE_DIRECTORY = String.raw`
import hashlib,json,os,pathlib,shutil,sys
root=pathlib.Path(sys.argv[1])
wanted=json.loads(sys.argv[2])
for parent in root.parents:
    if parent.is_symlink(): raise ValueError('sandbox root ancestor is a symlink')
def remove(p):
    if p.is_symlink() or not p.is_dir(): p.unlink()
    else: shutil.rmtree(p)
if root.is_symlink() or (root.exists() and not root.is_dir()): remove(root)
root.mkdir(parents=True,exist_ok=True)
for base,dirs,files in os.walk(root,topdown=True,followlinks=False):
    for name in dirs[:] + files:
        p=pathlib.Path(base)/name
        rel=p.relative_to(root).as_posix()
        if p.is_symlink() or rel not in wanted or (p.is_dir() != (wanted[rel] is None)):
            remove(p)
            if name in dirs: dirs.remove(name)
for rel,entry in wanted.items():
    if entry is None: (root/rel).mkdir(parents=True,exist_ok=True)
def file_hash(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for chunk in iter(lambda: f.read(1048576),b''): h.update(chunk)
    return h.hexdigest()
changed=[]
for rel,entry in wanted.items():
    if entry is None: continue
    p=root/rel
    if not p.is_file() or file_hash(p)!=entry['hash']:
        if p.exists(): remove(p)
        changed.append(rel)
    else: p.chmod(entry['mode'])
print('__openneko_sync__'+json.dumps(changed))
`;

// OpenShell rejects newline characters in command arguments.
export const RECONCILE_COMMAND = `exec(__import__('base64').b64decode('${Buffer.from(RECONCILE_DIRECTORY).toString("base64")}'))`;

export async function syncSandboxDirectory(options: {
  source: string;
  destination: string;
  deltaRoot: string;
  reconcile: (manifest: string) => Promise<string>;
  upload: (directory: string) => Promise<void>;
}): Promise<{ files: number; changed: number; bytes: number }> {
  const entries: Record<string, Entry> = Object.create(null);
  async function walk(directory: string): Promise<void> {
    for (const file of await readdir(directory, { withFileTypes: true })) {
      const full = path.join(directory, file.name);
      const relative = path.relative(options.source, full).split(path.sep).join("/");
      // Staged inputs must never introduce links outside their permitted tree.
      if (file.isSymbolicLink() || (!file.isDirectory() && !file.isFile())) throw new Error("Unsupported sandbox input file");
      if (file.isDirectory()) { entries[relative] = null; await walk(full); }
      else {
        const hash = createHash("sha256");
        for await (const chunk of createReadStream(full)) hash.update(chunk);
        entries[relative] = { hash: hash.digest("hex"), mode: (await stat(full)).mode & 0o777 };
      }
    }
  }
  await walk(options.source);
  const output = await options.reconcile(JSON.stringify(entries));
  const marker = output.split("\n").find(line => line.startsWith("__openneko_sync__"));
  if (!marker) throw new Error("Sandbox file reconciliation did not return a manifest");
  const changed: unknown = JSON.parse(marker.slice("__openneko_sync__".length));
  if (!Array.isArray(changed) || changed.some(file => typeof file !== "string" || !Object.hasOwn(entries, file) || entries[file] === null) || new Set(changed).size !== changed.length) {
    throw new Error("Invalid sandbox file reconciliation result");
  }
  let bytes = 0;
  if (changed.length) {
    const delta = path.join(options.deltaRoot, path.basename(options.destination));
    await mkdir(delta, { recursive: true });
    for (const file of changed as string[]) {
      const target = path.join(delta, file);
      await mkdir(path.dirname(target), { recursive: true });
      await cp(path.join(options.source, file), target);
      bytes += (await stat(target)).size;
    }
    await options.upload(delta);
  }
  return { files: Object.values(entries).filter(Boolean).length, changed: changed.length, bytes };
}

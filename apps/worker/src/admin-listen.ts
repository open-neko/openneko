import { lstatSync, unlinkSync } from "node:fs";
import type { Server } from "node:http";

export function adminSocketPath(env: NodeJS.ProcessEnv = process.env): string | undefined {
  return env.OPENNEKO_WORKER_SOCKET?.trim() || undefined;
}

export function listenAdmin(server: Server, socket: string | undefined, port: number, ready: (where: string) => void): void {
  if (!socket) {
    server.listen(port, () => ready(`http://localhost:${port}`));
    return;
  }
  if (lstatSync(socket, { throwIfNoEntry: false })?.isSocket()) unlinkSync(socket);
  const old = process.umask(0o117);
  try {
    server.listen(socket, () => ready(`unix://${socket}`));
  } finally {
    process.umask(old);
  }
}

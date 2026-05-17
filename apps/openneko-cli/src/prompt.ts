// Minimal interactive prompt helpers — no external deps. Hidden input
// support uses readline's keypress events with stdout writes muted, the
// same trick `read -s` uses in shell. Non-TTY callers should check
// `isInteractive()` first and surface a clear error path instead.
import { createInterface } from "node:readline";

export function isInteractive(): boolean {
  return Boolean(process.stdin.isTTY && process.stdout.isTTY);
}

export async function promptVisible(question: string): Promise<string> {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    return await new Promise<string>((resolve) => {
      rl.question(question, (answer) => resolve(answer));
    });
  } finally {
    rl.close();
  }
}

/**
 * Hidden-input prompt for secrets. Reads from stdin in raw mode so
 * keystrokes never echo. Returns the string the user typed up to
 * Enter (Ctrl-C / Ctrl-D throw).
 */
export async function promptHidden(question: string): Promise<string> {
  if (!process.stdin.isTTY) {
    throw new Error("hidden prompt requires a TTY");
  }
  process.stdout.write(question);
  process.stdin.setRawMode(true);
  process.stdin.resume();
  process.stdin.setEncoding("utf8");
  return new Promise<string>((resolve, reject) => {
    let value = "";
    const onData = (chunk: string) => {
      for (const ch of chunk) {
        const code = ch.charCodeAt(0);
        if (ch === "\n" || ch === "\r") {
          process.stdin.setRawMode(false);
          process.stdin.pause();
          process.stdin.removeListener("data", onData);
          process.stdout.write("\n");
          resolve(value);
          return;
        }
        if (code === 3) {
          // Ctrl-C
          process.stdin.setRawMode(false);
          process.stdin.pause();
          process.stdin.removeListener("data", onData);
          process.stdout.write("\n");
          reject(new Error("aborted"));
          return;
        }
        if (code === 4) {
          // Ctrl-D
          process.stdin.setRawMode(false);
          process.stdin.pause();
          process.stdin.removeListener("data", onData);
          process.stdout.write("\n");
          reject(new Error("eof"));
          return;
        }
        if (code === 8 || code === 127) {
          value = value.slice(0, -1);
          continue;
        }
        value += ch;
      }
    };
    process.stdin.on("data", onData);
  });
}

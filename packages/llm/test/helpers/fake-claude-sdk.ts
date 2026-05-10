export type ClaudeQueryCall = {
  prompt: string | unknown;
  options: Record<string, unknown>;
};

export type ClaudeMockScript = {
  records?: unknown[];
  error?: Error;
  delayMs?: number;
};

export type ClaudeMockController = {
  calls: ClaudeQueryCall[];
  setScript: (script: ClaudeMockScript) => void;
  lastOptions: () => Record<string, unknown> | undefined;
};

export function makeClaudeMockController(): ClaudeMockController {
  const c: ClaudeMockController = {
    calls: [],
    setScript(script) {
      (c as { _script?: ClaudeMockScript })._script = script;
    },
    lastOptions() {
      return c.calls[c.calls.length - 1]?.options;
    },
  };
  return c;
}

export function createMockClaudeQuery(controller: ClaudeMockController) {
  return ({
    prompt,
    options,
  }: {
    prompt: string | unknown;
    options: Record<string, unknown>;
  }) => {
    controller.calls.push({ prompt, options });
    const script: ClaudeMockScript = (controller as { _script?: ClaudeMockScript })._script ?? {
      records: [],
    };
    return scriptedAsyncIterable(script);
  };
}

async function* scriptedAsyncIterable(script: ClaudeMockScript): AsyncIterable<unknown> {
  if (script.error) throw script.error;
  if (script.delayMs) {
    await new Promise<void>((resolve) => setTimeout(resolve, script.delayMs));
  }
  for (const record of script.records ?? []) {
    yield record;
  }
}

export const systemInit = (sessionId: string) => ({
  type: "system",
  subtype: "init",
  session_id: sessionId,
});

export const assistantText = (text: string) => ({
  type: "assistant",
  message: { content: [{ type: "text", text }] },
});

export const assistantToolUse = (id: string, name: string, input: unknown) => ({
  type: "assistant",
  message: { content: [{ type: "tool_use", id, name, input }] },
});

export const userToolResult = (
  toolUseId: string,
  content: unknown,
  isError = false,
) => ({
  type: "user",
  message: {
    content: [
      {
        type: "tool_result",
        tool_use_id: toolUseId,
        content,
        is_error: isError,
      },
    ],
  },
});

export const resultSuccess = (sessionId: string, result: string) => ({
  type: "result",
  subtype: "success",
  session_id: sessionId,
  result,
});

export const resultError = (sessionId: string, subtype: string, result: string) => ({
  type: "result",
  subtype,
  session_id: sessionId,
  result,
});

export const streamEventDelta = (text: string) => ({
  type: "stream_event",
  event: { type: "content_block_delta", delta: { text } },
});

/**
 * Runs items in their given order with at most `concurrency` in flight. The
 * first failure, from `beforeDispatch` or `run`, stops further dispatch; the
 * pool waits for in-flight items and then rethrows that failure.
 */
export async function runBoundedPool<T>(input: {
  items: readonly T[];
  concurrency: number;
  run: (item: T, index: number) => Promise<void>;
  beforeDispatch?: (item: T, index: number) => void | Promise<void>;
}): Promise<void> {
  const width = Math.max(1, Math.min(Math.floor(input.concurrency), input.items.length));
  let next = 0;
  let failure: { cause: unknown } | undefined;
  const worker = async (): Promise<void> => {
    while (!failure && next < input.items.length) {
      const index = next;
      next += 1;
      const item = input.items[index]!;
      try {
        await input.beforeDispatch?.(item, index);
        if (failure) return;
        await input.run(item, index);
      } catch (cause) {
        failure ??= { cause };
      }
    }
  };
  await Promise.all(Array.from({ length: width }, worker));
  if (failure) throw failure.cause;
}

/** Runs tasks one after another, in call order, whatever the caller's concurrency. */
export function createSerialQueue(): <T>(task: () => Promise<T>) => Promise<T> {
  let tail: Promise<unknown> = Promise.resolve();
  return <T>(task: () => Promise<T>): Promise<T> => {
    const result = tail.then(task);
    tail = result.catch(() => undefined);
    return result;
  };
}

export function effectiveConcurrency(
  configured: number,
  driverMax: number | undefined,
): number {
  return Math.max(1, Math.min(configured, driverMax ?? 1));
}

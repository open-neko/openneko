import WorkScreen from "../work-screen";

export default async function WorkThreadPage({
  params,
}: {
  params: Promise<{ threadId: string }>;
}) {
  const { threadId } = await params;
  return <WorkScreen initialThreadId={threadId} />;
}

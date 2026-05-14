import WorkScreen from "./work-screen";

// WorkScreen lives in the layout (not the page) so navigating between
// threads — which only changes the [threadId] segment — doesn't unmount
// the transcript, EventSource, or composer state. The page files are
// intentionally near-empty; WorkScreen reads the active thread from
// `useParams()` and reacts in-place.
export default function WorkLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <>
      <WorkScreen />
      {children}
    </>
  );
}

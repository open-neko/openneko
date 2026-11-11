import { Toaster } from "sonner";

export default function AppToaster() {
  return (
    <Toaster
      position="bottom-right"
      expand
      closeButton
      gap={10}
      offset={20}
      visibleToasts={4}
      toastOptions={{
        duration: 4500,
        unstyled: true,
        classNames: {
          toast: "app-toast",
          title: "app-toast-title",
          description: "app-toast-desc",
          closeButton: "app-toast-close",
          icon: "app-toast-icon",
          content: "app-toast-content",
          actionButton: "app-toast-action",
          cancelButton: "app-toast-cancel",
          success: "app-toast-success",
          error: "app-toast-error",
          info: "app-toast-info",
          warning: "app-toast-warning",
        },
      }}
    />
  );
}

import { createFileRoute } from "@tanstack/react-router";
import { lazy, Suspense } from "react";

// import.meta.env.DEV is false in a production build, so this import is folded
// away and the gallery's module never reaches the bundle.
const Gallery = import.meta.env.DEV ? lazy(() => import("@/gallery")) : null;

export const Route = createFileRoute("/ui/_gallery")({
  component: GalleryPage,
});

function GalleryPage() {
  if (!Gallery) return <p className="p-6 text-sm text-muted-foreground">Not found.</p>;
  return (
    <Suspense fallback={null}>
      <Gallery />
    </Suspense>
  );
}

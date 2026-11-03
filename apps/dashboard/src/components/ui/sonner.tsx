import {
  CircleCheckIcon,
  InfoIcon,
  Loader2Icon,
  OctagonXIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

import { useMediaQuery } from "@/lib/media";
import { useTheme } from "@/lib/theme";

// Bottom-right on a desktop, out of the work's way; at the top on a phone,
// clear of the bottom bar and the thumb. Sonner already pauses its timers
// while the tab is hidden, and leaves the way it came.
const Toaster = (props: ToasterProps) => {
  const { theme } = useTheme();
  const wide = useMediaQuery("(min-width: 48rem)");

  return (
    <Sonner
      theme={theme}
      position={wide ? "bottom-right" : "top-center"}
      className="toaster group"
      icons={{
        success: <CircleCheckIcon className="size-4 text-succeeded" />,
        info: <InfoIcon className="size-4" />,
        warning: <TriangleAlertIcon className="size-4 text-waiting" />,
        error: <OctagonXIcon className="size-4 text-failed" />,
        loading: <Loader2Icon className="size-4 animate-spin" />,
      }}
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--sheet-edge)",
          "--border-radius": "var(--radius)",
        } as React.CSSProperties
      }
      {...props}
    />
  );
};

export { Toaster };

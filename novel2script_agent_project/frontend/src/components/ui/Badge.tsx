import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/classNames";

interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  children: ReactNode;
  tone?: "neutral" | "success" | "warning" | "danger" | "info";
}

export function Badge({ children, className, tone = "neutral", ...props }: BadgeProps) {
  return (
    <span className={cn("ui-badge", `ui-badge-${tone}`, className)} {...props}>
      {children}
    </span>
  );
}

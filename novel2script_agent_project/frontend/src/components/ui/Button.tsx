import { Loader2 } from "lucide-react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/classNames";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  children: ReactNode;
  fullWidth?: boolean;
  loading?: boolean;
  size?: "sm" | "md" | "icon";
  variant?: "default" | "primary" | "ghost" | "soft";
}

export function Button({ children, className, disabled, fullWidth = false, loading = false, size = "md", type = "button", variant = "default", ...props }: ButtonProps) {
  return (
    <button
      className={cn("ui-button", `ui-button-${variant}`, `ui-button-${size}`, fullWidth && "ui-button-full", className)}
      disabled={disabled || loading}
      type={type}
      {...props}
    >
      {loading ? <Loader2 aria-hidden className="spin" size={15} /> : null}
      {children}
    </button>
  );
}

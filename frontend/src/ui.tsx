import { Check, ClipboardCopy, Loader2, Terminal } from "lucide-react";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import "./styles.css";

export function cn(...classes: (string | undefined | null | false)[]): string {
  return classes.filter(Boolean).join(" ");
}

export function GitHubIcon(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" aria-hidden="true" {...props}>
      <path
        fill="currentColor"
        d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z"
      />
    </svg>
  );
}

// ── Button ────────────────────────────────────────────────────────────────────

type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "md" | "sm";
  icon?: boolean;
  loading?: boolean;
};

export function Button({
  variant = "secondary",
  size = "md",
  icon,
  loading,
  className,
  children,
  disabled,
  ...props
}: ButtonProps) {
  return (
    <button
      type="button"
      className={cn(
        "ghp-btn",
        `ghp-btn-${variant}`,
        size === "sm" && "ghp-btn-sm",
        icon && "ghp-btn-icon",
        className,
      )}
      disabled={disabled || loading}
      {...props}
    >
      {loading && <Loader2 size={14} className="ghp-spin" aria-hidden="true" />}
      {children}
    </button>
  );
}

// ── Form controls ─────────────────────────────────────────────────────────────

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className="ghp-field">
      <label className="ghp-label" htmlFor={id}>
        {label}
      </label>
      {children(id)}
      {hint && <p className="ghp-hint">{hint}</p>}
    </div>
  );
}

export function Input({ className, mono, ...props }: React.InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }) {
  return <input className={cn("ghp-input", mono && "ghp-input-mono", className)} {...props} />;
}

export function Select({ className, children, ...props }: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={cn("ghp-select", className)} {...props}>
      {children}
    </select>
  );
}

export function Switch({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className="ghp-switch"
      onClick={() => onChange(!checked)}
    />
  );
}

// ── Display ───────────────────────────────────────────────────────────────────

export function Badge({
  tone = "neutral",
  dot,
  children,
}: {
  tone?: "neutral" | "primary" | "success" | "warning" | "danger" | "merged";
  dot?: boolean;
  children: ReactNode;
}) {
  return (
    <span className={cn("ghp-badge", tone !== "neutral" && `ghp-badge-${tone}`)}>
      {dot && <span className="ghp-dot" aria-hidden="true" />}
      {children}
    </span>
  );
}

export function Banner({
  tone = "neutral",
  icon,
  title,
  children,
  action,
}: {
  tone?: "neutral" | "success" | "warning" | "danger";
  icon?: ReactNode;
  title?: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className={cn("ghp-banner", tone !== "neutral" && `ghp-banner-${tone}`)} role={tone === "danger" ? "alert" : "status"}>
      {icon}
      <div className="ghp-banner-body">
        {title && <p className="ghp-banner-title">{title}</p>}
        {children && <div className={title ? "ghp-muted" : undefined}>{children}</div>}
      </div>
      {action}
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  children,
  action,
}: {
  icon: ReactNode;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="ghp-empty">
      <div className="ghp-empty-icon">{icon}</div>
      <p className="ghp-strong">{title}</p>
      {children && <p className="ghp-muted ghp-small">{children}</p>}
      {action}
    </div>
  );
}

export function Skeleton({ height = 56 }: { height?: number }) {
  return <div className="ghp-skeleton" style={{ height }} aria-hidden="true" />;
}

export function Spinner() {
  return <Loader2 size={26} className="ghp-spin" aria-label="Loading" />;
}

export function Avatar({ name, round }: { name: string; round?: boolean }) {
  return (
    <span className={cn("ghp-avatar", round && "ghp-avatar-round")} aria-hidden="true">
      {name.trim().slice(0, 2)}
    </span>
  );
}

// ── Tabs ──────────────────────────────────────────────────────────────────────

export interface TabDef {
  id: string;
  label: string;
  count?: number;
}

export function Tabs({ tabs, active, onChange }: { tabs: TabDef[]; active: string; onChange: (id: string) => void }) {
  const onKey = (e: React.KeyboardEvent, index: number) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    e.preventDefault();
    const next = (index + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length;
    onChange(tabs[next].id);
    (e.currentTarget.parentElement?.children[next] as HTMLElement | undefined)?.focus();
  };
  return (
    <div className="ghp-tabs" role="tablist">
      {tabs.map((t, i) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          id={`ghp-tab-${t.id}`}
          aria-selected={active === t.id}
          aria-controls={`ghp-panel-${t.id}`}
          tabIndex={active === t.id ? 0 : -1}
          className="ghp-tab"
          onClick={() => onChange(t.id)}
          onKeyDown={(e) => onKey(e, i)}
        >
          {t.label}
          {t.count !== undefined && <span className="ghp-tab-count">{t.count}</span>}
        </button>
      ))}
    </div>
  );
}

export function TabPanel({ id, children }: { id: string; children: ReactNode }) {
  return (
    <div role="tabpanel" id={`ghp-panel-${id}`} aria-labelledby={`ghp-tab-${id}`}>
      {children}
    </div>
  );
}

// ── Modal ─────────────────────────────────────────────────────────────────────

export function Modal({
  open,
  onClose,
  title,
  description,
  wide,
  footer,
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  wide?: boolean;
  footer?: ReactNode;
  children?: ReactNode;
}) {
  const titleId = useId();
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey);
    // Move focus into the dialog so keyboard users land inside it.
    ref.current?.querySelector<HTMLElement>("input, select, button:not([data-close])")?.focus();
    return () => {
      document.removeEventListener("keydown", onKey);
      previous?.focus?.();
    };
  }, [open, onClose]);

  if (!open) return null;
  return (
    <div className="ghp-overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={ref} role="dialog" aria-modal="true" aria-labelledby={titleId} className={cn("ghp-modal", wide && "ghp-modal-wide")}>
        <h2 id={titleId} className="ghp-modal-title">
          {title}
        </h2>
        {description && <p className="ghp-modal-desc">{description}</p>}
        <div className="ghp-modal-body">{children}</div>
        {footer && <div className="ghp-modal-footer">{footer}</div>}
      </div>
    </div>
  );
}

// ── Task-section primitives ───────────────────────────────────────────────────
// Styled to sit beside the host's own task-detail sections (see styles.css).

/** A task-detail section: the host's heading, optional action pills, then content. */
export function Section({
  title,
  actions,
  children,
}: {
  title: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="ghp ghp-sec" aria-label={title}>
      <div className="ghp-sec-head">
        <h3 className="ghp-sec-title">{title}</h3>
        {actions && <div className="ghp-sec-actions">{actions}</div>}
      </div>
      {children}
    </section>
  );
}

export function Pill({
  quiet,
  className,
  children,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { quiet?: boolean }) {
  return (
    <button type="button" className={cn("ghp-pill", quiet && "ghp-pill-quiet", className)} {...props}>
      {children}
    </button>
  );
}

export function RowIconButton({
  danger,
  className,
  children,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { danger?: boolean }) {
  return (
    <button type="button" className={cn("ghp-icon-btn", danger && "ghp-icon-btn-danger", className)} {...props}>
      {children}
    </button>
  );
}

export function SectionEmpty({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <div className="ghp-sec-empty">
      {icon}
      <p>{children}</p>
    </div>
  );
}

export function Segmented<T extends string>({ options, value, onChange, label }: { options: readonly T[]; value: T; onChange: (v: T) => void; label: string }) {
  return (
    <div className="ghp-seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o} type="button" aria-pressed={value === o} onClick={() => onChange(o)}>
          {o}
        </button>
      ))}
    </div>
  );
}

function useCopy(text: string) {
  const [copied, setCopied] = useState(false);
  return {
    copied,
    copy: () => {
      navigator.clipboard?.writeText(text)?.catch(() => {});
      setCopied(true);
      setTimeout(() => setCopied(false), 1800);
    },
  };
}

/** Copy button for a list row (hover action). */
export function CopyIconButton({ text, label }: { text: string; label: string }) {
  const { copied, copy } = useCopy(text);
  return (
    <RowIconButton aria-label={copied ? "Copied" : label} title={label} onClick={copy}>
      {copied ? <Check size={13} style={{ color: "var(--ghp-success)" }} aria-hidden="true" /> : <ClipboardCopy size={13} aria-hidden="true" />}
    </RowIconButton>
  );
}

export function CopyButton({ text, label = "Copy to clipboard" }: { text: string; label?: string }) {
  const { copied, copy } = useCopy(text);
  return (
    <Button variant="ghost" size="sm" icon aria-label={copied ? "Copied" : label} title={label} onClick={copy}>
      {copied ? <Check size={14} style={{ color: "var(--ghp-success)" }} aria-hidden="true" /> : <ClipboardCopy size={14} aria-hidden="true" />}
    </Button>
  );
}

export function CommandBlock({ command }: { command: string }) {
  return (
    <div className="ghp-cmd">
      <Terminal size={14} className="ghp-muted" style={{ flex: "none" }} aria-hidden="true" />
      <code>{command}</code>
      <CopyButton text={command} label="Copy command" />
    </div>
  );
}

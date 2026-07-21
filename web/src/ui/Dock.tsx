import type { LucideIcon } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";

export type PanelPresentation = "sheet" | "pop";
export type PanelSection = "scene" | "session";
export type PanelBadge = "running" | "done" | "stale" | "failed" | "estimated" | "limited";

/**
 * One dock panel, declared as data. Extending the dock — a layers toggle, a
 * compare view, a metrics board — means registering one descriptor here;
 * the strip, grouping, badges, and open/close behavior come for free.
 */
export interface PanelDescriptor {
  id: string;
  icon: LucideIcon;
  hint: string;
  /** scene = how the stage renders; session = depth content about the trace */
  section: PanelSection;
  /** sheet = full-height paper, one at a time; pop = compact card anchored
   * to the strip, coexists with an open sheet */
  presentation: PanelPresentation;
  badge?: PanelBadge | null;
  render: () => ReactNode;
}

interface DockProps {
  panels: PanelDescriptor[];
  openSheet: string | null;
  openPop: string | null;
  onToggle: (panel: PanelDescriptor) => void;
  onClosePop: () => void;
}

export function Dock({ panels, openSheet, openPop, onToggle, onClosePop }: DockProps) {
  const popRef = useRef<HTMLDivElement | null>(null);
  const stripRef = useRef<HTMLDivElement | null>(null);
  const sheet = panels.find((panel) => panel.id === openSheet && panel.presentation === "sheet");
  const pop = panels.find((panel) => panel.id === openPop && panel.presentation === "pop");
  const sections: PanelSection[] = ["scene", "session"];
  const grouped = sections
    .map((section) => ({ section, items: panels.filter((panel) => panel.section === section) }))
    .filter((group) => group.items.length > 0);

  // pops are transient: click-away or Escape dismisses, like any menu; the
  // strip is exempt so the toggle button doesn't dismiss-then-reopen
  useEffect(() => {
    if (!pop) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (popRef.current?.contains(target) || stripRef.current?.contains(target)) return;
      onClosePop();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClosePop();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [pop, onClosePop]);

  return (
    <div className="dock">
      {/* anchored left of the whole dock: beside the sheet when one is open,
          beside the strip otherwise — never covering either */}
      {pop ? (
        <div className="dock-pop" ref={popRef}>
          {pop.render()}
        </div>
      ) : null}
      {sheet ? <aside className="dock-panel">{sheet.render()}</aside> : null}
      <div className="dock-side" ref={stripRef}>
        {/* toggle buttons, not tabs: panels open as pops or sheets with no
            tabpanel relationship, so aria-pressed is the honest semantic */}
        <div className="dock-strip" role="group" aria-label="Stage panels">
          {grouped.map((group, index) => (
            <div key={group.section} className="dock-strip-group">
              {index > 0 ? <div className="dock-strip-divider" aria-hidden /> : null}
              {group.items.map((panel) => {
                const active = panel.id === openSheet || panel.id === openPop;
                const Icon = panel.icon;
                return (
                  <button
                    key={panel.id}
                    aria-pressed={active}
                    className={active ? "active" : ""}
                    onClick={() => onToggle(panel)}
                    data-hint={panel.hint}
                    aria-label={panel.hint}
                  >
                    <Icon size={15} />
                    {panel.badge ? <span className={`dock-dot dock-dot-${panel.badge}`} aria-hidden /> : null}
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

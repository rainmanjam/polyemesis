/* One shared tab controller (WAI-ARIA tabs, manual activation off: arrows, Home
 * and End move and select). Tabs are [role="tab"][data-key]; each names its panel
 * with aria-controls. Only the panels named by this tablist's own tabs are shown
 * or hidden, so the tablist may sit beside its panels or apart from them. */
export function initTabs(
  tablist: HTMLElement,
  onSelect: (key: string) => void,
): { select(key: string): void; destroy(): void } {
  const tabs = [...tablist.querySelectorAll<HTMLElement>('[role="tab"][data-key]')];
  const doc = tablist.ownerDocument;

  const apply = (index: number) => {
    tabs.forEach((tab, i) => {
      const on = i === index;
      tab.setAttribute("aria-selected", String(on));
      tab.tabIndex = on ? 0 : -1;
      const id = tab.getAttribute("aria-controls");
      const panel = id ? doc.getElementById(id) : null;
      if (panel) panel.hidden = !on;
    });
  };

  const select = (key: string) => {
    const index = tabs.findIndex((t) => t.dataset.key === key);
    if (index < 0) return;
    apply(index);
    onSelect(key);
  };

  const move = (index: number) => {
    const next = tabs[(index + tabs.length) % tabs.length];
    next.focus();
    select(next.dataset.key!);
  };

  const onKeydown = (e: KeyboardEvent) => {
    // Alt+Left is Back, Ctrl/Cmd+arrows belong to the browser and the OS: not ours to take.
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    const index = tabs.indexOf((e.target as HTMLElement).closest<HTMLElement>('[role="tab"]')!);
    if (index < 0) return;
    let target: number;
    if (e.key === "ArrowRight") target = index + 1;
    else if (e.key === "ArrowLeft") target = index - 1;
    else if (e.key === "Home") target = 0;
    else if (e.key === "End") target = tabs.length - 1;
    else return;
    e.preventDefault();
    move(target);
  };

  const onClick = (e: Event) => {
    const tab = (e.target as HTMLElement).closest<HTMLElement>('[role="tab"][data-key]');
    if (tab && tabs.includes(tab)) select(tab.dataset.key!);
  };

  tablist.addEventListener("keydown", onKeydown);
  tablist.addEventListener("click", onClick);

  // Initial state follows the markup: the tab already marked selected, else the first.
  if (tabs.length) {
    const initial = tabs.findIndex((t) => t.getAttribute("aria-selected") === "true");
    apply(initial < 0 ? 0 : initial);
  }

  return {
    select,
    destroy() {
      tablist.removeEventListener("keydown", onKeydown);
      tablist.removeEventListener("click", onClick);
    },
  };
}

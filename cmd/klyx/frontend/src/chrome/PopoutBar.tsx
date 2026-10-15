import { ThemeToggle } from "./ThemeToggle";
import { KlyxMark } from "./KlyxMark";
import { drag, noDrag } from "./drag";

// PopoutBar: the bar of a pop-out window (logs). It keeps the main bar's
// geometry - a draggable strip clear of the traffic lights - but none of its
// navigation: a pop-out has no places, history or palette, so Back, Recent,
// the palette and the utilities would be controls that lie. The theme
// toggle stays here: a pop-out has no Settings, and its theme is its own
// window's (the main window's Settings > Appearance does not reach it).
export function PopoutBar({ title }: { title: string }) {
  return (
    <header className="mast" style={drag}>
      <div className="mast-left">
        <KlyxMark size={18} />
        <div className="mast-identity" style={noDrag} title={title}>
          <span className="mast-cluster">{title}</span>
        </div>
      </div>
      <div className="mast-right" style={noDrag}>
        <ThemeToggle />
      </div>
    </header>
  );
}

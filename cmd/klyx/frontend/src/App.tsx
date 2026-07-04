import { useEffect } from "react";
import { AppShell } from "./app/AppShell";
import { initFleetBridge } from "./bridge/fleet";
import { initForwardsBridge } from "./bridge/forwards";
import { refreshNewContextCount } from "./bridge/configsvc";
import { initRoutePersistence } from "./store/persist";

export default function App() {
  useEffect(() => {
    // If cleanup runs before the init promise resolves, the late-arriving
    // unsubscriber must be invoked immediately or its listener leaks forever.
    let cancelled = false;
    let offFleet = () => {};
    let offForwards = () => {};
    const offRoute = initRoutePersistence();
    initFleetBridge().then((u) => (cancelled ? u() : (offFleet = u))).catch((e) => console.error("fleet bridge init", e));
    initForwardsBridge().then((u) => (cancelled ? u() : (offForwards = u))).catch((e) => console.error("forwards bridge init", e));
    void refreshNewContextCount();
    return () => { cancelled = true; offFleet(); offForwards(); offRoute(); };
  }, []);
  return <AppShell />;
}

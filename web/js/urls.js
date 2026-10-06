// Application paths are relative to the configured deployment, never inferred
// from the current page (which may be a schedule detail or editor).
(() => {
    const basePath = document.documentElement.dataset.basePath || "";
    window.Minihub = Object.freeze({
        basePath,
        url: (path) => basePath + path,
        path: () => location.pathname.slice(basePath.length),
        websocketURL: () => `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}${basePath}/api/realtime`,
        // Preserve the existing root keys while separating installations on one host.
        storageKey: (key) => basePath ? `${key}:base:${basePath}` : key,
        scheduleReturnURL: (next) => {
            if (!next || !next.startsWith(basePath + "/schedules") || next.includes("\\")) return basePath + "/";
            const target = new URL(next, location.origin);
            const path = target.pathname.slice(basePath.length);
            return target.origin === location.origin && target.pathname.startsWith(basePath + "/") &&
                (path === "/schedules" || path.startsWith("/schedules/"))
                ? target.pathname + target.search + target.hash : basePath + "/";
        },
    });
})();

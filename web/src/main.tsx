// SPDX-License-Identifier: MIT
import { StrictMode, useSyncExternalStore } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./index.css";
import { createEmbedding } from "./lib/run-config";

const embedding = createEmbedding(window);
function ConsoleRoot() {
  const state = useSyncExternalStore(embedding.subscribe, embedding.getSnapshot);
  if (state.mode === "waiting") return <p role="status">Waiting for run configuration…</p>;
  if (state.mode === "invalid") return <p role="alert">Invalid run configuration</p>;
  return <App run={state.mode === "run" ? state.run : undefined} onTokenExpired={embedding.notifyExpired} onSessionLost={embedding.notifySessionLost} onConnected={embedding.notifyConnected} />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ConsoleRoot />
  </StrictMode>,
);

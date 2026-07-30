import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./i18n";
import "./styles/fonts.css";
import "./styles/tokens.css";
import "./styles.css";
import "./styles/shell.css";
import "./styles/views.css";
import "./styles/responsive.css";
import "./styles/workspace.css";
import "./styles/overview.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

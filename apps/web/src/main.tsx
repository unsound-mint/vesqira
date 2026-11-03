import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./styles/base.css";
import { Examples } from "./examples/Examples";
import { Shell } from "./shell/Shell";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {location.hash === "#/components" ? <Examples /> : <Shell />}
  </StrictMode>,
);

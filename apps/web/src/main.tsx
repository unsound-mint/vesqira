import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./styles/base.css";
import { Examples } from "./examples/Examples";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Examples />
  </StrictMode>,
);

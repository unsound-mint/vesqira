import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./styles/base.css";
import { Examples } from "./examples/Examples";
import { Shell } from "./shell/Shell";
import { ReviewPage } from "./review/ReviewPage";
import { PrintBoard } from "./boards/PrintBoard";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {location.hash === "#/components" ? <Examples /> : location.hash.startsWith("#/print/") ? <PrintBoard boardId={Number(location.hash.slice("#/print/".length))} /> : location.hash.startsWith("#/review/") ? <ReviewPage token={decodeURIComponent(location.hash.slice("#/review/".length))} /> : <Shell />}
  </StrictMode>,
);

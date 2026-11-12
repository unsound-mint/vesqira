import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
import "./styles/base.css";
import { Examples } from "./examples/Examples";
import { ReviewPage } from "./review/ReviewPage";
import { PrintBoard } from "./boards/PrintBoard";
import { AccountGate } from "./account/AccountGate";

// The workbench and the report reader are loaded on demand, so a supervisor opening a reader link on a phone does not
// download the viewer, jobs and analysis code, and the workbench does not carry the reader.
const Shell = lazy(() => import("./shell/Shell").then((m) => ({ default: m.Shell })));
const ReaderPage = lazy(() => import("./read/ReaderPage").then((m) => ({ default: m.ReaderPage })));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Suspense fallback={null}>
      {location.pathname.startsWith("/read/") ? <ReaderPage token={decodeURIComponent(location.pathname.slice("/read/".length))} /> : location.hash === "#/components" ? <Examples /> : location.hash.startsWith("#/print/") ? <PrintBoard boardId={Number(location.hash.slice("#/print/".length))} /> : location.hash.startsWith("#/review/") ? <ReviewPage token={decodeURIComponent(location.hash.slice("#/review/".length))} /> : <AccountGate>{(account) => <Shell account={account} />}</AccountGate>}
    </Suspense>
  </StrictMode>,
);

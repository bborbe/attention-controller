import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { prefetchBoard } from "./api";
import "./styles.css";

prefetchBoard();

createRoot(document.getElementById("root") as HTMLElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
